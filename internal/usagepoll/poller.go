// Package usagepoll is the core usage poller. It fetches usage for idle
// credentials from each provider's usage endpoint and records the results as
// quota readings, so expiring-first routing can rank credentials that have not
// served traffic lately. Busy credentials stay fresh from response headers and
// are not polled. The poller never runs in the request path.
package usagepoll

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotareading"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	// DefaultCheckInterval is how often Run looks for credentials that are due.
	DefaultCheckInterval = time.Minute
	// DefaultPollTimeout bounds one usage call, so a hung call cannot stall
	// the loop or a provider's throttle. It is listed as an exception to the
	// AGENTS.md timeout rule.
	DefaultPollTimeout = 30 * time.Second
)

// Fetcher fetches usage for one provider's credentials. Add one per provider.
type Fetcher interface {
	// Provider is the credential provider this fetcher serves, for example
	// "codex". It is matched case-insensitively against Auth.Provider.
	Provider() string
	// Interval is the per-credential poll interval: a credential is polled
	// on the timer only when it has no quota reading newer than this.
	Interval() time.Duration
	// Eligible reports whether the credential may be polled at all, for
	// example a login rather than an API key, or a free rather than a paid
	// plan. Disabled credentials never reach Eligible.
	Eligible(auth *coreauth.Auth) bool
	// Fetch calls the usage endpoint for one credential and returns its
	// readings, learned at now. An error records nothing, so the last
	// reading stays.
	Fetch(ctx context.Context, auth *coreauth.Auth, now time.Time) ([]quotareading.Reading, error)
}

// ProviderThrottle is an optional interface for a Fetcher whose provider needs
// provider-wide rate limiting or backoff (for example a minimum gap between
// any two calls across all credentials, or a shared backoff after a 429).
// The poller asks Ready before each call and reports every call to Observe.
// A credential skipped because the throttle is not ready stays due.
type ProviderThrottle interface {
	// Ready reports whether the provider may be called at now.
	Ready(now time.Time) bool
	// Observe records the outcome of a call made at now. err is the Fetch
	// error, nil on success; see StatusError for HTTP failures.
	Observe(now time.Time, err error)
}

// CredentialSource lists the credentials to consider. *coreauth.Manager
// satisfies it.
type CredentialSource interface {
	List() []*coreauth.Auth
}

// Requester executes an HTTP request for a credential, injecting its
// credentials and using its proxy settings. *coreauth.Manager satisfies it
// through the provider executor.
type Requester interface {
	HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error)
}

// Config configures a Poller.
type Config struct {
	// Credentials lists the credentials to poll.
	Credentials CredentialSource
	// Readings is where fetched readings are recorded. Its newest reading
	// per credential decides whether the credential is due.
	Readings *quotareading.Store
	// Fetchers holds one fetcher per provider. Providers without a fetcher
	// are never polled.
	Fetchers []Fetcher
	// NowFunc returns the current time. Nil means time.Now.
	NowFunc func() time.Time
	// PollTimeout bounds one usage call. Zero means DefaultPollTimeout.
	PollTimeout time.Duration
}

// Poller polls idle credentials' usage. Call PollDue once at startup and then
// on a timer; Run does both.
type Poller struct {
	credentials CredentialSource
	readings    *quotareading.Store
	fetchers    []Fetcher
	now         func() time.Time
	pollTimeout time.Duration

	// mu serializes PollDue and guards lastAttempt.
	mu sync.Mutex
	// lastAttempt holds when each credential was last polled, successful or
	// not. A credential without an entry has not been polled since start.
	lastAttempt map[string]time.Time
}

// New returns a Poller. Fetchers are tried in provider-name order.
func New(cfg Config) *Poller {
	now := cfg.NowFunc
	if now == nil {
		now = time.Now
	}
	pollTimeout := cfg.PollTimeout
	if pollTimeout <= 0 {
		pollTimeout = DefaultPollTimeout
	}
	fetchers := make([]Fetcher, 0, len(cfg.Fetchers))
	for _, fetcher := range cfg.Fetchers {
		if fetcher != nil {
			fetchers = append(fetchers, fetcher)
		}
	}
	sort.SliceStable(fetchers, func(i, j int) bool { return fetchers[i].Provider() < fetchers[j].Provider() })
	return &Poller{
		credentials: cfg.Credentials,
		readings:    cfg.Readings,
		fetchers:    fetchers,
		now:         now,
		pollTimeout: pollTimeout,
		lastAttempt: make(map[string]time.Time),
	}
}

// Run polls every due credential at once, which is the startup poll, and
// then every checkInterval until ctx is done. A non-positive checkInterval
// means DefaultCheckInterval.
func (p *Poller) Run(ctx context.Context, checkInterval time.Duration) {
	if p == nil {
		return
	}
	if checkInterval <= 0 {
		checkInterval = DefaultCheckInterval
	}
	p.PollDue(ctx)
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.PollDue(ctx)
		}
	}
}

// PollDue polls every credential that is due, one call at a time. A
// credential is due when its provider has a fetcher, it is enabled and
// eligible, and either it has not been polled since the poller started, or
// both its newest quota reading and its last poll are at least the fetcher's
// interval old.
func (p *Poller) PollDue(ctx context.Context) {
	if p == nil || p.credentials == nil || len(p.fetchers) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	auths := p.credentials.List()
	p.forgetRemoved(auths)
	newest := p.newestReadings()
	for _, fetcher := range p.fetchers {
		if ctx.Err() != nil {
			return
		}
		throttle, _ := fetcher.(ProviderThrottle)
		for _, auth := range p.dueCredentials(fetcher, auths, newest) {
			if ctx.Err() != nil {
				return
			}
			now := p.now()
			if throttle != nil && !throttle.Ready(now) {
				break
			}
			errFetch := p.poll(ctx, fetcher, auth, now)
			if throttle != nil {
				throttle.Observe(now, errFetch)
			}
		}
	}
}

func (p *Poller) poll(ctx context.Context, fetcher Fetcher, auth *coreauth.Auth, now time.Time) error {
	p.lastAttempt[auth.ID] = now
	pollCtx, cancel := context.WithTimeout(ctx, p.pollTimeout)
	defer cancel()
	readings, errFetch := fetcher.Fetch(pollCtx, auth, now)
	if errFetch != nil {
		log.Warnf("usage poll | credential=%s provider=%s result=failed error=%v", auth.ID, auth.Provider, errFetch)
		return errFetch
	}
	accepted := p.readings.Record(auth.ID, readings...)
	log.Infof("usage poll | credential=%s provider=%s result=ok readings=%d", auth.ID, auth.Provider, len(accepted))
	return nil
}

// dueCredentials returns the fetcher's due credentials: those never polled
// first, then the stalest, then by ID.
func (p *Poller) dueCredentials(fetcher Fetcher, auths []*coreauth.Auth, newest map[string]time.Time) []*coreauth.Auth {
	now := p.now()
	interval := fetcher.Interval()
	var due []*coreauth.Auth
	for _, auth := range auths {
		if auth == nil || auth.ID == "" || !strings.EqualFold(strings.TrimSpace(auth.Provider), fetcher.Provider()) {
			continue
		}
		if auth.Disabled || auth.Status == coreauth.StatusDisabled || !fetcher.Eligible(auth) {
			continue
		}
		lastAttempt, attempted := p.lastAttempt[auth.ID]
		if attempted && (!olderThan(newest[auth.ID], interval, now) || !olderThan(lastAttempt, interval, now)) {
			continue
		}
		due = append(due, auth)
	}
	sort.SliceStable(due, func(i, j int) bool {
		_, iAttempted := p.lastAttempt[due[i].ID]
		_, jAttempted := p.lastAttempt[due[j].ID]
		if iAttempted != jAttempted {
			return !iAttempted
		}
		iNewest, jNewest := newest[due[i].ID], newest[due[j].ID]
		if !iNewest.Equal(jNewest) {
			return iNewest.Before(jNewest)
		}
		return due[i].ID < due[j].ID
	})
	return due
}

// newestReadings returns the newest LearnedAt per credential.
func (p *Poller) newestReadings() map[string]time.Time {
	out := make(map[string]time.Time)
	for credentialID, readings := range p.readings.Snapshot() {
		for _, reading := range readings {
			if reading.LearnedAt.After(out[credentialID]) {
				out[credentialID] = reading.LearnedAt
			}
		}
	}
	return out
}

// forgetRemoved drops poll history for credentials the source no longer
// lists, so the map does not grow without bound.
func (p *Poller) forgetRemoved(auths []*coreauth.Auth) {
	listed := make(map[string]struct{}, len(auths))
	for _, auth := range auths {
		if auth != nil {
			listed[auth.ID] = struct{}{}
		}
	}
	for id := range p.lastAttempt {
		if _, ok := listed[id]; !ok {
			delete(p.lastAttempt, id)
		}
	}
}

// olderThan reports whether t is zero or at least interval before now.
func olderThan(t time.Time, interval time.Duration, now time.Time) bool {
	return t.IsZero() || !t.After(now.Add(-interval))
}

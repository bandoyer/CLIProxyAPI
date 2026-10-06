package usagepoll_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotareading"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagepoll"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// codexUsageBody is a /wham/usage body for a Pro plan: one weekly main window
// with 30% used, and an account without credits.
const codexUsageBody = `{
	"plan_type": "pro",
	"rate_limit": {
		"allowed": true, "limit_reached": false,
		"primary_window": {"used_percent": 30, "limit_window_seconds": 604800, "reset_after_seconds": 86400, "reset_at": 1791374400},
		"secondary_window": null
	},
	"credits": {"has_credits": false, "unlimited": false, "balance": "0"}
}`

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// upstreamCall is one request the fake Codex upstream received.
type upstreamCall struct {
	Path          string
	Authorization string
	AccountID     string
}

// codexUpstream is an httptest stand-in for chatgpt.com/backend-api. It
// answers /wham/usage with codexUsageBody unless a failure is set for the
// calling account.
type codexUpstream struct {
	server *httptest.Server

	mu       sync.Mutex
	calls    []upstreamCall
	failures map[string]func(http.ResponseWriter)
}

func newCodexUpstream(t *testing.T) *codexUpstream {
	t.Helper()
	upstream := &codexUpstream{failures: make(map[string]func(http.ResponseWriter))}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accountID := r.Header.Get("Chatgpt-Account-Id")
		upstream.mu.Lock()
		upstream.calls = append(upstream.calls, upstreamCall{
			Path:          r.URL.Path,
			Authorization: r.Header.Get("Authorization"),
			AccountID:     accountID,
		})
		fail := upstream.failures[accountID]
		upstream.mu.Unlock()
		if fail != nil {
			fail(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(codexUsageBody))
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

func (u *codexUpstream) failFor(accountID string, respond func(http.ResponseWriter)) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.failures[accountID] = respond
}

// takeCalls returns the calls received since the last takeCalls.
func (u *codexUpstream) takeCalls() []upstreamCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	calls := u.calls
	u.calls = nil
	return calls
}

func calledAccounts(calls []upstreamCall) map[string]int {
	out := make(map[string]int)
	for _, call := range calls {
		out[call.AccountID]++
	}
	return out
}

type codexPollHarness struct {
	clock    *fakeClock
	upstream *codexUpstream
	manager  *coreauth.Manager
	poller   *usagepoll.Poller
}

// newCodexPollHarness builds a manager with the real Codex executor, so the
// poll goes through the same credential injection and HTTP client as
// requests, and a poller with a Codex fetcher pointed at an httptest upstream.
func newCodexPollHarness(t *testing.T) *codexPollHarness {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, time.October, 6, 12, 0, 0, 0, time.UTC)}
	upstream := newCodexUpstream(t)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewCodexExecutor(&config.Config{}))
	poller := usagepoll.New(usagepoll.Config{
		Credentials: manager,
		Readings:    manager.QuotaReadings(),
		NowFunc:     clock.Now,
		Fetchers:    []usagepoll.Fetcher{usagepoll.NewCodexFetcher(manager, upstream.server.URL)},
	})
	return &codexPollHarness{clock: clock, upstream: upstream, manager: manager, poller: poller}
}

// addCodexCredential registers a Codex login credential whose access token is
// "token-<accountID>".
func (h *codexPollHarness) addCodexCredential(t *testing.T, id, accountID string, mutate ...func(*coreauth.Auth)) {
	t.Helper()
	auth := &coreauth.Auth{
		ID:       id,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "token-" + accountID,
			"account_id":   accountID,
		},
	}
	for _, fn := range mutate {
		fn(auth)
	}
	if _, errRegister := h.manager.Register(coreauth.WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register(%s) error = %v", id, errRegister)
	}
}

func (h *codexPollHarness) pollDue(t *testing.T) []upstreamCall {
	t.Helper()
	h.poller.PollDue(context.Background())
	return h.upstream.takeCalls()
}

func readingByWindow(readings []quotareading.Reading, window string) (quotareading.Reading, bool) {
	for _, reading := range readings {
		if reading.Window == window {
			return reading, true
		}
	}
	return quotareading.Reading{}, false
}

func TestUsagePollerPollsEveryEnabledCodexCredentialOnceAtStartup(t *testing.T) {
	h := newCodexPollHarness(t)
	h.addCodexCredential(t, "codex-a.json", "acct-a")
	h.addCodexCredential(t, "codex-b.json", "acct-b")

	calls := h.pollDue(t)

	if len(calls) != 2 {
		t.Fatalf("startup poll made %d calls, want 2: %+v", len(calls), calls)
	}
	for _, call := range calls {
		if call.Path != "/wham/usage" {
			t.Errorf("poll path = %q, want /wham/usage", call.Path)
		}
		if want := "Bearer token-" + call.AccountID; call.Authorization != want {
			t.Errorf("poll for %s sent Authorization %q, want %q", call.AccountID, call.Authorization, want)
		}
	}
	if got := calledAccounts(calls); got["acct-a"] != 1 || got["acct-b"] != 1 {
		t.Fatalf("startup poll called accounts %v, want acct-a and acct-b once each", got)
	}

	for _, id := range []string{"codex-a.json", "codex-b.json"} {
		reading, ok := readingByWindow(h.manager.QuotaReadings().Readings(id, ""), quotareading.CodexPrimaryWindow)
		if !ok {
			t.Fatalf("credential %s has no %s reading after the startup poll", id, quotareading.CodexPrimaryWindow)
		}
		if reading.Source != quotareading.SourcePoll || !reading.LearnedAt.Equal(h.clock.Now()) {
			t.Errorf("credential %s reading source=%v learnedAt=%v, want poll at %v", id, reading.Source, reading.LearnedAt, h.clock.Now())
		}
		if reading.ShareLeft < 0.69 || reading.ShareLeft > 0.71 {
			t.Errorf("credential %s share left = %v, want 0.70", id, reading.ShareLeft)
		}
	}
}

func TestUsagePollerExhaustedWindowMarksTheCredentialQuotaExceeded(t *testing.T) {
	h := newCodexPollHarness(t)
	// The manager's quota-exceeded mark compares the reset time with the wall
	// clock, so this poll happens at the current time.
	h.clock.now = time.Now()
	h.addCodexCredential(t, "codex-a.json", "acct-a")
	h.upstream.failFor("acct-a", func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"rate_limit": {
				"allowed": false, "limit_reached": true,
				"primary_window": {"used_percent": 100, "limit_window_seconds": 604800, "reset_after_seconds": 86400}
			},
			"credits": {"has_credits": false, "unlimited": false, "balance": "0"}
		}`))
	})

	h.pollDue(t)

	auth, ok := h.manager.GetByID("codex-a.json")
	if !ok {
		t.Fatal("credential codex-a.json not found")
	}
	wantReset := h.clock.Now().Add(24 * time.Hour)
	if !auth.Quota.Exceeded || auth.Quota.Reason != "credential_quota" || auth.Quota.NextRecoverAt.Sub(wantReset).Abs() > time.Second {
		t.Fatalf("quota after polling an exhausted window = %+v, want credential_quota exceeded until %v", auth.Quota, wantReset)
	}
}

// recordHeaderReading stands in for a response that served traffic on the
// credential: the response headers give it a fresh reading at the clock time.
func (h *codexPollHarness) recordHeaderReading(id string) {
	h.manager.QuotaReadings().Record(id, quotareading.Reading{
		Window:    quotareading.CodexPrimaryWindow,
		Kind:      quotareading.KindRanking,
		Length:    7 * 24 * time.Hour,
		ShareLeft: 0.5,
		ResetAt:   h.clock.Now().Add(24 * time.Hour),
		LearnedAt: h.clock.Now(),
		Source:    quotareading.SourceHeader,
	})
}

func TestUsagePollerPollsOnTheTimerOnlyCodexCredentialsWithoutAReadingNewerThanFiveMinutes(t *testing.T) {
	h := newCodexPollHarness(t)
	h.addCodexCredential(t, "codex-busy.json", "acct-busy")
	h.addCodexCredential(t, "codex-idle.json", "acct-idle")
	h.pollDue(t) // startup poll at t0

	h.clock.Advance(3 * time.Minute)
	h.recordHeaderReading("codex-busy.json") // traffic at t0+3m

	h.clock.Advance(time.Minute) // t0+4m: every reading is newer than 5 minutes
	if calls := h.pollDue(t); len(calls) != 0 {
		t.Fatalf("poll at t0+4m made calls %+v, want none", calls)
	}

	h.clock.Advance(time.Minute) // t0+5m: only the idle credential's reading is 5 minutes old
	if got := calledAccounts(h.pollDue(t)); len(got) != 1 || got["acct-idle"] != 1 {
		t.Fatalf("poll at t0+5m called %v, want only acct-idle", got)
	}

	h.clock.Advance(3 * time.Minute) // t0+8m: the busy credential has been idle for 5 minutes
	if got := calledAccounts(h.pollDue(t)); len(got) != 1 || got["acct-busy"] != 1 {
		t.Fatalf("poll at t0+8m called %v, want only acct-busy", got)
	}
}

func TestUsagePollerKeepsThePreviousReadingWhenAPollFails(t *testing.T) {
	h := newCodexPollHarness(t)
	h.addCodexCredential(t, "codex-a.json", "acct-a")
	h.pollDue(t) // startup poll at t0 succeeds
	startup := h.clock.Now()

	failures := []struct {
		name    string
		respond func(http.ResponseWriter)
	}{
		{name: "server error", respond: func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) }},
		{name: "rate limited", respond: func(w http.ResponseWriter) { w.WriteHeader(http.StatusTooManyRequests) }},
		{name: "login page instead of JSON", respond: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>login</html>"))
		}},
	}
	for _, failure := range failures {
		h.upstream.failFor("acct-a", failure.respond)
		h.clock.Advance(5 * time.Minute)
		if calls := h.pollDue(t); len(calls) != 1 {
			t.Fatalf("%s: poll made %d calls, want 1", failure.name, len(calls))
		}
		reading, ok := readingByWindow(h.manager.QuotaReadings().Readings("codex-a.json", ""), quotareading.CodexPrimaryWindow)
		if !ok || !reading.LearnedAt.Equal(startup) || reading.ShareLeft < 0.69 || reading.ShareLeft > 0.71 {
			t.Fatalf("%s: reading after the failed poll = %+v (found %v), want the startup reading kept", failure.name, reading, ok)
		}
	}

	// A failed credential is retried one interval after the failed poll, not
	// on every check.
	h.clock.Advance(4 * time.Minute)
	if calls := h.pollDue(t); len(calls) != 0 {
		t.Fatalf("poll 4 minutes after a failed poll made calls %+v, want none", calls)
	}
	h.clock.Advance(time.Minute)
	if calls := h.pollDue(t); len(calls) != 1 {
		t.Fatalf("poll 5 minutes after a failed poll made %d calls, want 1", len(calls))
	}
}

func TestUsagePollerNeverPollsDisabledCredentialsOrCodexAPIKeys(t *testing.T) {
	h := newCodexPollHarness(t)
	h.addCodexCredential(t, "codex-enabled.json", "acct-enabled")
	h.addCodexCredential(t, "codex-disabled-flag.json", "acct-disabled-flag", func(a *coreauth.Auth) { a.Disabled = true })
	h.addCodexCredential(t, "codex-disabled-status.json", "acct-disabled-status", func(a *coreauth.Auth) { a.Status = coreauth.StatusDisabled })
	h.addCodexCredential(t, "codex-api-key", "acct-api-key", func(a *coreauth.Auth) {
		a.Attributes = map[string]string{"api_key": "sk-test"}
	})

	for i := 0; i < 3; i++ {
		got := calledAccounts(h.pollDue(t))
		if got["acct-enabled"] != 1 {
			t.Fatalf("poll %d called %v, want the enabled credential polled once", i, got)
		}
		for account := range got {
			if account != "acct-enabled" {
				t.Fatalf("poll %d called %s, want only enabled Codex logins polled (got %v)", i, account, got)
			}
		}
		h.clock.Advance(5 * time.Minute)
	}
}

// gappedCodexFetcher is the Codex fetcher with a provider-wide throttle that
// allows one call per gap, the shape a Claude fetcher needs.
type gappedCodexFetcher struct {
	*usagepoll.CodexFetcher
	gap      time.Duration
	next     time.Time
	observed []error
}

func (f *gappedCodexFetcher) Ready(now time.Time) bool { return !now.Before(f.next) }

func (f *gappedCodexFetcher) Observe(now time.Time, err error) {
	f.next = now.Add(f.gap)
	var statusErr *usagepoll.StatusError
	if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusTooManyRequests && statusErr.RetryAfter > 0 {
		f.next = now.Add(statusErr.RetryAfter)
	}
	f.observed = append(f.observed, err)
}

func TestUsagePollerHoldsDueCredentialsWhileTheProviderThrottleIsClosed(t *testing.T) {
	h := newCodexPollHarness(t)
	fetcher := &gappedCodexFetcher{CodexFetcher: usagepoll.NewCodexFetcher(h.manager, h.upstream.server.URL), gap: 10 * time.Minute}
	h.poller = usagepoll.New(usagepoll.Config{
		Credentials: h.manager,
		Readings:    h.manager.QuotaReadings(),
		NowFunc:     h.clock.Now,
		Fetchers:    []usagepoll.Fetcher{fetcher},
	})
	h.addCodexCredential(t, "codex-a.json", "acct-a")
	h.addCodexCredential(t, "codex-b.json", "acct-b")
	h.upstream.failFor("acct-a", func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", "1200")
		w.WriteHeader(http.StatusTooManyRequests)
	})

	if got := calledAccounts(h.pollDue(t)); len(got) != 1 || got["acct-a"] != 1 {
		t.Fatalf("first startup poll called %v, want only acct-a (the throttle allows one call)", got)
	}
	var statusErr *usagepoll.StatusError
	if len(fetcher.observed) != 1 || !errors.As(fetcher.observed[0], &statusErr) || statusErr.StatusCode != http.StatusTooManyRequests || statusErr.RetryAfter != 20*time.Minute {
		t.Fatalf("throttle observed %v, want one 429 with Retry-After 20m", fetcher.observed)
	}

	h.clock.Advance(10 * time.Minute) // the gap passed, but Retry-After holds the provider
	if calls := h.pollDue(t); len(calls) != 0 {
		t.Fatalf("poll inside Retry-After made calls %+v, want none", calls)
	}

	h.clock.Advance(10 * time.Minute) // acct-b was never polled, so it goes first
	if got := calledAccounts(h.pollDue(t)); len(got) != 1 || got["acct-b"] != 1 {
		t.Fatalf("poll after Retry-After called %v, want acct-b, which still owed its startup poll", got)
	}
	if fetcher.observed[1] != nil {
		t.Fatalf("throttle observed %v for a successful call, want nil", fetcher.observed[1])
	}
}

func TestUsagePollerDoesNotPollDuringAPick(t *testing.T) {
	h := newCodexPollHarness(t)
	h.addCodexCredential(t, "codex-a.json", "acct-a")

	for i := 0; i < 3; i++ {
		if _, errSelect := h.manager.SelectAuth(context.Background(), "codex", "", cliproxyexecutor.Options{}); errSelect != nil {
			t.Fatalf("SelectAuth() error = %v", errSelect)
		}
	}
	if calls := h.upstream.takeCalls(); len(calls) != 0 {
		t.Fatalf("picks made usage calls %+v, want none", calls)
	}
	if readings := h.manager.QuotaReadings().Readings("codex-a.json", ""); len(readings) != 0 {
		t.Fatalf("picks recorded readings %+v, want none", readings)
	}
}

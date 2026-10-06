package auth

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotareading"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// ExpiringFirstConfig configures the expiring-first selector.
type ExpiringFirstConfig struct {
	// Readings is the quota-reading store the selector ranks by. Use the
	// manager's store (Manager.QuotaReadings) so readings outlive selectors.
	Readings *quotareading.Store
	// NowFunc is the clock urgency and availability checks read. Nil means
	// time.Now. Tests inject a controllable clock here instead of sleeping.
	NowFunc func() time.Time
}

// ExpiringFirstSelector implements expiring-first routing: among the highest
// priority tier's usable credentials it picks the one with the highest
// urgency (percent left in the ranking window ÷ hours to its reset time).
// It is a core selector, not a scheduler plugin (ADR 0001).
type ExpiringFirstSelector struct {
	readings *quotareading.Store
	nowFunc  func() time.Time

	// mu guards lastNoData, the round-robin position among credentials with
	// no reading, per provider and model.
	mu         sync.Mutex
	lastNoData map[string]string
}

// maxExpiringFirstRotationKeys bounds lastNoData, as RoundRobinSelector does.
const maxExpiringFirstRotationKeys = 4096

// NewExpiringFirstSelector creates an expiring-first selector.
func NewExpiringFirstSelector(cfg ExpiringFirstConfig) *ExpiringFirstSelector {
	nowFunc := cfg.NowFunc
	if nowFunc == nil {
		nowFunc = time.Now
	}
	return &ExpiringFirstSelector{readings: cfg.Readings, nowFunc: nowFunc}
}

func (s *ExpiringFirstSelector) now() time.Time {
	if s != nil && s.nowFunc != nil {
		return s.nowFunc()
	}
	return time.Now()
}

// Pick selects the most urgent usable credential.
func (s *ExpiringFirstSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := s.now()
	available, errAvailable := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if errAvailable != nil {
		return nil, errAvailable
	}
	available, errUsable := s.dropExhausted(available, provider, model, now)
	if errUsable != nil {
		return nil, errUsable
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)

	var best *Auth
	bestUrgency := 0.0
	noData := make([]*Auth, 0, len(available))
	for _, candidate := range available {
		urgency, known := s.urgency(candidate.ID, model, now)
		if !known {
			noData = append(noData, candidate)
			continue
		}
		if best == nil || urgency > bestUrgency {
			best, bestUrgency = candidate, urgency
		}
	}
	if best != nil {
		logExpiringFirstPick(ctx, best, bestUrgency, true, expiringFirstReasonMoreUrgent, expiringFirstThread(opts.Metadata), provider, model)
		return best, nil
	}
	picked := s.nextNoData(provider+":"+canonicalModelKey(model), noData)
	logExpiringFirstPick(ctx, picked, 0, false, expiringFirstReasonNoData, expiringFirstThread(opts.Metadata), provider, model)
	return picked, nil
}

// dropExhausted removes credentials whose quota reading shows an exhausted
// window that has not reset yet. When no credential is left, it returns a
// quota cooldown error that lasts until the soonest such reset.
func (s *ExpiringFirstSelector) dropExhausted(auths []*Auth, provider, model string, now time.Time) ([]*Auth, error) {
	usable := make([]*Auth, 0, len(auths))
	var soonestReset time.Time
	for _, candidate := range auths {
		exhaustedUntil := s.exhaustedUntil(candidate.ID, model, now)
		if exhaustedUntil.IsZero() {
			usable = append(usable, candidate)
			continue
		}
		if soonestReset.IsZero() || exhaustedUntil.Before(soonestReset) {
			soonestReset = exhaustedUntil
		}
	}
	if len(usable) > 0 || len(auths) == 0 {
		return usable, nil
	}
	providerForError := provider
	if providerForError == "mixed" {
		providerForError = ""
	}
	return nil, newModelCooldownError(model, providerForError, soonestReset.Sub(now))
}

// exhaustedUntil returns when the credential becomes usable again by its
// quota reading: the latest reset among its exhausted windows that have not
// reset yet. Zero means no window blocks it. A window without a reset time
// never blocks, because nothing would end the block.
func (s *ExpiringFirstSelector) exhaustedUntil(credentialID, model string, now time.Time) time.Time {
	if s == nil || s.readings == nil {
		return time.Time{}
	}
	var until time.Time
	for _, reading := range s.readings.Readings(credentialID, canonicalModelKey(model)) {
		if !reading.Exhausted() || !reading.ResetAt.After(now) {
			continue
		}
		if reading.ResetAt.After(until) {
			until = reading.ResetAt
		}
	}
	return until
}

// Pick-log reasons. The routing report parses these tokens.
const (
	expiringFirstReasonBindingKept = "binding_kept"
	expiringFirstReasonMoreUrgent  = "more_urgent"
	expiringFirstReasonNoData      = "no_data"
)

// observeBindingKept logs a pick that affinity served from an existing
// binding, so every pick produces one expiring-first log line. The session
// affinity selector calls it on its fallback.
func (s *ExpiringFirstSelector) observeBindingKept(ctx context.Context, provider, model, thread string, auth *Auth) {
	if s == nil || auth == nil {
		return
	}
	urgency, known := s.urgency(auth.ID, model, s.now())
	logExpiringFirstPick(ctx, auth, urgency, known, expiringFirstReasonBindingKept, thread, provider, model)
}

// expiringFirstThread returns the thread identifier the affinity resolver
// read, when affinity wraps this selector.
func expiringFirstThread(metadata map[string]any) string {
	return sessionMetadataString(metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey)
}

// logExpiringFirstPick writes the pick log line: credential, urgency (or
// no_data), reason and thread. It never logs tokens, keys or prompt text.
func logExpiringFirstPick(ctx context.Context, auth *Auth, urgency float64, known bool, reason, thread, provider, model string) {
	if auth == nil {
		return
	}
	urgencyText := "no_data"
	if known {
		urgencyText = fmt.Sprintf("%.2f%%/h", urgency)
	}
	if thread == "" {
		thread = "-"
	}
	selectorLogEntry(ctx).Infof("expiring-first pick | credential=%s urgency=%s reason=%s thread=%s provider=%s model=%s",
		auth.ID, urgencyText, reason, thread, provider, model)
}

// nextNoData rotates through credentials with no reading, resuming after the
// previous pick even when the candidate set changed in between.
func (s *ExpiringFirstSelector) nextNoData(key string, candidates []*Auth) *Auth {
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastNoData == nil {
		s.lastNoData = make(map[string]string)
	}
	if _, ok := s.lastNoData[key]; !ok && len(s.lastNoData) >= maxExpiringFirstRotationKeys {
		s.lastNoData = make(map[string]string)
	}
	picked := candidates[successorIndex(candidates, s.lastNoData[key])]
	s.lastNoData[key] = picked.ID
	return picked
}

// urgency returns the credential's urgency in percent per hour, from its
// ranking window: the longest KindRanking reading. A reading whose reset time
// has passed counts as a full window that resets one window length from now.
func (s *ExpiringFirstSelector) urgency(credentialID, model string, now time.Time) (float64, bool) {
	if s == nil || s.readings == nil {
		return 0, false
	}
	var ranking *quotareading.Reading
	readings := s.readings.Readings(credentialID, canonicalModelKey(model))
	for i := range readings {
		reading := readings[i]
		if reading.Kind != quotareading.KindRanking || reading.ResetAt.IsZero() {
			continue
		}
		if ranking == nil || reading.Length > ranking.Length {
			ranking = &readings[i]
		}
	}
	if ranking == nil {
		return 0, false
	}
	shareLeft, untilReset := ranking.ShareLeft, ranking.ResetAt.Sub(now)
	if untilReset <= 0 {
		if ranking.Length <= 0 {
			return 0, false
		}
		shareLeft, untilReset = 1, ranking.Length
	}
	return shareLeft * 100 / untilReset.Hours(), true
}

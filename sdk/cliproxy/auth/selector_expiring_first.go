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

// tierRankingSelector is a selector that keeps the highest priority tier
// itself, after dropping credentials by its own usability rules. Callers
// offer it every tier, so a tier whose credentials it drops falls through to
// the next one.
type tierRankingSelector interface {
	ranksPriorityTiers()
}

func (s *ExpiringFirstSelector) ranksPriorityTiers() {}

// selectorTierCandidates narrows available credentials to the highest
// priority tier, unless the selector keeps the tier itself.
func selectorTierCandidates(selector Selector, available []*Auth) []*Auth {
	if _, ranksTiers := selector.(tierRankingSelector); ranksTiers {
		return available
	}
	return highestPriorityAuths(available)
}

// Pick selects the most urgent usable credential. It drops credentials with
// an exhausted quota window, keeps the highest priority tier left, and ranks
// it: credentials with a known urgency first, then credentials with no
// reading, then credentials that can serve only from a credit balance.
// Candidates from different providers compete in one ranking.
func (s *ExpiringFirstSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := s.now()
	available, errAvailable := getSelectorAvailableAuthsAcrossPriorities(ctx, auths, provider, model, now)
	if errAvailable != nil {
		return nil, errAvailable
	}
	usable, creditOnly, errUsable := s.usableByQuota(available, provider, model, now)
	if errUsable != nil {
		return nil, errUsable
	}
	usable = highestPriorityAuths(usable)
	usable = preferCodexWebsocketAuths(ctx, provider, usable)

	thread := expiringFirstThread(opts.Metadata)
	var best *Auth
	bestUrgency := 0.0
	noData := make([]*Auth, 0, len(usable))
	credits := make([]*Auth, 0, len(usable))
	for _, candidate := range usable {
		if creditOnly[candidate.ID] {
			credits = append(credits, candidate)
			continue
		}
		urgency, known := s.urgency(candidate.ID, model, now)
		if !known {
			noData = append(noData, candidate)
			continue
		}
		if best == nil || urgency > bestUrgency {
			best, bestUrgency = candidate, urgency
		}
	}
	rotationKey := provider + ":" + canonicalModelKey(model)
	switch {
	case best != nil:
		logExpiringFirstPick(ctx, best, bestUrgency, true, expiringFirstReasonMoreUrgent, thread, provider, model)
		return best, nil
	case len(noData) > 0:
		picked := s.nextInTurn(expiringFirstReasonNoData+":"+rotationKey, noData)
		logExpiringFirstPick(ctx, picked, 0, false, expiringFirstReasonNoData, thread, provider, model)
		return picked, nil
	default:
		picked := s.nextInTurn(expiringFirstReasonCreditOnly+":"+rotationKey, credits)
		logExpiringFirstPick(ctx, picked, 0, false, expiringFirstReasonCreditOnly, thread, provider, model)
		return picked, nil
	}
}

// quotaStanding is what a credential's quota reading says about its use.
type quotaStanding int

const (
	// standingSubscription serves from subscription quota, ranked by urgency
	// (or as no data when no ranking window is known).
	standingSubscription quotaStanding = iota
	// standingCreditOnly can serve only from a credit balance, so it ranks
	// after every other usable credential: credits don't expire at a reset.
	standingCreditOnly
	// standingBlocked can't serve until its exhausted windows reset.
	standingBlocked
)

// usableByQuota drops credentials whose quota reading blocks them and marks
// the credit-only ones. When every credential is blocked, it returns a quota
// cooldown error that lasts until the soonest reset that unblocks one.
func (s *ExpiringFirstSelector) usableByQuota(auths []*Auth, provider, model string, now time.Time) ([]*Auth, map[string]bool, error) {
	usable := make([]*Auth, 0, len(auths))
	creditOnly := make(map[string]bool)
	var soonestReset time.Time
	for _, candidate := range auths {
		standing, blockedUntil := s.quotaStanding(candidate.ID, model, now)
		switch standing {
		case standingBlocked:
			if !blockedUntil.IsZero() && (soonestReset.IsZero() || blockedUntil.Before(soonestReset)) {
				soonestReset = blockedUntil
			}
			continue
		case standingCreditOnly:
			creditOnly[candidate.ID] = true
		}
		usable = append(usable, candidate)
	}
	if len(usable) > 0 || len(auths) == 0 {
		return usable, creditOnly, nil
	}
	if soonestReset.IsZero() {
		return nil, nil, newAuthUnavailableError(soonestReset, now)
	}
	providerForError := provider
	if providerForError == "mixed" {
		providerForError = ""
	}
	return nil, nil, newModelCooldownError(model, providerForError, soonestReset.Sub(now))
}

// quotaStanding reads the credential's quota windows for the model. An
// exhausted window blocks the credential until its reset time, unless a
// credit balance is left, which makes it credit-only. A window that has
// already reset counts as full, and an exhausted window without a reset time
// never blocks, because nothing would end the block. A credential whose only
// readings are credit balances is credit-only while any balance is left.
func (s *ExpiringFirstSelector) quotaStanding(credentialID, model string, now time.Time) (quotaStanding, time.Time) {
	if s == nil || s.readings == nil {
		return standingSubscription, time.Time{}
	}
	var blockedUntil time.Time
	hasSubscription, hasCredit, creditLeft := false, false, false
	for _, reading := range s.readings.Readings(credentialID, canonicalModelKey(model)) {
		if reading.Kind == quotareading.KindCredit {
			hasCredit = true
			creditLeft = creditLeft || !reading.Exhausted()
			continue
		}
		hasSubscription = true
		if reading.Exhausted() && reading.ResetAt.After(now) && reading.ResetAt.After(blockedUntil) {
			blockedUntil = reading.ResetAt
		}
	}
	subscriptionBlocked := !blockedUntil.IsZero()
	switch {
	case (subscriptionBlocked || !hasSubscription) && creditLeft:
		return standingCreditOnly, time.Time{}
	case subscriptionBlocked:
		return standingBlocked, blockedUntil
	case !hasSubscription && hasCredit:
		return standingBlocked, time.Time{}
	default:
		return standingSubscription, time.Time{}
	}
}

// creditOnlyMoveCandidates returns the credentials a bound thread should be
// picked again from when its bound credential can serve the model only from
// a credit balance: the bound credential's priority tier, when that tier has
// a usable credential with subscription quota. It returns nil when the
// binding should stay, so a thread never moves between credit-only
// credentials.
func (s *ExpiringFirstSelector) creditOnlyMoveCandidates(ctx context.Context, provider, model string, bound *Auth, available []*Auth) []*Auth {
	if s == nil || bound == nil {
		return nil
	}
	now := s.now()
	if standing, _ := s.quotaStanding(bound.ID, model, now); standing != standingCreditOnly {
		return nil
	}
	tierPriority := authPriority(bound)
	tier := make([]*Auth, 0, len(available))
	for _, candidate := range available {
		if authPriority(candidate) == tierPriority {
			tier = append(tier, candidate)
		}
	}
	usable, creditOnly, errUsable := s.usableByQuota(tier, provider, model, now)
	if errUsable != nil {
		return nil
	}
	for _, candidate := range preferCodexWebsocketAuths(ctx, provider, usable) {
		if !creditOnly[candidate.ID] {
			return tier
		}
	}
	return nil
}

// Pick-log reasons. The routing report parses these tokens.
const (
	expiringFirstReasonBindingKept = "binding_kept"
	expiringFirstReasonMoreUrgent  = "more_urgent"
	expiringFirstReasonNoData      = "no_data"
	expiringFirstReasonCreditOnly  = "credit_only"
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

// nextInTurn rotates through credentials without an urgency (no reading, or
// credit-only), resuming after the previous pick even when the candidate set
// changed in between.
func (s *ExpiringFirstSelector) nextInTurn(key string, candidates []*Auth) *Auth {
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

// urgency returns the credential's urgency in percent per hour for a request
// on model. It comes from the ranking window (the longest KindRanking
// reading) or, when a per-model window applies to model, from the more urgent
// of that window (the longest such reading) and the ranking window. A reading
// whose reset time has passed counts as a full window that resets one window
// length from now.
func (s *ExpiringFirstSelector) urgency(credentialID, model string, now time.Time) (float64, bool) {
	if s == nil || s.readings == nil {
		return 0, false
	}
	readings := s.readings.Readings(credentialID, canonicalModelKey(model))
	urgency, known := 0.0, false
	for _, kind := range []quotareading.Kind{quotareading.KindRanking, quotareading.KindPerModel} {
		windowUrgency, ok := readingUrgency(longestReading(readings, kind), now)
		if ok && (!known || windowUrgency > urgency) {
			urgency, known = windowUrgency, true
		}
	}
	return urgency, known
}

// longestReading returns the longest reading of kind that has a reset time,
// or nil.
func longestReading(readings []quotareading.Reading, kind quotareading.Kind) *quotareading.Reading {
	var longest *quotareading.Reading
	for i := range readings {
		reading := &readings[i]
		if reading.Kind != kind || reading.ResetAt.IsZero() {
			continue
		}
		if longest == nil || reading.Length > longest.Length {
			longest = reading
		}
	}
	return longest
}

// readingUrgency returns one window's urgency: percent left ÷ hours to reset.
func readingUrgency(reading *quotareading.Reading, now time.Time) (float64, bool) {
	if reading == nil {
		return 0, false
	}
	shareLeft, untilReset := reading.ShareLeft, reading.ResetAt.Sub(now)
	if untilReset <= 0 {
		if reading.Length <= 0 {
			return 0, false
		}
		shareLeft, untilReset = 1, reading.Length
	}
	return shareLeft * 100 / untilReset.Hours(), true
}

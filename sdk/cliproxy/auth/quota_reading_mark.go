package auth

import (
	"context"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotareading"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	log "github.com/sirupsen/logrus"
)

// exhaustedWindowsToMark returns the readings that should mark their credential
// quota-exceeded: exhausted quota windows whose reset time is still ahead.
// Credit balances never mark a credential, and a window without a reset time
// has nothing to mark until.
func exhaustedWindowsToMark(readings []quotareading.Reading, now time.Time) []quotareading.Reading {
	var out []quotareading.Reading
	for _, reading := range readings {
		if reading.Kind == quotareading.KindCredit || !reading.Exhausted() {
			continue
		}
		if reading.ResetAt.IsZero() || !reading.ResetAt.After(now) {
			continue
		}
		out = append(out, reading)
	}
	return out
}

// hasCreditLeft reports whether any reading is a credit balance with share left.
func hasCreditLeft(readings []quotareading.Reading) bool {
	for _, reading := range readings {
		if reading.Kind == quotareading.KindCredit && !reading.Exhausted() {
			return true
		}
	}
	return false
}

// markExhaustedQuotaWindows is the quota-reading module's observer. When the
// credential's readings show an exhausted window, it marks the credential (or,
// for a per-model window, the credential and each model the window covers)
// quota-exceeded until the window's reset time. The availability check that
// affinity and new picks share then skips the credential, so a bound thread
// moves before it hits a 429. The mark clears by itself at the reset time.
//
// A credential with a credit balance left is not marked, and a mark set
// earlier is lifted, so the expiring-first selector can still rank it
// credit-only (last) instead of the manager filtering it out.
func (m *Manager) markExhaustedQuotaWindows(credentialID string, accepted []quotareading.Reading) {
	if m == nil || len(accepted) == 0 {
		return
	}
	now := m.now()
	stored := m.quotaReadings.Snapshot()[credentialID]
	windows := exhaustedWindowsToMark(stored, now)
	if len(windows) == 0 {
		return
	}
	if hasCreditLeft(stored) {
		m.liftQuotaWindowMarks(credentialID, windows)
		return
	}
	registeredModels := modelsForRegisteredAuth(credentialID)
	var marked []quotareading.Reading
	snapshot := m.updateAuthQuotaMarks(credentialID, now, func(auth *Auth) bool {
		for _, window := range windows {
			resetAt := window.ResetAt.Round(0)
			changed := false
			if window.Kind == quotareading.KindPerModel {
				for _, modelKey := range modelKeysForWindow(auth, registeredModels, window) {
					if markModelQuotaWindowExhausted(auth, modelKey, resetAt, now) {
						changed = true
					}
				}
				if changed {
					updateAggregatedAvailability(auth, now)
				}
			} else {
				changed = markCredentialQuotaWindowExhausted(auth, resetAt)
			}
			if changed {
				marked = append(marked, window)
			}
		}
		return len(marked) > 0
	})
	if snapshot == nil {
		return
	}
	for _, window := range marked {
		model := window.Model
		if model == "" {
			model = "-"
		}
		log.Infof("quota window exhausted, credential marked quota-exceeded | credential=%s provider=%s window=%s model=%s reset_at=%s",
			credentialID, snapshot.Provider, window.Window, model, window.ResetAt.UTC().Format(time.RFC3339))
	}
}

// liftQuotaWindowMarks removes the marks that exhausted windows set earlier,
// because the credential now has a credit balance left. Only marks that end
// exactly at one of the windows' reset times are lifted, so a 429 cooldown
// stays in place.
func (m *Manager) liftQuotaWindowMarks(credentialID string, windows []quotareading.Reading) {
	now := m.now()
	snapshot := m.updateAuthQuotaMarks(credentialID, now, func(auth *Auth) bool {
		lifted := false
		for _, window := range windows {
			resetAt := window.ResetAt.Round(0)
			if auth.Quota.Exceeded && auth.Quota.Reason == "credential_quota" && auth.Quota.NextRecoverAt.Equal(resetAt) {
				auth.Unavailable = false
				auth.NextRetryAfter = time.Time{}
				applyCooldownFields(&auth.Quota, QuotaState{})
				lifted = true
			}
			for _, state := range auth.ModelStates {
				if state != nil && state.Quota.Exceeded && state.Quota.Reason == "quota" && state.Quota.NextRecoverAt.Equal(resetAt) {
					resetModelState(state, now)
					lifted = true
				}
			}
		}
		if lifted {
			updateAggregatedAvailability(auth, now)
		}
		return lifted
	})
	if snapshot != nil {
		log.Infof("quota window mark lifted, credit balance left | credential=%s provider=%s", credentialID, snapshot.Provider)
	}
}

// updateAuthQuotaMarks applies mutate to the credential under the manager
// lock and, when it reports a change, publishes the new state the way
// ResetQuota does: generation bump, persistence, cooldown state, registry
// projections and the scheduler. It returns the new snapshot, or nil when
// nothing changed or the credential is unknown.
func (m *Manager) updateAuthQuotaMarks(credentialID string, now time.Time, mutate func(auth *Auth) bool) *Auth {
	ctx := context.Background()
	m.mu.Lock()
	auth, ok := m.auths[credentialID]
	if !ok || auth == nil {
		m.mu.Unlock()
		return nil
	}
	var cooldownRecordsBefore []CooldownStateRecord
	trackCooldownState := m.cooldownStore != nil
	if trackCooldownState {
		cooldownRecordsBefore = m.cooldownStateRecordsForAuthLocked(auth, now)
	}
	if !mutate(auth) {
		m.mu.Unlock()
		return nil
	}
	auth.Generation++
	auth.UpdatedAt = now
	snapshot := auth.Clone()
	cooldownStateChanged := false
	if trackCooldownState {
		cooldownRecordsAfter := m.cooldownStateRecordsForAuthLocked(auth, now)
		cooldownStateChanged = !cooldownStateRecordsEqual(cooldownRecordsBefore, cooldownRecordsAfter)
	}
	errPersist := m.persist(ctx, auth)
	m.mu.Unlock()

	if errPersist != nil {
		log.Warnf("quota window mark: persist credential %s: %v", credentialID, errPersist)
	}
	if cooldownStateChanged {
		m.persistCooldownStates(ctx)
	}
	supportedModels, regEpoch := registry.GetGlobalRegistry().GetModelsAndEpochForClient(credentialID)
	projections := make([]registry.ClientModelProjection, 0, len(supportedModels))
	for _, supportedModel := range supportedModels {
		if supportedModel == nil || strings.TrimSpace(supportedModel.ID) == "" {
			continue
		}
		projections = append(projections, m.clientModelProjectionForAuth(snapshot, supportedModel.ID, now))
	}
	registry.GetGlobalRegistry().ApplyClientModelProjections(credentialID, regEpoch, snapshot.Generation, projections)
	if m.scheduler != nil {
		m.scheduler.upsertAuth(snapshot)
	}
	return snapshot
}

// markCredentialQuotaWindowExhausted blocks the whole credential until resetAt,
// the same way a credential-wide 429 does. It never shortens a later mark.
func markCredentialQuotaWindowExhausted(auth *Auth, resetAt time.Time) bool {
	if auth.Quota.Exceeded && auth.Quota.Reason == "credential_quota" && !auth.Quota.NextRecoverAt.Before(resetAt) {
		return false
	}
	auth.Unavailable = true
	applyCooldownFields(&auth.Quota, QuotaState{
		Exceeded:      true,
		Reason:        "credential_quota",
		NextRecoverAt: resetAt,
		BackoffLevel:  auth.Quota.BackoffLevel,
	})
	if auth.NextRetryAfter.Before(resetAt) {
		auth.NextRetryAfter = resetAt
	}
	return true
}

// markModelQuotaWindowExhausted blocks one model of the credential until
// resetAt, the same way a model 429 does. It never shortens a later mark.
func markModelQuotaWindowExhausted(auth *Auth, modelKey string, resetAt, now time.Time) bool {
	state := ensureModelState(auth, modelKey)
	if state == nil {
		return false
	}
	if state.Unavailable && state.Quota.Exceeded && !state.Quota.NextRecoverAt.Before(resetAt) && !state.NextRetryAfter.Before(resetAt) {
		return false
	}
	state.Unavailable = true
	state.Status = StatusError
	state.UpdatedAt = now
	if state.NextRetryAfter.Before(resetAt) {
		state.NextRetryAfter = resetAt
	}
	nextRecoverAt := resetAt
	if state.Quota.Exceeded && state.Quota.NextRecoverAt.After(nextRecoverAt) {
		nextRecoverAt = state.Quota.NextRecoverAt
	}
	applyCooldownFields(&state.Quota, QuotaState{
		Exceeded:      true,
		Reason:        "quota",
		NextRecoverAt: nextRecoverAt,
		BackoffLevel:  state.Quota.BackoffLevel,
	})
	return true
}

// modelKeysForWindow lists the credential's models that a per-model window
// covers: its registered models and the models it already has state for.
func modelKeysForWindow(auth *Auth, registeredModels []string, window quotareading.Reading) []string {
	keys := make([]string, 0, len(registeredModels)+len(auth.ModelStates))
	for _, modelKey := range registeredModels {
		keys = append(keys, canonicalModelKey(modelKey))
	}
	for modelKey := range auth.ModelStates {
		keys = append(keys, canonicalModelKey(modelKey))
	}
	keys = dedupeStrings(keys)
	out := keys[:0]
	for _, modelKey := range keys {
		if modelKey != "" && window.AppliesToModel(modelKey) {
			out = append(out, modelKey)
		}
	}
	return out
}

// quotaWindowExhaustedForModel reports whether a stored reading shows an
// exhausted window that applies to model and has not reset yet. Binding-end
// logs use it to name the reason a bound credential became unusable.
func (m *Manager) quotaWindowExhaustedForModel(credentialID, model string, now time.Time) bool {
	if m == nil {
		return false
	}
	return len(exhaustedWindowsToMark(m.quotaReadings.Readings(credentialID, model), now)) > 0
}

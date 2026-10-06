package auth

import (
	"context"
	"net/http"
	"strings"
)

// Binding-end reasons. The routing report parses them from the binding-end
// log line, so keep the tokens stable.
const (
	bindingEndQuotaWindowExhausted = "quota_window_exhausted"
	bindingEndCooldown             = "cooldown"
	bindingEndQuotaExceeded429     = "quota_exceeded_429"
	bindingEndUnauthorized         = "unauthorized"
	bindingEndForbidden            = "forbidden"
	bindingEndDisabled             = "disabled"
	bindingEndUnavailable          = "unavailable"
	// bindingEndSubscriptionExhausted: the bound credential can serve only
	// from its credit balance, and its priority tier still has a credential
	// with subscription quota.
	bindingEndSubscriptionExhausted = "subscription_exhausted"
)

// bindingEndReasonFunc tells the affinity selector which provider a bound
// credential belongs to and why it can no longer serve the route model.
type bindingEndReasonFunc func(authID, routeModel string) (provider, reason string)

type bindingEndReasonContextKey struct{}

// withBindingEndReasons lets the affinity selector ask the manager why a bound
// credential is unusable when it moves a thread.
func (m *Manager) withBindingEndReasons(ctx context.Context) context.Context {
	if m == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, bindingEndReasonContextKey{}, bindingEndReasonFunc(m.bindingEndReason))
}

func bindingEndReasonFromContext(ctx context.Context, authID, routeModel string) (provider, reason string) {
	if ctx != nil {
		if lookup, ok := ctx.Value(bindingEndReasonContextKey{}).(bindingEndReasonFunc); ok && lookup != nil {
			return lookup(authID, routeModel)
		}
	}
	return "", bindingEndUnavailable
}

// bindingEndReason names why the credential cannot serve routeModel now.
func (m *Manager) bindingEndReason(authID, routeModel string) (string, string) {
	now := m.now()
	m.mu.RLock()
	defer m.mu.RUnlock()
	auth := m.auths[authID]
	if auth == nil {
		return "", bindingEndUnavailable
	}
	provider := auth.Provider
	if auth.Disabled || auth.Status == StatusDisabled {
		return provider, bindingEndDisabled
	}
	if hasUnauthorizedAuthFailure(auth) {
		return provider, bindingEndUnauthorized
	}
	model := m.selectionModelForAuth(auth, routeModel)
	if m.quotaWindowExhaustedForModel(authID, model, now) {
		return provider, bindingEndQuotaWindowExhausted
	}
	blocked, reason, _ := isAuthBlockedForModel(auth, model, now)
	if !blocked {
		return provider, bindingEndUnavailable
	}
	if reason == blockReasonDisabled {
		return provider, bindingEndDisabled
	}
	if reason == blockReasonCooldown {
		return provider, bindingEndQuotaExceeded429
	}
	lastErr := auth.LastError
	if state := existingModelState(auth, model); state != nil && state.LastError != nil {
		lastErr = state.LastError
	}
	if lastErr != nil {
		return provider, bindingEndReasonForStatus(lastErr.StatusCode())
	}
	return provider, bindingEndCooldown
}

// bindingEndReasonForStatus names the binding-end reason for a credential
// failure with an HTTP status.
func bindingEndReasonForStatus(status int) string {
	switch status {
	case http.StatusTooManyRequests:
		return bindingEndQuotaExceeded429
	case http.StatusUnauthorized:
		return bindingEndUnauthorized
	case http.StatusForbidden:
		return bindingEndForbidden
	default:
		return bindingEndCooldown
	}
}

// logBindingEnded writes the binding-end line the routing report counts moves
// from. Thread and credential are identifiers, never secrets.
func logBindingEnded(ctx context.Context, thread, authID, provider, model, reason string) {
	if strings.TrimSpace(provider) == "" {
		provider = "-"
	}
	selectorLogEntry(ctx).Infof("affinity binding ended | thread=%s credential=%s provider=%s model=%s reason=%s",
		thread, authID, provider, model, reason)
}

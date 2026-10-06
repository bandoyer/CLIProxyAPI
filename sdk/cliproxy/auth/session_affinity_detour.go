package auth

import "time"

// affinityDetour records that a binding's credential failed with an upstream
// overload (5xx or 529). Until the mark expires, requests in the thread may be
// served by another credential without rewriting the binding, so the thread
// returns to its warm credential once that credential is usable again.
type affinityDetour struct {
	authID string
	until  time.Time
}

// isUpstreamOverloadResult reports a 5xx or 529 result that should detour one
// request but keep the binding. Forced cooldowns still end the binding.
func isUpstreamOverloadResult(err *Error) bool {
	if err == nil || err.Code == ErrorCodeForceCooldown {
		return false
	}
	status := err.StatusCode()
	return status >= 500 && status <= 599
}

// upstreamOverloadDetourWindow covers the transient cooldown that the manager
// applies after an upstream overload. It is never shorter than the default
// transient cooldown, so the failed request's own retry is always covered.
func upstreamOverloadDetourWindow(now time.Time, retryAfter *time.Duration) time.Duration {
	window := recoverableFailureRetryAfterWithHint(now, retryAfter, false).Sub(now)
	if window < transientErrorCooldown {
		window = transientErrorCooldown
	}
	return window
}

// markDetour keeps key bound to authID while authID recovers from an upstream
// overload. It only marks keys that are still bound to authID.
func (s *SessionAffinitySelector) markDetour(key, authID string, until time.Time) {
	if s == nil || s.cache == nil || key == "" {
		return
	}
	if bound, ok := s.cache.Get(key); !ok || bound != authID {
		return
	}
	now := s.now()
	s.detourMu.Lock()
	defer s.detourMu.Unlock()
	if s.detours == nil {
		s.detours = make(map[string]affinityDetour)
	}
	for k, d := range s.detours {
		if now.After(d.until) {
			delete(s.detours, k)
		}
	}
	s.detours[key] = affinityDetour{authID: authID, until: until}
}

// detourActive reports whether key's binding to authID is in an unexpired detour.
func (s *SessionAffinitySelector) detourActive(key, authID string) bool {
	if s == nil || key == "" {
		return false
	}
	s.detourMu.Lock()
	defer s.detourMu.Unlock()
	d, ok := s.detours[key]
	if !ok {
		return false
	}
	if d.authID != authID || s.now().After(d.until) {
		delete(s.detours, key)
		return false
	}
	return true
}

// clearDetour ends any detour on key.
func (s *SessionAffinitySelector) clearDetour(key string) {
	if s == nil || key == "" {
		return
	}
	s.detourMu.Lock()
	defer s.detourMu.Unlock()
	delete(s.detours, key)
}

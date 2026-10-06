package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const detourTestModel = "affinity-detour-model"

// detourUpstream is a fake upstream that serves every credential, except that
// it fails the next request on one chosen credential with a chosen error.
type detourUpstream struct {
	mu      sync.Mutex
	failID  string
	failErr error
}

func (u *detourUpstream) failNext(authID string, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.failID = authID
	u.failErr = err
}

func (u *detourUpstream) execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.failErr != nil && auth.ID == u.failID {
		err := u.failErr
		u.failID, u.failErr = "", nil
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

// detourHarness is an auth manager whose session-affinity selector wraps
// round-robin and reads an injected clock, with two codex credentials served
// by a fake upstream.
type detourHarness struct {
	*Manager
	clock    *affinityTestClock
	upstream *detourUpstream
	ids      []string
}

// newDetourHarness builds the harness. When disableCooling is true the
// credentials never cool down, so a failed credential is usable again on the
// thread's next request and only the binding decides where the thread goes.
func newDetourHarness(t *testing.T, disableCooling bool) *detourHarness {
	t.Helper()
	ctx := context.Background()
	clock := newAffinityTestClock()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
		NowFunc:  clock.Now,
	})
	t.Cleanup(selector.Stop)
	manager := NewManager(nil, selector, nil)
	manager.SetRetryConfig(0, 0, 0)
	ids := []string{"a-" + t.Name(), "b-" + t.Name()}
	for _, id := range ids {
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: detourTestModel}})
		authID := id
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
		auth := &Auth{ID: id, Provider: "codex", Status: StatusActive}
		if disableCooling {
			auth.Metadata = map[string]any{"disable_cooling": true}
		}
		if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	upstream := &detourUpstream{}
	manager.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex", executeFn: upstream.execute})
	return &detourHarness{Manager: manager, clock: clock, upstream: upstream, ids: ids}
}

// serve sends one request in the thread and returns the credential that
// served it, or the error the caller saw.
func (h *detourHarness) serve(thread string) (string, error) {
	opts := cliproxyexecutor.Options{
		Headers:  http.Header{"X-Session-Id": []string{thread}},
		Metadata: map[string]any{},
	}
	response, errExecute := h.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: detourTestModel}, opts)
	if errExecute != nil {
		return "", errExecute
	}
	return string(response.Payload), nil
}

// bound returns the credential the thread is bound to, or "" when unbound.
func (h *detourHarness) bound(thread string) string {
	auth, status := h.LookupSessionAffinity("codex", detourTestModel, thread)
	if status != "bound" || auth == nil {
		return ""
	}
	return auth.ID
}

// startThread serves the thread's first request and returns the warm
// credential it bound to and the other credential.
func (h *detourHarness) startThread(t *testing.T, thread string) (warm, other string) {
	t.Helper()
	warm, errFirst := h.serve(thread)
	if errFirst != nil {
		t.Fatalf("first request: %v", errFirst)
	}
	if h.ids[0] == warm {
		return warm, h.ids[1]
	}
	return warm, h.ids[0]
}

func TestSessionAffinityBindingKeptThroughUpstreamOverloadDetour(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{name: "500", status: http.StatusInternalServerError},
		{name: "502", status: http.StatusBadGateway},
		{name: "503", status: http.StatusServiceUnavailable},
		{name: "529", status: 529},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newDetourHarness(t, true)
			thread := "thread-detour-" + tc.name
			warm, detour := h.startThread(t, thread)

			h.upstream.failNext(warm, &Error{HTTPStatus: tc.status, Message: "upstream overloaded"})
			if served, errDetour := h.serve(thread); errDetour != nil || served != detour {
				t.Fatalf("request after %d on the bound credential: served by %q (err %v), want detour to %s", tc.status, served, errDetour, detour)
			}
			if got := h.bound(thread); got != warm {
				t.Fatalf("binding after detour = %q, want it kept on the warm credential %s", got, warm)
			}
			if served, errNext := h.serve(thread); errNext != nil || served != warm {
				t.Fatalf("next request: served by %q (err %v), want back on the warm credential %s", served, errNext, warm)
			}
		})
	}
}

func TestSessionAffinityDetourDuringOverloadCooldownKeepsBinding(t *testing.T) {
	h := newDetourHarness(t, false)
	thread := "thread-detour-cooldown"
	warm, detour := h.startThread(t, thread)

	h.upstream.failNext(warm, &Error{HTTPStatus: 529, Message: "overloaded_error"})
	if served, errDetour := h.serve(thread); errDetour != nil || served != detour {
		t.Fatalf("request after 529: served by %q (err %v), want detour to %s", served, errDetour, detour)
	}

	// The warm credential is in its short transient cooldown, so later turns
	// detour too, but none of them rewrites the binding.
	for turn := 0; turn < 3; turn++ {
		h.clock.Advance(10 * time.Second)
		if served, errTurn := h.serve(thread); errTurn != nil || served != detour {
			t.Fatalf("turn %d during cooldown: served by %q (err %v), want detour to %s", turn, served, errTurn, detour)
		}
		if got := h.bound(thread); got != warm {
			t.Fatalf("turn %d during cooldown: binding = %q, want it kept on %s", turn, got, warm)
		}
	}

	// The manager's cooldown runs on wall time, so end it explicitly.
	if _, _, errReset := h.ResetQuota(context.Background(), warm); errReset != nil {
		t.Fatalf("end cooldown: %v", errReset)
	}
	if served, errBack := h.serve(thread); errBack != nil || served != warm {
		t.Fatalf("after cooldown: served by %q (err %v), want back on the warm credential %s", served, errBack, warm)
	}
}

func TestSessionAffinityQuotaCooldownDuringDetourEndsBinding(t *testing.T) {
	h := newDetourHarness(t, false)
	thread := "thread-detour-then-quota"
	warm, detour := h.startThread(t, thread)

	h.upstream.failNext(warm, &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream unavailable"})
	if served, errDetour := h.serve(thread); errDetour != nil || served != detour {
		t.Fatalf("request after 503: served by %q (err %v), want detour to %s", served, errDetour, detour)
	}

	// Another thread then exhausts the warm credential's quota.
	h.MarkResult(context.Background(), Result{
		AuthID:   warm,
		Provider: "codex",
		Model:    detourTestModel,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	// Once the overload detour has run its course, a credential that is still
	// unusable ends the binding.
	h.clock.Advance(2 * time.Minute)
	if served, errMove := h.serve(thread); errMove != nil || served != detour {
		t.Fatalf("after quota cooldown: served by %q (err %v), want %s", served, errMove, detour)
	}
	if got := h.bound(thread); got != detour {
		t.Fatalf("binding after quota cooldown = %q, want moved to %s", got, detour)
	}
}

func TestSessionAffinityCredentialFailureEndsBinding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{name: "429", status: http.StatusTooManyRequests},
		{name: "401", status: http.StatusUnauthorized},
		{name: "403", status: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Cooling is off, so the failed credential stays usable: only the
			// released binding can move the thread.
			h := newDetourHarness(t, true)
			thread := "thread-credential-failure-" + tc.name
			warm, moved := h.startThread(t, thread)

			h.upstream.failNext(warm, &Error{HTTPStatus: tc.status, Message: "credential failure"})
			if served, errMove := h.serve(thread); errMove != nil || served != moved {
				t.Fatalf("request after %d: served by %q (err %v), want %s", tc.status, served, errMove, moved)
			}
			if got := h.bound(thread); got != moved {
				t.Fatalf("binding after %d = %q, want moved to %s", tc.status, got, moved)
			}
			if served, errNext := h.serve(thread); errNext != nil || served != moved {
				t.Fatalf("next request: served by %q (err %v), want %s", served, errNext, moved)
			}
		})
	}
}

func TestSessionAffinityRequestScopedErrorKeepsBinding(t *testing.T) {
	h := newDetourHarness(t, false)
	thread := "thread-request-scoped"
	warm, _ := h.startThread(t, thread)

	h.upstream.failNext(warm, NewRequestScopedError("invalid request", http.StatusBadRequest))
	if _, errBad := h.serve(thread); errBad == nil {
		t.Fatalf("request with a request-scoped error: want the error returned to the caller")
	}
	if got := h.bound(thread); got != warm {
		t.Fatalf("binding after request-scoped error = %q, want kept on %s", got, warm)
	}
	if served, errNext := h.serve(thread); errNext != nil || served != warm {
		t.Fatalf("next request: served by %q (err %v), want %s", served, errNext, warm)
	}
}

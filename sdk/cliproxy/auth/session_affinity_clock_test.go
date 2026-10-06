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

// affinityTestClock is a controllable clock for session affinity tests.
// Pass its Now method as SessionAffinityConfig.NowFunc and call Advance to
// move time forward without wall-clock sleeps.
type affinityTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func newAffinityTestClock() *affinityTestClock {
	return &affinityTestClock{now: time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *affinityTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *affinityTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newAffinityClockManager builds a manager whose session-affinity selector reads
// the given clock and wraps round-robin, with two usable codex credentials.
func newAffinityClockManager(t *testing.T, clock *affinityTestClock, ttl time.Duration) *Manager {
	t.Helper()
	ctx := context.Background()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      ttl,
		NowFunc:  clock.Now,
	})
	t.Cleanup(selector.Stop)
	manager := NewManager(nil, selector, nil)
	manager.SetRetryConfig(0, 0, 0)
	for _, id := range []string{"a-" + t.Name(), "b-" + t.Name()} {
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "affinity-clock-model"}})
		authID := id
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
		if _, errRegister := manager.Register(ctx, &Auth{ID: id, Provider: "codex", Status: StatusActive}); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	manager.RegisterExecutor(&mockCustomErrorExecutor{
		identifier: "codex",
		executeFn: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
		},
	})
	return manager
}

func executeInThread(t *testing.T, manager *Manager, thread string) string {
	t.Helper()
	opts := cliproxyexecutor.Options{
		Headers:  http.Header{"X-Session-Id": []string{thread}},
		Metadata: map[string]any{},
	}
	response, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "affinity-clock-model"}, opts)
	if errExecute != nil {
		t.Fatalf("execute: %v", errExecute)
	}
	return string(response.Payload)
}

func TestSessionAffinityBindingExpiresAfterIdleLifetimeOnInjectedClock(t *testing.T) {
	clock := newAffinityTestClock()
	manager := newAffinityClockManager(t, clock, time.Hour)

	first := executeInThread(t, manager, "thread-expiry")
	clock.Advance(59 * time.Minute)
	if got := executeInThread(t, manager, "thread-expiry"); got != first {
		t.Fatalf("within binding lifetime: credential = %s, want binding kept on %s", got, first)
	}

	clock.Advance(time.Hour + time.Second)
	if got := executeInThread(t, manager, "thread-expiry"); got == first {
		t.Fatalf("after binding lifetime: credential = %s, want a new pick (round-robin moves to the other credential)", got)
	}
}

func TestSessionAffinityBindingLifetimeSlidesOnInjectedClock(t *testing.T) {
	clock := newAffinityTestClock()
	manager := newAffinityClockManager(t, clock, time.Hour)

	first := executeInThread(t, manager, "thread-sliding")
	// Each request inside the lifetime refreshes it, so the thread stays bound
	// long after the first binding would have expired.
	for step := 0; step < 4; step++ {
		clock.Advance(45 * time.Minute)
		if got := executeInThread(t, manager, "thread-sliding"); got != first {
			t.Fatalf("step %d: credential = %s, want binding kept on %s", step, got, first)
		}
	}
}

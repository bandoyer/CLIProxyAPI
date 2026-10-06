package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// newExpiringFirstProviderManager builds affinity(expiring-first) on the
// manager's quota readings, with one credential per ID for provider and the
// given models. The executor answers with the credential ID.
func newExpiringFirstProviderManager(t *testing.T, clock *affinityTestClock, provider string, models []string, ids ...string) *Manager {
	t.Helper()
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: NewExpiringFirstSelector(ExpiringFirstConfig{Readings: manager.QuotaReadings(), NowFunc: clock.Now}),
		TTL:      time.Hour,
		NowFunc:  clock.Now,
	})
	t.Cleanup(selector.Stop)
	manager.SetSelector(selector)
	manager.SetRetryConfig(0, 0, 0)
	modelInfos := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		modelInfos = append(modelInfos, &registry.ModelInfo{ID: model})
	}
	for _, id := range ids {
		authID := id
		registry.GetGlobalRegistry().RegisterClient(authID, provider, modelInfos)
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
		if _, errRegister := manager.Register(WithSkipPersist(ctx), &Auth{ID: authID, Provider: provider, Status: StatusActive}); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	manager.RegisterExecutor(&mockCustomErrorExecutor{
		identifier: provider,
		executeFn: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
		},
	})
	return manager
}

func executeThread(t *testing.T, manager *Manager, provider, model, thread string) string {
	t.Helper()
	opts := cliproxyexecutor.Options{
		Headers:  http.Header{"X-Session-Id": []string{thread}},
		Metadata: map[string]any{},
	}
	response, errExecute := manager.Execute(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, opts)
	if errExecute != nil {
		t.Fatalf("execute %s on %s: %v", thread, model, errExecute)
	}
	return string(response.Payload)
}

// devinRefreshExecutor answers refreshes with the quota signals Devin's
// refresh stores for each credential.
type devinRefreshExecutor struct {
	mockCustomErrorExecutor
	signals    map[string]map[string]string
	observedAt time.Time
}

func (e *devinRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	updated := auth.Clone()
	updated.Quota.Signals = e.signals[auth.ID]
	updated.Quota.ObservedAt = e.observedAt
	return updated, nil
}

func TestExpiringFirstDevinRefreshDataSteersTheNextNewThread(t *testing.T) {
	const model = "devin-test-model"
	clock := newAffinityTestClock()
	lessUrgent, moreUrgent := "ef-devin-1-"+t.Name(), "ef-devin-2-"+t.Name()
	manager := newExpiringFirstProviderManager(t, clock, "devin", []string{model}, lessUrgent, moreUrgent)
	daily := clock.Now().Add(6 * time.Hour).Format(time.RFC3339)
	manager.RegisterExecutor(&devinRefreshExecutor{
		mockCustomErrorExecutor: mockCustomErrorExecutor{
			identifier: "devin",
			executeFn: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
			},
		},
		observedAt: clock.Now(),
		signals: map[string]map[string]string{
			lessUrgent: { // 90% of the week left, 4 days to its reset
				"daily_quota_remaining_percent":  "100%",
				"daily_quota_reset_at":           daily,
				"weekly_quota_remaining_percent": "90%",
				"weekly_quota_reset_at":          clock.Now().Add(4 * 24 * time.Hour).Format(time.RFC3339),
			},
			moreUrgent: { // 40% of the week left, 3 hours to its reset
				"daily_quota_remaining_percent":  "100%",
				"daily_quota_reset_at":           daily,
				"weekly_quota_remaining_percent": "40%",
				"weekly_quota_reset_at":          clock.Now().Add(3 * time.Hour).Format(time.RFC3339),
			},
		},
	})

	for _, credential := range []string{lessUrgent, moreUrgent} {
		if _, errRefresh := manager.ForceRefreshAuth(context.Background(), credential); errRefresh != nil {
			t.Fatalf("refresh %s: %v", credential, errRefresh)
		}
	}
	if readings := manager.QuotaReadings().Readings(moreUrgent, model); len(readings) != 2 {
		t.Fatalf("readings learned from the refresh = %+v, want the daily and weekly windows", readings)
	}

	for _, thread := range []string{"thread-1", "thread-2"} {
		if got := executeThread(t, manager, "devin", model, thread); got != moreUrgent {
			t.Fatalf("%s: credential = %s, want %s, more urgent by its refresh data", thread, got, moreUrgent)
		}
	}
}

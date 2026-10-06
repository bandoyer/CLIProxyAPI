package auth

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotareading"
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

// recordWindow gives a credential one reading that resets resetIn from the
// clock's now.
func recordWindow(manager *Manager, clock *affinityTestClock, credentialID string, reading quotareading.Reading, resetIn time.Duration) {
	reading.ResetAt = clock.Now().Add(resetIn)
	reading.LearnedAt = clock.Now()
	reading.Source = quotareading.SourcePoll
	manager.QuotaReadings().Record(credentialID, reading)
}

func sevenDayOpus(shareLeft float64) quotareading.Reading {
	return quotareading.Reading{Window: "seven_day_opus", Kind: quotareading.KindPerModel, Model: "claude-opus", Length: 7 * 24 * time.Hour, ShareLeft: shareLeft}
}

func sevenDay(shareLeft float64) quotareading.Reading {
	return quotareading.Reading{Window: quotareading.ClaudeSevenDayWindow, Kind: quotareading.KindRanking, Length: 7 * 24 * time.Hour, ShareLeft: shareLeft}
}

func TestExpiringFirstPerModelWindowRanksOnlyRequestsForItsModel(t *testing.T) {
	const opus, sonnet = "claude-opus-4-5-20251101", "claude-sonnet-4-5-20250929"
	clock := newAffinityTestClock()
	opusUrgent, mainUrgent := "ef-pm-1-"+t.Name(), "ef-pm-2-"+t.Name()
	manager := newExpiringFirstProviderManager(t, clock, "claude", []string{opus, sonnet}, opusUrgent, mainUrgent)
	// opusUrgent: 90% of the week left in 4 days (0.94%/h), but 80% of its
	// Opus week left in 2 hours (40%/h).
	recordWindow(manager, clock, opusUrgent, sevenDay(0.90), 4*24*time.Hour)
	recordWindow(manager, clock, opusUrgent, sevenDayOpus(0.80), 2*time.Hour)
	// mainUrgent: 40% of the week left in 3 hours (13.33%/h), no Opus window.
	recordWindow(manager, clock, mainUrgent, sevenDay(0.40), 3*time.Hour)

	if got := executeThread(t, manager, "claude", opus, "opus-thread"); got != opusUrgent {
		t.Fatalf("Opus request: credential = %s, want %s, more urgent by its Opus window", got, opusUrgent)
	}
	if got := executeThread(t, manager, "claude", sonnet, "sonnet-thread"); got != mainUrgent {
		t.Fatalf("Sonnet request: credential = %s, want %s; the Opus window must not count for Sonnet", got, mainUrgent)
	}
}

func TestExpiringFirstPerModelWindowNeverLowersTheLongestWindowsUrgency(t *testing.T) {
	const opus = "claude-opus-4-5-20251101"
	clock := newAffinityTestClock()
	other, urgent := "ef-pm-1-"+t.Name(), "ef-pm-2-"+t.Name()
	manager := newExpiringFirstProviderManager(t, clock, "claude", []string{opus}, other, urgent)
	// urgent: 40% of the week left in 3 hours (13.33%/h); its Opus window is
	// nearly idle (90% left in 4 days, 0.94%/h). The more urgent one counts.
	recordWindow(manager, clock, urgent, sevenDay(0.40), 3*time.Hour)
	recordWindow(manager, clock, urgent, sevenDayOpus(0.90), 4*24*time.Hour)
	// other: 80% of the week left in 10 hours (8%/h).
	recordWindow(manager, clock, other, sevenDay(0.80), 10*time.Hour)

	hook := captureInfoLogs(t)
	if got := executeThread(t, manager, "claude", opus, "opus-thread"); got != urgent {
		t.Fatalf("Opus request: credential = %s, want %s, ranked by its more urgent longest window", got, urgent)
	}
	lines := expiringFirstPickLines(hook)
	want := "expiring-first pick | credential=" + urgent + " urgency=13.33%/h reason=more_urgent"
	if len(lines) != 1 || !strings.HasPrefix(lines[0], want) {
		t.Fatalf("pick log lines = %q, want one line with prefix %q", lines, want)
	}
}

func TestExpiringFirstPerModelWindowAloneGivesTheModelAnUrgency(t *testing.T) {
	const opus = "claude-opus-4-5-20251101"
	clock := newAffinityTestClock()
	noData, opusOnly := "ef-pm-1-"+t.Name(), "ef-pm-2-"+t.Name()
	manager := newExpiringFirstProviderManager(t, clock, "claude", []string{opus}, noData, opusOnly)
	// Polling returned only the Opus window for this credential.
	recordWindow(manager, clock, opusOnly, sevenDayOpus(0.99), 6*24*time.Hour)

	for _, thread := range []string{"thread-1", "thread-2"} {
		if got := executeThread(t, manager, "claude", opus, thread); got != opusOnly {
			t.Fatalf("%s: credential = %s, want %s, which has a known urgency for Opus", thread, got, opusOnly)
		}
	}
}

// codexRateLimitHeaders is a Codex response with one weekly main window and
// the GPT-5.3-Codex-Spark weekly limit, in the header names the HTTP response
// uses and the WebSocket codex.rate_limits event is stored under.
func codexRateLimitHeaders(mainUsed float64, mainReset time.Time, sparkUsed float64, sparkReset time.Time) http.Header {
	return http.Header{
		"X-Codex-Plan-Type":                              []string{"pro"},
		"X-Codex-Primary-Used-Percent":                   []string{strconv.FormatFloat(mainUsed, 'f', -1, 64)},
		"X-Codex-Primary-Window-Minutes":                 []string{"10080"},
		"X-Codex-Primary-Reset-At":                       []string{strconv.FormatInt(mainReset.Unix(), 10)},
		"X-Codex-Bengalfox-Limit-Name":                   []string{"GPT-5.3-Codex-Spark"},
		"X-Codex-Bengalfox-Primary-Used-Percent":         []string{strconv.FormatFloat(sparkUsed, 'f', -1, 64)},
		"X-Codex-Bengalfox-Primary-Window-Minutes":       []string{"10080"},
		"X-Codex-Bengalfox-Primary-Reset-At":             []string{strconv.FormatInt(sparkReset.Unix(), 10)},
		"X-Codex-Credits-Has-Credits":                    []string{"False"},
		"X-Codex-Bengalfox-Over-Secondary-Limit-Percent": []string{"0"},
	}
}

func TestExpiringFirstCodexResponseHeadersSteerNewThreadsPerModel(t *testing.T) {
	const spark, gpt = "gpt-5.3-codex-spark", "gpt-5.4"
	clock := newAffinityTestClock()
	// The first expected winner sorts last, so a round-robin pick fails.
	sparkUrgent, mainUrgent := "ef-codex-2-"+t.Name(), "ef-codex-1-"+t.Name()
	manager := newExpiringFirstProviderManager(t, clock, "codex", []string{spark, gpt}, sparkUrgent, mainUrgent)
	// MarkResult learns readings on the wall clock; align the selector's clock.
	wallNow := time.Now()
	clock.Advance(wallNow.Sub(clock.Now()))
	headers := map[string]http.Header{
		// 90% of the week left in 4 days, 80% of the Spark week left in 2 hours.
		sparkUrgent: codexRateLimitHeaders(10, wallNow.Add(4*24*time.Hour), 20, wallNow.Add(2*time.Hour)),
		// 40% of the week left in 3 hours, Spark nearly idle.
		mainUrgent: codexRateLimitHeaders(60, wallNow.Add(3*time.Hour), 1, wallNow.Add(6*24*time.Hour)),
	}
	manager.RegisterExecutor(&mockCustomErrorExecutor{
		identifier: "codex",
		executeFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			internallogging.SetResponseHeaders(ctx, headers[auth.ID])
			return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
		},
	})
	// One pinned request per credential stands in for earlier traffic.
	for _, credential := range []string{sparkUrgent, mainUrgent} {
		opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: credential}}
		if _, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: gpt}, opts); errExecute != nil {
			t.Fatalf("execute pinned to %s: %v", credential, errExecute)
		}
	}
	if readings := manager.QuotaReadings().Readings(sparkUrgent, spark); len(readings) != 3 {
		t.Fatalf("readings for a Spark request = %+v, want the main window, the Spark window and credits", readings)
	}
	if readings := manager.QuotaReadings().Readings(sparkUrgent, gpt); len(readings) != 2 {
		t.Fatalf("readings for a GPT-5.4 request = %+v, want the main window and credits only", readings)
	}

	if got := executeThread(t, manager, "codex", spark, "spark-thread"); got != sparkUrgent {
		t.Fatalf("Spark request: credential = %s, want %s, more urgent by its Spark limit", got, sparkUrgent)
	}
	if got := executeThread(t, manager, "codex", gpt, "gpt-thread"); got != mainUrgent {
		t.Fatalf("GPT-5.4 request: credential = %s, want %s; the Spark limit must not count for other models", got, mainUrgent)
	}
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

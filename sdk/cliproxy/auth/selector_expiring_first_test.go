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
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

const expiringFirstTestModel = "expiring-first-model"

// newExpiringFirstManager builds a manager the way config does for
// routing.strategy: expiring-first with routing.session-affinity: true, with
// one Claude credential per ID. The executor answers with the credential ID.
func newExpiringFirstManager(t *testing.T, clock *affinityTestClock, ids ...string) *Manager {
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
	for _, id := range ids {
		authID := id
		registry.GetGlobalRegistry().RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: expiringFirstTestModel}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
		if _, errRegister := manager.Register(WithSkipPersist(ctx), &Auth{ID: authID, Provider: "claude", Status: StatusActive}); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	manager.RegisterExecutor(&mockCustomErrorExecutor{
		identifier: "claude",
		executeFn: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
		},
	})
	return manager
}

// recordSevenDay gives a credential a Claude 7-day (ranking) reading.
func recordSevenDay(manager *Manager, clock *affinityTestClock, credentialID string, shareLeft float64, resetIn time.Duration) {
	manager.QuotaReadings().Record(credentialID, quotareading.Reading{
		Window:    quotareading.ClaudeSevenDayWindow,
		Kind:      quotareading.KindRanking,
		Length:    7 * 24 * time.Hour,
		ShareLeft: shareLeft,
		ResetAt:   clock.Now().Add(resetIn),
		LearnedAt: clock.Now(),
		Source:    quotareading.SourceHeader,
	})
}

func executeClaudeThread(t *testing.T, manager *Manager, thread string) string {
	t.Helper()
	opts := cliproxyexecutor.Options{
		Headers:  http.Header{"X-Session-Id": []string{thread}},
		Metadata: map[string]any{},
	}
	response, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: expiringFirstTestModel}, opts)
	if errExecute != nil {
		t.Fatalf("execute: %v", errExecute)
	}
	return string(response.Payload)
}

func TestExpiringFirstNewThreadGoesToMostUrgentCredential(t *testing.T) {
	// The examples from #8. The expected winner always sorts after the loser,
	// so an ID-ordered or round-robin first pick cannot pass by accident.
	tests := []struct {
		name          string
		winnerShare   float64
		winnerResetIn time.Duration
		loserShare    float64
		loserResetIn  time.Duration
	}{
		{
			name:        "A with 40% left in 3h beats B with 90% left in 4 days",
			winnerShare: 0.40, winnerResetIn: 3 * time.Hour,
			loserShare: 0.90, loserResetIn: 4 * 24 * time.Hour,
		},
		{
			name:        "D with 80% left in 2h beats C with 1% left in 10 minutes",
			winnerShare: 0.80, winnerResetIn: 2 * time.Hour,
			loserShare: 0.01, loserResetIn: 10 * time.Minute,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clock := newAffinityTestClock()
			loser, winner := "ef-1-"+t.Name(), "ef-2-"+t.Name()
			manager := newExpiringFirstManager(t, clock, loser, winner)
			recordSevenDay(manager, clock, winner, tc.winnerShare, tc.winnerResetIn)
			recordSevenDay(manager, clock, loser, tc.loserShare, tc.loserResetIn)

			for _, thread := range []string{"thread-1", "thread-2", "thread-3"} {
				if got := executeClaudeThread(t, manager, thread); got != winner {
					t.Fatalf("%s: credential = %s, want the more urgent %s", thread, got, winner)
				}
			}
		})
	}
}

func TestExpiringFirstCredentialWithNoReadingRanksAfterKnownUrgency(t *testing.T) {
	clock := newAffinityTestClock()
	noData, known := "ef-1-"+t.Name(), "ef-2-"+t.Name()
	manager := newExpiringFirstManager(t, clock, noData, known)
	// A nearly idle window far from its reset still has a known urgency.
	recordSevenDay(manager, clock, known, 0.99, 6*24*time.Hour)

	for _, thread := range []string{"thread-1", "thread-2", "thread-3"} {
		if got := executeClaudeThread(t, manager, thread); got != known {
			t.Fatalf("%s: credential = %s, want %s, which has a known urgency", thread, got, known)
		}
	}
}

func TestExpiringFirstCredentialsWithNoReadingAreUsedInTurn(t *testing.T) {
	clock := newAffinityTestClock()
	first, second, third := "ef-1-"+t.Name(), "ef-2-"+t.Name(), "ef-3-"+t.Name()
	manager := newExpiringFirstManager(t, clock, first, second, third)

	for i, want := range []string{first, second, third, first} {
		thread := "thread-" + strconv.Itoa(i)
		if got := executeClaudeThread(t, manager, thread); got != want {
			t.Fatalf("%s: credential = %s, want %s (round-robin among credentials with no reading)", thread, got, want)
		}
	}
}

func TestExpiringFirstWarmThreadKeepsBindingWhenAnotherCredentialIsMoreUrgent(t *testing.T) {
	clock := newAffinityTestClock()
	bound, urgent := "ef-1-"+t.Name(), "ef-2-"+t.Name()
	manager := newExpiringFirstManager(t, clock, bound, urgent)
	recordSevenDay(manager, clock, bound, 0.40, 3*time.Hour)
	recordSevenDay(manager, clock, urgent, 0.90, 4*24*time.Hour)

	if got := executeClaudeThread(t, manager, "warm-thread"); got != bound {
		t.Fatalf("first request: credential = %s, want the more urgent %s", got, bound)
	}

	// The other credential becomes far more urgent while the thread is warm.
	clock.Advance(10 * time.Minute)
	recordSevenDay(manager, clock, urgent, 0.95, 30*time.Minute)

	if got := executeClaudeThread(t, manager, "warm-thread"); got != bound {
		t.Fatalf("warm thread: credential = %s, want binding kept on %s", got, bound)
	}
	if got := executeClaudeThread(t, manager, "new-thread"); got != urgent {
		t.Fatalf("new thread: credential = %s, want the now more urgent %s", got, urgent)
	}
}

func TestExpiringFirstClaudeResponseHeadersSteerTheNextNewThread(t *testing.T) {
	clock := newAffinityTestClock()
	lessUrgent, moreUrgent := "ef-1-"+t.Name(), "ef-2-"+t.Name()
	manager := newExpiringFirstManager(t, clock, lessUrgent, moreUrgent)
	// Each credential's upstream answers with its own Claude unified headers.
	// Reset times are relative to the wall clock because MarkResult learns
	// readings on it; the selector's clock is set to match below.
	wallNow := time.Now()
	clock.Advance(wallNow.Sub(clock.Now()))
	headers := map[string]http.Header{
		lessUrgent: claudeUnifiedHeaders(0.10, wallNow.Add(4*24*time.Hour)), // 90% left in 4 days
		moreUrgent: claudeUnifiedHeaders(0.60, wallNow.Add(3*time.Hour)),    // 40% left in 3 hours
	}
	manager.RegisterExecutor(&mockCustomErrorExecutor{
		identifier: "claude",
		executeFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			internallogging.SetResponseHeaders(ctx, headers[auth.ID])
			return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
		},
	})

	// One pinned request per credential stands in for earlier traffic.
	for _, credential := range []string{lessUrgent, moreUrgent} {
		opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: credential}}
		if _, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: expiringFirstTestModel}, opts); errExecute != nil {
			t.Fatalf("execute pinned to %s: %v", credential, errExecute)
		}
	}
	readings := manager.QuotaReadings().Readings(moreUrgent, expiringFirstTestModel)
	if len(readings) != 2 {
		t.Fatalf("readings learned from headers = %+v, want the five-hour and seven-day windows", readings)
	}

	for _, thread := range []string{"thread-1", "thread-2"} {
		if got := executeClaudeThread(t, manager, thread); got != moreUrgent {
			t.Fatalf("%s: credential = %s, want %s, more urgent by its response headers", thread, got, moreUrgent)
		}
	}
}

func claudeUnifiedHeaders(sevenDayUsed float64, sevenDayReset time.Time) http.Header {
	return http.Header{
		"Anthropic-Ratelimit-Unified-Status":         []string{"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Status":      []string{"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"0.05"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       []string{strconv.FormatInt(sevenDayReset.Add(-time.Hour).Unix(), 10)},
		"Anthropic-Ratelimit-Unified-7d-Status":      []string{"allowed"},
		"Anthropic-Ratelimit-Unified-7d-Utilization": []string{strconv.FormatFloat(sevenDayUsed, 'f', 2, 64)},
		"Anthropic-Ratelimit-Unified-7d-Reset":       []string{strconv.FormatInt(sevenDayReset.Unix(), 10)},
	}
}

// captureInfoLogs records log entries at Info level and above for one test.
func captureInfoLogs(t *testing.T) *logtest.Hook {
	t.Helper()
	hook := new(logtest.Hook)
	oldLevel := log.GetLevel()
	savedHooks := make(log.LevelHooks)
	for level, hooks := range log.StandardLogger().Hooks {
		savedHooks[level] = append([]log.Hook(nil), hooks...)
	}
	log.SetLevel(log.InfoLevel)
	log.AddHook(hook)
	t.Cleanup(func() {
		log.SetLevel(oldLevel)
		log.StandardLogger().ReplaceHooks(savedHooks)
	})
	return hook
}

func expiringFirstPickLines(hook *logtest.Hook) []string {
	var lines []string
	for _, entry := range hook.AllEntries() {
		if strings.HasPrefix(entry.Message, "expiring-first pick") {
			lines = append(lines, entry.Message)
		}
	}
	return lines
}

func TestExpiringFirstEachPickLogsCredentialUrgencyReasonAndThread(t *testing.T) {
	clock := newAffinityTestClock()
	other, urgent := "ef-1-"+t.Name(), "ef-2-"+t.Name()
	manager := newExpiringFirstManager(t, clock, other, urgent)
	recordSevenDay(manager, clock, urgent, 0.40, 3*time.Hour)
	recordSevenDay(manager, clock, other, 0.90, 4*24*time.Hour)

	tests := []struct {
		name   string
		thread string
		want   string
	}{
		{
			name:   "new thread goes to the more urgent credential",
			thread: "thread-1",
			want:   "expiring-first pick | credential=" + urgent + " urgency=13.33%/h reason=more_urgent thread=header:thread-1",
		},
		{
			name:   "warm thread keeps its binding",
			thread: "thread-1",
			want:   "expiring-first pick | credential=" + urgent + " urgency=13.33%/h reason=binding_kept thread=header:thread-1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hook := captureInfoLogs(t)
			executeClaudeThread(t, manager, tc.thread)
			lines := expiringFirstPickLines(hook)
			if len(lines) != 1 {
				t.Fatalf("pick log lines = %q, want exactly one", lines)
			}
			if !strings.HasPrefix(lines[0], tc.want) {
				t.Fatalf("pick log line = %q, want prefix %q", lines[0], tc.want)
			}
		})
	}
}

func TestExpiringFirstPickLogSaysNoDataWithoutAReading(t *testing.T) {
	clock := newAffinityTestClock()
	credential := "ef-1-" + t.Name()
	manager := newExpiringFirstManager(t, clock, credential)

	hook := captureInfoLogs(t)
	executeClaudeThread(t, manager, "thread-no-data")
	lines := expiringFirstPickLines(hook)
	want := "expiring-first pick | credential=" + credential + " urgency=no_data reason=no_data thread=header:thread-no-data"
	if len(lines) != 1 || !strings.HasPrefix(lines[0], want) {
		t.Fatalf("pick log lines = %q, want one line with prefix %q", lines, want)
	}
}

func TestExpiringFirstPickLogNeverContainsCredentialSecrets(t *testing.T) {
	clock := newAffinityTestClock()
	manager := newExpiringFirstManager(t, clock)
	const secret = "sk-ant-oat01-expiring-first-secret"
	credential := "ef-secret-" + t.Name()
	registry.GetGlobalRegistry().RegisterClient(credential, "claude", []*registry.ModelInfo{{ID: expiringFirstTestModel}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(credential) })
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{
		ID:         credential,
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{"api_key": secret},
		Metadata:   map[string]any{"access_token": secret},
	}); errRegister != nil {
		t.Fatal(errRegister)
	}
	recordSevenDay(manager, clock, credential, 0.5, time.Hour)

	hook := captureInfoLogs(t)
	executeClaudeThread(t, manager, "thread-secret")
	lines := expiringFirstPickLines(hook)
	if len(lines) != 1 {
		t.Fatalf("pick log lines = %q, want exactly one", lines)
	}
	if strings.Contains(lines[0], secret) {
		t.Fatalf("pick log line leaks a credential secret: %q", lines[0])
	}
}

package auth

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotareading"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// newQuotaMarkManager builds affinity(expiring-first) on a manager whose own
// availability check reads the same controllable clock as the selectors. The
// clock starts at the wall clock, so marks written by MarkResult (which reads
// the wall clock) agree with it.
func newQuotaMarkManager(t *testing.T, ids ...string) (*Manager, *affinityTestClock, *servedCounter) {
	t.Helper()
	clock := newAffinityTestClock()
	clock.Advance(time.Now().Sub(clock.Now()))
	manager := newExpiringFirstManager(t, clock, ids...)
	manager.nowFunc = clock.Now
	served := &servedCounter{counts: make(map[string]int)}
	manager.RegisterExecutor(&mockCustomErrorExecutor{
		identifier: "claude",
		executeFn: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			served.add(auth.ID)
			return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
		},
	})
	return manager, clock, served
}

// servedCounter counts the requests each credential's upstream received.
type servedCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func (c *servedCounter) add(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[id]++
}

func (c *servedCounter) get(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[id]
}

func recordWindow(manager *Manager, clock *affinityTestClock, credentialID string, reading quotareading.Reading) {
	reading.LearnedAt = clock.Now()
	reading.Source = quotareading.SourcePoll
	manager.QuotaReadings().Record(credentialID, reading)
}

func exhaustedFiveHourWindow(clock *affinityTestClock, resetIn time.Duration) quotareading.Reading {
	return quotareading.Reading{
		Window:    quotareading.ClaudeFiveHourWindow,
		Kind:      quotareading.KindGating,
		Length:    5 * time.Hour,
		ShareLeft: 0,
		ResetAt:   clock.Now().Add(resetIn),
	}
}

func TestBoundThreadMovesWhenAQuotaReadingShowsAnExhaustedWindow(t *testing.T) {
	tests := []struct {
		name    string
		reading func(clock *affinityTestClock) quotareading.Reading
	}{
		{
			name: "short window",
			reading: func(clock *affinityTestClock) quotareading.Reading {
				return exhaustedFiveHourWindow(clock, 2*time.Hour)
			},
		},
		{
			name: "long window",
			reading: func(clock *affinityTestClock) quotareading.Reading {
				return quotareading.Reading{
					Window:    quotareading.ClaudeSevenDayWindow,
					Kind:      quotareading.KindRanking,
					Length:    7 * 24 * time.Hour,
					ShareLeft: 0,
					ResetAt:   clock.Now().Add(3 * time.Hour),
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bound, other := "qm-1-"+t.Name(), "qm-2-"+t.Name()
			manager, clock, served := newQuotaMarkManager(t, bound, other)
			recordSevenDay(manager, clock, bound, 0.40, 3*time.Hour)
			recordSevenDay(manager, clock, other, 0.90, 4*24*time.Hour)

			if got := executeClaudeThread(t, manager, "thread-1"); got != bound {
				t.Fatalf("first request: credential = %s, want %s", got, bound)
			}
			clock.Advance(time.Minute)
			recordWindow(manager, clock, bound, tc.reading(clock))

			if got := executeClaudeThread(t, manager, "thread-1"); got != other {
				t.Fatalf("next request: credential = %s, want the thread moved to %s", got, other)
			}
			if got := served.get(bound); got != 1 {
				t.Fatalf("requests sent to the exhausted credential = %d, want 1 (no request after the reading)", got)
			}
		})
	}
}

func TestBoundThreadMovesWhenResponseHeadersShowAnExhaustedWindow(t *testing.T) {
	bound, other := "qm-1-"+t.Name(), "qm-2-"+t.Name()
	manager, clock, served := newQuotaMarkManager(t, bound, other)
	recordSevenDay(manager, clock, bound, 0.40, 3*time.Hour)
	recordSevenDay(manager, clock, other, 0.90, 4*24*time.Hour)
	// The bound credential's upstream answers 200, but its headers say the
	// five-hour window is used up until two hours from now.
	fiveHourReset := strconv.FormatInt(clock.Now().Add(2*time.Hour).Unix(), 10)
	manager.RegisterExecutor(&mockCustomErrorExecutor{
		identifier: "claude",
		executeFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			served.add(auth.ID)
			if auth.ID == bound {
				internallogging.SetResponseHeaders(ctx, http.Header{
					"Anthropic-Ratelimit-Unified-Status":         []string{"rejected"},
					"Anthropic-Ratelimit-Unified-5h-Status":      []string{"rejected"},
					"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"1.0"},
					"Anthropic-Ratelimit-Unified-5h-Reset":       []string{fiveHourReset},
				})
			}
			return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
		},
	})

	if got := executeClaudeThread(t, manager, "thread-1"); got != bound {
		t.Fatalf("first request: credential = %s, want %s", got, bound)
	}
	if got := executeClaudeThread(t, manager, "thread-1"); got != other {
		t.Fatalf("next request: credential = %s, want the thread moved to %s", got, other)
	}
	if got := served.get(bound); got != 1 {
		t.Fatalf("requests sent to the exhausted credential = %d, want 1", got)
	}
}

func TestQuotaExceededMarkClearsAtTheWindowsResetTime(t *testing.T) {
	exhausted, other := "qm-1-"+t.Name(), "qm-2-"+t.Name()
	manager, clock, _ := newQuotaMarkManager(t, exhausted, other)
	recordSevenDay(manager, clock, exhausted, 0.40, 3*time.Hour)
	recordSevenDay(manager, clock, other, 0.90, 4*24*time.Hour)
	recordWindow(manager, clock, exhausted, exhaustedFiveHourWindow(clock, time.Hour))

	if got := executeClaudeThread(t, manager, "before-reset"); got != other {
		t.Fatalf("new thread before the reset: credential = %s, want %s", got, other)
	}

	clock.Advance(59 * time.Minute)
	if got := executeClaudeThread(t, manager, "just-before-reset"); got != other {
		t.Fatalf("new thread a minute before the reset: credential = %s, want %s", got, other)
	}

	clock.Advance(time.Minute)
	if got := executeClaudeThread(t, manager, "after-reset"); got != exhausted {
		t.Fatalf("new thread at the reset: credential = %s, want the more urgent %s usable again", got, exhausted)
	}
}

func TestCredentialKeepsItsThreadsWhenNoWindowIsExhausted(t *testing.T) {
	tests := []struct {
		name    string
		reading func(clock *affinityTestClock) quotareading.Reading
	}{
		{
			name: "close to its limit",
			reading: func(clock *affinityTestClock) quotareading.Reading {
				reading := exhaustedFiveHourWindow(clock, 2*time.Hour)
				reading.ShareLeft = 0.01
				return reading
			},
		},
		{
			name: "credit balance used up",
			reading: func(clock *affinityTestClock) quotareading.Reading {
				return quotareading.Reading{Window: "extra_usage", Kind: quotareading.KindCredit, ShareLeft: 0, ResetAt: clock.Now().Add(24 * time.Hour)}
			},
		},
		{
			name: "exhausted window whose reset time has passed",
			reading: func(clock *affinityTestClock) quotareading.Reading {
				return exhaustedFiveHourWindow(clock, -time.Minute)
			},
		},
		{
			name: "exhausted window with no reset time",
			reading: func(clock *affinityTestClock) quotareading.Reading {
				reading := exhaustedFiveHourWindow(clock, 0)
				reading.ResetAt = time.Time{}
				return reading
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bound, other := "qm-1-"+t.Name(), "qm-2-"+t.Name()
			manager, clock, _ := newQuotaMarkManager(t, bound, other)
			recordSevenDay(manager, clock, bound, 0.40, 3*time.Hour)
			recordSevenDay(manager, clock, other, 0.90, 4*24*time.Hour)

			if got := executeClaudeThread(t, manager, "thread-1"); got != bound {
				t.Fatalf("first request: credential = %s, want %s", got, bound)
			}
			clock.Advance(time.Minute)
			recordWindow(manager, clock, bound, tc.reading(clock))

			if got := executeClaudeThread(t, manager, "thread-1"); got != bound {
				t.Fatalf("next request: credential = %s, want the binding kept on %s", got, bound)
			}
		})
	}
}

func creditLeft(share float64) quotareading.Reading {
	return quotareading.Reading{Window: "extra_usage", Kind: quotareading.KindCredit, ShareLeft: share}
}

// A credential whose subscription window is exhausted but which still has a
// credit balance stays usable, so the expiring-first selector can rank it
// credit-only (last) instead of the manager filtering it out.
func TestExhaustedWindowDoesNotMarkACredentialWithCreditLeft(t *testing.T) {
	tests := []struct {
		name   string
		record func(manager *Manager, clock *affinityTestClock, id string)
	}{
		{
			name: "credit reading before the exhausted window",
			record: func(manager *Manager, clock *affinityTestClock, id string) {
				recordWindow(manager, clock, id, creditLeft(0.5))
				recordWindow(manager, clock, id, exhaustedFiveHourWindow(clock, 2*time.Hour))
			},
		},
		{
			name: "credit reading after the exhausted window",
			record: func(manager *Manager, clock *affinityTestClock, id string) {
				recordWindow(manager, clock, id, exhaustedFiveHourWindow(clock, 2*time.Hour))
				recordWindow(manager, clock, id, creditLeft(0.5))
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			creditOnly := "qm-credit-" + t.Name()
			manager, clock, _ := newQuotaMarkManager(t, creditOnly)
			tc.record(manager, clock, creditOnly)

			if got := executeClaudeThread(t, manager, "thread-1"); got != creditOnly {
				t.Fatalf("credential = %s, want %s served from its credit balance", got, creditOnly)
			}
		})
	}
}

func executeClaudeThreadOnModel(t *testing.T, manager *Manager, thread, model string) string {
	t.Helper()
	opts := cliproxyexecutor.Options{
		Headers:  http.Header{"X-Session-Id": []string{thread}},
		Metadata: map[string]any{},
	}
	response, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, opts)
	if errExecute != nil {
		t.Fatalf("execute %s: %v", model, errExecute)
	}
	return string(response.Payload)
}

func TestExhaustedPerModelWindowMovesOnlyThatModelsThreads(t *testing.T) {
	const opusModel, sonnetModel = "qm-opus-4-5", "qm-sonnet-4-5"
	bound, other := "qm-1-"+t.Name(), "qm-2-"+t.Name()
	manager, clock, _ := newQuotaMarkManager(t, bound, other)
	for _, id := range []string{bound, other} {
		registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: opusModel}, {ID: sonnetModel}})
	}
	recordSevenDay(manager, clock, bound, 0.40, 3*time.Hour)
	recordSevenDay(manager, clock, other, 0.90, 4*24*time.Hour)

	for _, model := range []string{opusModel, sonnetModel} {
		if got := executeClaudeThreadOnModel(t, manager, "thread-1", model); got != bound {
			t.Fatalf("first %s request: credential = %s, want %s", model, got, bound)
		}
	}
	clock.Advance(time.Minute)
	recordWindow(manager, clock, bound, quotareading.Reading{
		Window:    "seven_day_opus",
		Kind:      quotareading.KindPerModel,
		Model:     "qm-opus",
		Length:    7 * 24 * time.Hour,
		ShareLeft: 0,
		ResetAt:   clock.Now().Add(6 * time.Hour),
	})

	if got := executeClaudeThreadOnModel(t, manager, "thread-1", opusModel); got != other {
		t.Fatalf("next %s request: credential = %s, want the thread moved to %s", opusModel, got, other)
	}
	if got := executeClaudeThreadOnModel(t, manager, "thread-1", sonnetModel); got != bound {
		t.Fatalf("next %s request: credential = %s, want the binding kept on %s", sonnetModel, got, bound)
	}
}

func TestExhaustedWindowMarksACredentialWhoseCreditIsUsedUp(t *testing.T) {
	bound, other := "qm-1-"+t.Name(), "qm-2-"+t.Name()
	manager, clock, _ := newQuotaMarkManager(t, bound, other)
	recordSevenDay(manager, clock, bound, 0.40, 3*time.Hour)
	recordSevenDay(manager, clock, other, 0.90, 4*24*time.Hour)
	recordWindow(manager, clock, bound, creditLeft(0.5))
	recordWindow(manager, clock, bound, exhaustedFiveHourWindow(clock, 2*time.Hour))

	clock.Advance(time.Minute)
	recordWindow(manager, clock, bound, creditLeft(0))

	if got := executeClaudeThread(t, manager, "thread-1"); got != other {
		t.Fatalf("credential = %s, want %s (the other credential's window and credit are both used up)", got, other)
	}
}

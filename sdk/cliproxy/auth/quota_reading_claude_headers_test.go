package auth

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// claudeHeadersExecutor answers 200 for every credential and, for the
// credentials in headers, sets those response headers. It counts the requests
// each credential received per model.
func claudeHeadersExecutor(headers map[string]func() http.Header) (*mockCustomErrorExecutor, func(id, model string) int) {
	var mu sync.Mutex
	served := make(map[string]int)
	executor := &mockCustomErrorExecutor{
		identifier: "claude",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			mu.Lock()
			served[auth.ID+"|"+req.Model]++
			mu.Unlock()
			if build, ok := headers[auth.ID]; ok {
				internallogging.SetResponseHeaders(ctx, build())
			}
			return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
		},
	}
	return executor, func(id, model string) int {
		mu.Lock()
		defer mu.Unlock()
		return served[id+"|"+model]
	}
}

// A 200 response whose headers show the Fable 7-day window used up moves the
// credential's Fable threads before any 429, and keeps its other models' threads.
func TestFableResponseHeadersMoveOnlyFableThreads(t *testing.T) {
	const fableModel, opusModel = "claude-fable-5", "claude-opus-4-5"
	bound, other := "qm-1-"+t.Name(), "qm-2-"+t.Name()
	manager, clock, _ := newQuotaMarkManager(t, bound, other)
	for _, id := range []string{bound, other} {
		registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: fableModel}, {ID: opusModel}})
	}
	recordSevenDay(manager, clock, bound, 0.40, 3*time.Hour)
	recordSevenDay(manager, clock, other, 0.90, 4*24*time.Hour)
	executor, served := claudeHeadersExecutor(map[string]func() http.Header{
		bound: func() http.Header {
			headers := claudeUnifiedHeaders(0.60, clock.Now().Add(3*time.Hour))
			headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Status", "rejected")
			headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Utilization", "1.0")
			headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Reset", strconv.FormatInt(clock.Now().Add(6*time.Hour).Unix(), 10))
			return headers
		},
	})
	manager.RegisterExecutor(executor)

	for _, model := range []string{fableModel, opusModel} {
		if got := executeClaudeThreadOnModel(t, manager, model+"-thread", model); got != bound {
			t.Fatalf("first %s request: credential = %s, want %s", model, got, bound)
		}
	}
	if got := executeClaudeThreadOnModel(t, manager, fableModel+"-thread", fableModel); got != other {
		t.Fatalf("next %s request: credential = %s, want the thread moved to %s", fableModel, got, other)
	}
	if got := executeClaudeThreadOnModel(t, manager, opusModel+"-thread", opusModel); got != bound {
		t.Fatalf("next %s request: credential = %s, want the binding kept on %s", opusModel, got, bound)
	}
	if got := served(bound, fableModel); got != 1 {
		t.Fatalf("%s requests sent to the exhausted credential = %d, want 1", fableModel, got)
	}
}

// A Claude account with extra usage on keeps serving after its five-hour
// window is used up: Claude serves it from overage. Its headers make it
// credit-only (ranked last), not blocked. With overage rejected it is blocked.
func TestClaudeOverageHeadersDecideWhetherAnExhaustedCredentialServesFromCredit(t *testing.T) {
	tests := []struct {
		name          string
		overageStatus string
		wantUsable    bool
	}{
		{name: "overage allowed", overageStatus: "allowed", wantUsable: true},
		{name: "overage allowed with warning", overageStatus: "allowed_warning", wantUsable: true},
		{name: "overage rejected", overageStatus: "rejected", wantUsable: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			overage, fresh := "qm-overage-"+t.Name(), "qm-fresh-"+t.Name()
			manager, clock, _ := newQuotaMarkManager(t, overage, fresh)
			recordSevenDay(manager, clock, overage, 0.40, 3*time.Hour)
			recordSevenDay(manager, clock, fresh, 0.90, 4*24*time.Hour)
			executor, served := claudeHeadersExecutor(map[string]func() http.Header{
				overage: func() http.Header {
					headers := claudeUnifiedHeaders(0.60, clock.Now().Add(3*time.Hour))
					headers.Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
					headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "1.0")
					headers.Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(clock.Now().Add(2*time.Hour).Unix(), 10))
					headers.Set("Anthropic-Ratelimit-Unified-Overage-Status", tc.overageStatus)
					if tc.overageStatus == "rejected" {
						headers.Set("Anthropic-Ratelimit-Unified-Overage-Disabled-Reason", "org_spend_cap_reached")
					}
					return headers
				},
			})
			manager.RegisterExecutor(executor)
			// One pinned request stands in for the traffic that used up the window.
			pinned := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: overage}}
			if _, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: expiringFirstTestModel}, pinned); errExecute != nil {
				t.Fatalf("execute pinned to %s: %v", overage, errExecute)
			}

			if got := executeClaudeThread(t, manager, "thread-1"); got != fresh {
				t.Fatalf("new thread: credential = %s, want %s ahead of the credential with its window used up", got, fresh)
			}

			// The other credential's window is used up too, with no credit.
			recordQuotaReading(manager, clock, fresh, exhaustedFiveHourWindow(clock, time.Hour))
			opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"thread-2"}}, Metadata: map[string]any{}}
			response, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: expiringFirstTestModel}, opts)
			if !tc.wantUsable {
				if errExecute == nil {
					t.Fatalf("new thread: credential = %s, want no usable credential", string(response.Payload))
				}
				if got := served(overage, expiringFirstTestModel); got != 1 {
					t.Fatalf("requests sent to the blocked credential = %d, want 1", got)
				}
				return
			}
			if errExecute != nil {
				t.Fatalf("new thread: %v, want %s served from extra usage", errExecute, overage)
			}
			if got := string(response.Payload); got != overage {
				t.Fatalf("new thread: credential = %s, want %s served from extra usage", got, overage)
			}
		})
	}
}

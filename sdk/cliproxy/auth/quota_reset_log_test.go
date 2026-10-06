package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotareading"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestQuotaResetLogReportsShareLeftFromTheLastResponseHeaders(t *testing.T) {
	clock := newAffinityTestClock()
	credential := "ef-reset-" + t.Name()
	manager := newExpiringFirstManager(t, clock, credential)
	// MarkResult learns readings on the wall clock, so reset times follow it.
	wallNow := time.Now().Truncate(time.Second)
	clock.Advance(wallNow.Sub(clock.Now()))
	sevenDayReset := wallNow.Add(3 * time.Hour)
	manager.RegisterExecutor(&mockCustomErrorExecutor{
		identifier: "claude",
		executeFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			internallogging.SetResponseHeaders(ctx, claudeUnifiedHeaders(0.70, sevenDayReset))
			return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
		},
	})
	executeClaudeThread(t, manager, "thread-reset")
	hook := captureInfoLogs(t)
	resetLog := manager.NewQuotaResetLog(clock.Now)

	clock.Advance(2*time.Hour + time.Second) // past the five-hour reset only
	resetLog.Check()
	clock.Advance(time.Hour) // past the seven-day reset
	resetLog.Check()
	resetLog.Check()

	var lines []string
	for _, entry := range hook.AllEntries() {
		if strings.HasPrefix(entry.Message, quotareading.ResetLogMessagePrefix) {
			lines = append(lines, entry.Message)
		}
	}
	prefix := "quota window reset | credential=" + credential + " provider=claude "
	want := []string{
		prefix + "window=five_hour share_left=0.9500 reset_at=" + sevenDayReset.Add(-time.Hour).UTC().Format(time.RFC3339) + " kind=gating",
		prefix + "window=seven_day share_left=0.3000 reset_at=" + sevenDayReset.UTC().Format(time.RFC3339) + " kind=ranking",
	}
	if len(lines) != len(want) {
		t.Fatalf("window-reset lines = %q, want one per window: %q", lines, want)
	}
	for i := range want {
		if !strings.HasPrefix(lines[i], want[i]+" learned_at=") {
			t.Fatalf("line %d = %q, want prefix %q", i, lines[i], want[i])
		}
	}
}

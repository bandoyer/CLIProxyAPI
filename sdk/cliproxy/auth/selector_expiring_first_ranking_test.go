package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotareading"
)

// recordGating gives a credential a Claude 5-hour (gating) reading.
func recordGating(manager *Manager, clock *affinityTestClock, credentialID string, shareLeft float64, resetIn time.Duration) {
	manager.QuotaReadings().Record(credentialID, quotareading.Reading{
		Window:    quotareading.ClaudeFiveHourWindow,
		Kind:      quotareading.KindGating,
		Length:    5 * time.Hour,
		ShareLeft: shareLeft,
		ResetAt:   clock.Now().Add(resetIn),
		LearnedAt: clock.Now(),
		Source:    quotareading.SourceHeader,
	})
}

func TestExpiringFirstReadingWithPassedResetRanksAsFullWindow(t *testing.T) {
	clock := newAffinityTestClock()
	passed, fresh := "ef-1-"+t.Name(), "ef-2-"+t.Name()
	manager := newExpiringFirstManager(t, clock, passed, fresh)
	// 5% left at the last reading, but its reset was an hour ago: the window
	// is full again and resets in 7 days, so 100% / 168h = 0.60%/h.
	recordSevenDay(manager, clock, passed, 0.05, -time.Hour)
	// 50% left in 2 days is 1.04%/h, more urgent than the refilled window.
	recordSevenDay(manager, clock, fresh, 0.50, 48*time.Hour)

	hook := captureInfoLogs(t)
	if got := executeClaudeThread(t, manager, "thread-1"); got != fresh {
		t.Fatalf("credential = %s, want %s: a passed reset counts as a full 7-day window", got, fresh)
	}
	recordSevenDay(manager, clock, fresh, 0.50, 4*24*time.Hour) // now 0.52%/h
	if got := executeClaudeThread(t, manager, "thread-2"); got != passed {
		t.Fatalf("credential = %s, want %s at 0.60%%/h", got, passed)
	}
	lines := expiringFirstPickLines(hook)
	want := "expiring-first pick | credential=" + passed + " urgency=0.60%/h reason=more_urgent"
	if len(lines) != 2 || !strings.HasPrefix(lines[1], want) {
		t.Fatalf("pick log lines = %q, want the second with prefix %q", lines, want)
	}
}

func TestExpiringFirstExhaustedWindowsWithPassedResetAreUsable(t *testing.T) {
	clock := newAffinityTestClock()
	noData, refilled := "ef-1-"+t.Name(), "ef-2-"+t.Name()
	manager := newExpiringFirstManager(t, clock, noData, refilled)
	recordSevenDay(manager, clock, refilled, 0, -time.Minute)
	recordGating(manager, clock, refilled, 0, -time.Minute)

	for _, thread := range []string{"thread-1", "thread-2"} {
		if got := executeClaudeThread(t, manager, thread); got != refilled {
			t.Fatalf("%s: credential = %s, want %s, whose exhausted windows have reset", thread, got, refilled)
		}
	}
}

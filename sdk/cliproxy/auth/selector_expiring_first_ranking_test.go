package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotareading"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
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

func TestExpiringFirstExhaustedGatingWindowSkipsCredentialUntilItsReset(t *testing.T) {
	clock := newAffinityTestClock()
	gated, other := "ef-1-"+t.Name(), "ef-2-"+t.Name()
	manager := newExpiringFirstManager(t, clock, gated, other)
	// The gated credential is the more urgent by its ranking window, but its
	// 5-hour window is used up for the next hour.
	recordSevenDay(manager, clock, gated, 0.90, 2*time.Hour)
	recordGating(manager, clock, gated, 0, time.Hour)
	recordSevenDay(manager, clock, other, 0.90, 4*24*time.Hour)

	for _, thread := range []string{"thread-1", "thread-2"} {
		if got := executeClaudeThread(t, manager, thread); got != other {
			t.Fatalf("%s: credential = %s, want %s while %s has an exhausted 5-hour window", thread, got, other, gated)
		}
	}

	clock.Advance(time.Hour + time.Minute)
	if got := executeClaudeThread(t, manager, "thread-after-reset"); got != gated {
		t.Fatalf("after the 5-hour reset: credential = %s, want the more urgent %s", got, gated)
	}
}

func TestExpiringFirstEveryCredentialExhaustedReturnsQuotaCooldown(t *testing.T) {
	clock := newAffinityTestClock()
	first, second := "ef-1-"+t.Name(), "ef-2-"+t.Name()
	manager := newExpiringFirstManager(t, clock, first, second)
	recordGating(manager, clock, first, 0, 2*time.Hour)
	recordSevenDay(manager, clock, second, 0, 30*time.Minute)

	opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"thread-1"}}, Metadata: map[string]any{}}
	_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: expiringFirstTestModel}, opts)
	var cooldown *modelCooldownError
	if !errors.As(errExecute, &cooldown) {
		t.Fatalf("execute error = %v, want a model cooldown error", errExecute)
	}
	if cooldown.resetIn != 30*time.Minute {
		t.Fatalf("cooldown reset in %s, want 30m, the soonest reset of an exhausted window", cooldown.resetIn)
	}
}

// recordCredit gives a credential a credit balance reading, which has no reset.
func recordCredit(manager *Manager, clock *affinityTestClock, credentialID string, shareLeft float64) {
	manager.QuotaReadings().Record(credentialID, quotareading.Reading{
		Window:    "extra_usage",
		Kind:      quotareading.KindCredit,
		ShareLeft: shareLeft,
		LearnedAt: clock.Now(),
		Source:    quotareading.SourcePoll,
	})
}

func TestExpiringFirstOrderInsideTierIsKnownUrgencyThenNoDataThenCreditOnly(t *testing.T) {
	clock := newAffinityTestClock()
	// IDs sort opposite to the expected order, so ID order cannot pass.
	creditOnly, noData, known := "ef-1-"+t.Name(), "ef-2-"+t.Name(), "ef-3-"+t.Name()
	manager := newExpiringFirstManager(t, clock, creditOnly, noData, known)
	// A full credit balance never sets urgency.
	recordCredit(manager, clock, creditOnly, 1)
	recordSevenDay(manager, clock, known, 0.10, 6*24*time.Hour)

	hook := captureInfoLogs(t)
	if got := executeClaudeThread(t, manager, "thread-1"); got != known {
		t.Fatalf("thread-1: credential = %s, want %s with a known urgency", got, known)
	}

	recordGating(manager, clock, known, 0, time.Hour)
	if got := executeClaudeThread(t, manager, "thread-2"); got != noData {
		t.Fatalf("thread-2: credential = %s, want %s with no reading before the credit-only %s", got, noData, creditOnly)
	}

	recordSevenDay(manager, clock, noData, 0, 2*24*time.Hour)
	if got := executeClaudeThread(t, manager, "thread-3"); got != creditOnly {
		t.Fatalf("thread-3: credential = %s, want the credit-only %s last", got, creditOnly)
	}

	lines := expiringFirstPickLines(hook)
	want := "expiring-first pick | credential=" + creditOnly + " urgency=no_data reason=credit_only thread=header:thread-3"
	if len(lines) != 3 || !strings.HasPrefix(lines[2], want) {
		t.Fatalf("pick log lines = %q, want the third with prefix %q", lines, want)
	}
}

func TestExpiringFirstExhaustedSubscriptionWithCreditsLeftServesFromCreditsLast(t *testing.T) {
	clock := newAffinityTestClock()
	credits, subscription := "ef-1-"+t.Name(), "ef-2-"+t.Name()
	manager := newExpiringFirstManager(t, clock, credits, subscription)
	// The credits credential's 7-day window is used up for two days, but it
	// can still serve from its extra usage balance.
	recordSevenDay(manager, clock, credits, 0, 2*24*time.Hour)
	recordCredit(manager, clock, credits, 0.5)
	recordSevenDay(manager, clock, subscription, 0.95, 6*24*time.Hour)

	if got := executeClaudeThread(t, manager, "thread-1"); got != subscription {
		t.Fatalf("thread-1: credential = %s, want subscription quota %s before credits", got, subscription)
	}

	recordGating(manager, clock, subscription, 0, time.Hour)
	if got := executeClaudeThread(t, manager, "thread-2"); got != credits {
		t.Fatalf("thread-2: credential = %s, want %s serving from credits", got, credits)
	}

	// With the balance spent too, nothing can serve until the 5-hour reset.
	recordCredit(manager, clock, credits, 0)
	opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"thread-3"}}, Metadata: map[string]any{}}
	_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: expiringFirstTestModel}, opts)
	var cooldown *modelCooldownError
	if !errors.As(errExecute, &cooldown) || cooldown.resetIn != time.Hour {
		t.Fatalf("execute error = %v, want a model cooldown error until the 5-hour reset in 1h", errExecute)
	}
}

func TestExpiringFirstCreditOnlyCredentialsAreUsedInTurn(t *testing.T) {
	clock := newAffinityTestClock()
	first, second := "ef-1-"+t.Name(), "ef-2-"+t.Name()
	manager := newExpiringFirstManager(t, clock, first, second)
	recordCredit(manager, clock, first, 0.2)
	recordCredit(manager, clock, second, 0.9)

	for i, want := range []string{first, second, first} {
		thread := "thread-" + string(rune('a'+i))
		if got := executeClaudeThread(t, manager, thread); got != want {
			t.Fatalf("%s: credential = %s, want %s (round-robin among credit-only credentials)", thread, got, want)
		}
	}
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

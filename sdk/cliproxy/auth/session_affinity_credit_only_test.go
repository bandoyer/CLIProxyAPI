package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// makeCreditOnly leaves a credential able to serve only from its credit
// balance: its 5-hour window is used up for two hours and credit is left.
func makeCreditOnly(manager *Manager, clock *affinityTestClock, id string) {
	recordQuotaReading(manager, clock, id, exhaustedFiveHourWindow(clock, 2*time.Hour))
	recordQuotaReading(manager, clock, id, creditLeft(0.5))
}

func TestBoundThreadMovesOffCreditOnlyCredentialWhileItsTierHasSubscriptionQuota(t *testing.T) {
	bound, subscription := "qm-1-"+t.Name(), "qm-2-"+t.Name()
	manager, clock, _ := newQuotaMarkManager(t, bound, subscription)
	recordSevenDay(manager, clock, bound, 0.40, 3*time.Hour)
	recordSevenDay(manager, clock, subscription, 0.90, 4*24*time.Hour)
	if got := executeClaudeThread(t, manager, "thread-1"); got != bound {
		t.Fatalf("first request: credential = %s, want the more urgent %s", got, bound)
	}
	makeCreditOnly(manager, clock, bound)

	hook := captureInfoLogs(t)
	if got := executeClaudeThread(t, manager, "thread-1"); got != subscription {
		t.Fatalf("credential = %s, want the thread moved to subscription quota on %s", got, subscription)
	}
	if got := executeClaudeThread(t, manager, "thread-1"); got != subscription {
		t.Fatalf("next request: credential = %s, want the new binding on %s kept", got, subscription)
	}

	want := "affinity binding ended | thread=header:thread-1 credential=" + bound +
		" provider=claude model=" + expiringFirstTestModel + " reason=subscription_exhausted"
	if lines := bindingEndedLines(hook); len(lines) != 1 || lines[0] != want {
		t.Fatalf("binding-end lines = %q, want exactly %q", lines, want)
	}
	picks := expiringFirstPickLines(hook)
	wantPick := "expiring-first pick | credential=" + subscription + " urgency="
	if len(picks) != 2 || !strings.HasPrefix(picks[0], wantPick) || !strings.Contains(picks[0], " reason=more_urgent thread=header:thread-1 ") ||
		!strings.Contains(picks[1], " reason=binding_kept ") {
		t.Fatalf("pick lines = %q, want a more_urgent pick of %s, then binding_kept", picks, subscription)
	}
}

func TestBindingMovedOffCreditOnlyCredentialSurvivesManagerRestart(t *testing.T) {
	ctx := context.Background()
	store := NewFileSessionBindingStore(t.TempDir())
	bound, subscription := "qm-1-"+t.Name(), "qm-2-"+t.Name()
	before, clock, _ := newQuotaMarkManager(t, bound, subscription)
	recordSevenDay(before, clock, bound, 0.40, 3*time.Hour)
	recordSevenDay(before, clock, subscription, 0.90, 4*24*time.Hour)
	executeClaudeThread(t, before, "thread-1")
	makeCreditOnly(before, clock, bound)
	if got := executeClaudeThread(t, before, "thread-1"); got != subscription {
		t.Fatalf("before restart: credential = %s, want the thread moved to %s", got, subscription)
	}
	if errSave := before.SaveSessionBindings(ctx, store); errSave != nil {
		t.Fatalf("save bindings: %v", errSave)
	}

	// Without readings, a cold pick would take the first credential by ID.
	after, _, _ := newQuotaMarkManager(t, bound, subscription)
	if _, errRestore := after.RestoreSessionBindings(ctx, store); errRestore != nil {
		t.Fatalf("restore bindings: %v", errRestore)
	}
	if got := executeClaudeThread(t, after, "thread-1"); got != subscription {
		t.Fatalf("after restart: credential = %s, want the restored binding on %s", got, subscription)
	}
}

func TestBoundThreadMovesOffCreditOnlyCredentialWithinItsOwnTier(t *testing.T) {
	manager, clock, _ := newQuotaMarkManager(t)
	bound, sameTier, higherTier := "qm-1-"+t.Name(), "qm-2-"+t.Name(), "qm-3-"+t.Name()
	registerClaudeCredential(t, manager, bound, 0)
	registerClaudeCredential(t, manager, sameTier, 0)
	recordSevenDay(manager, clock, bound, 0.40, 3*time.Hour)
	recordSevenDay(manager, clock, sameTier, 0.90, 4*24*time.Hour)
	if got := executeClaudeThread(t, manager, "thread-1"); got != bound {
		t.Fatalf("first request: credential = %s, want the more urgent %s", got, bound)
	}
	// A higher tier appears that can serve only from credits.
	registerClaudeCredential(t, manager, higherTier, 10)
	makeCreditOnly(manager, clock, higherTier)
	makeCreditOnly(manager, clock, bound)

	if got := executeClaudeThread(t, manager, "thread-1"); got != sameTier {
		t.Fatalf("credential = %s, want the thread moved to subscription quota on %s, not to the credit-only %s", got, sameTier, higherTier)
	}
}

func TestBoundThreadStaysOnCreditOnlyCredentialWhenEveryCandidateIsCreditOnly(t *testing.T) {
	bound, other := "qm-1-"+t.Name(), "qm-2-"+t.Name()
	manager, clock, _ := newQuotaMarkManager(t, bound, other)
	recordSevenDay(manager, clock, bound, 0.40, 3*time.Hour)
	recordSevenDay(manager, clock, other, 0.90, 4*24*time.Hour)
	if got := executeClaudeThread(t, manager, "thread-1"); got != bound {
		t.Fatalf("first request: credential = %s, want the more urgent %s", got, bound)
	}
	makeCreditOnly(manager, clock, bound)
	makeCreditOnly(manager, clock, other)

	hook := captureInfoLogs(t)
	if got := executeClaudeThread(t, manager, "thread-1"); got != bound {
		t.Fatalf("credential = %s, want the thread kept on %s: moving between credit-only credentials only pays a cache write", got, bound)
	}
	if lines := bindingEndedLines(hook); len(lines) != 0 {
		t.Fatalf("binding-end lines = %q, want none while the binding is kept", lines)
	}
	if picks := expiringFirstPickLines(hook); len(picks) != 1 || !strings.Contains(picks[0], " reason=binding_kept ") {
		t.Fatalf("pick lines = %q, want one binding_kept pick", picks)
	}
}

func TestBoundThreadStaysOnCreditOnlyCredentialWhenOnlyALowerTierHasSubscriptionQuota(t *testing.T) {
	manager, clock, _ := newQuotaMarkManager(t)
	bound, lowerTier := "qm-1-"+t.Name(), "qm-2-"+t.Name()
	registerClaudeCredential(t, manager, bound, 10)
	registerClaudeCredential(t, manager, lowerTier, 0)
	recordSevenDay(manager, clock, bound, 0.40, 3*time.Hour)
	recordSevenDay(manager, clock, lowerTier, 0.90, 4*24*time.Hour)
	if got := executeClaudeThread(t, manager, "thread-1"); got != bound {
		t.Fatalf("first request: credential = %s, want %s in the higher tier", got, bound)
	}
	makeCreditOnly(manager, clock, bound)

	hook := captureInfoLogs(t)
	if got := executeClaudeThread(t, manager, "thread-1"); got != bound {
		t.Fatalf("credential = %s, want the thread kept on %s: the subscription quota on %s is in a lower tier", got, bound, lowerTier)
	}
	if lines := bindingEndedLines(hook); len(lines) != 0 {
		t.Fatalf("binding-end lines = %q, want none while the binding is kept", lines)
	}
}

// executeClaudeCodeRequest sends a Claude Code request for the root session,
// as a subagent when agentID is set.
func executeClaudeCodeRequest(t *testing.T, manager *Manager, rootSession, agentID string) string {
	t.Helper()
	headers := http.Header{"X-Claude-Code-Session-Id": []string{rootSession}}
	if agentID != "" {
		headers.Set("X-Claude-Code-Agent-Id", agentID)
	}
	opts := cliproxyexecutor.Options{Headers: headers, Metadata: map[string]any{}}
	response, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: expiringFirstTestModel}, opts)
	if errExecute != nil {
		t.Fatalf("execute: %v", errExecute)
	}
	return string(response.Payload)
}

func TestSubagentDoesNotFollowItsParentOntoACreditOnlyCredentialWhileItsTierHasSubscriptionQuota(t *testing.T) {
	parentCredential, subscription := "qm-1-"+t.Name(), "qm-2-"+t.Name()
	manager, clock, _ := newQuotaMarkManager(t, parentCredential, subscription)
	recordSevenDay(manager, clock, parentCredential, 0.40, 3*time.Hour)
	recordSevenDay(manager, clock, subscription, 0.90, 4*24*time.Hour)
	if got := executeClaudeCodeRequest(t, manager, "root-1", ""); got != parentCredential {
		t.Fatalf("parent: credential = %s, want the more urgent %s", got, parentCredential)
	}
	makeCreditOnly(manager, clock, parentCredential)

	hook := captureInfoLogs(t)
	if got := executeClaudeCodeRequest(t, manager, "root-1", "agent-1"); got != subscription {
		t.Fatalf("subagent: credential = %s, want subscription quota on %s, not the parent's credit-only %s", got, subscription, parentCredential)
	}
	if lines := bindingEndedLines(hook); len(lines) != 0 {
		t.Fatalf("binding-end lines = %q, want none: the subagent had no binding of its own", lines)
	}
}

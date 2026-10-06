package auth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	logtest "github.com/sirupsen/logrus/hooks/test"
)

func bindingEndedLines(hook *logtest.Hook) []string {
	var lines []string
	for _, entry := range hook.AllEntries() {
		if strings.HasPrefix(entry.Message, "affinity binding ended | ") {
			lines = append(lines, entry.Message)
		}
	}
	return lines
}

func TestBindingEndLogRecordsThreadCredentialAndReasonForAnExhaustedWindow(t *testing.T) {
	bound, other := "qm-1-"+t.Name(), "qm-2-"+t.Name()
	manager, clock, _ := newQuotaMarkManager(t, bound, other)
	recordSevenDay(manager, clock, bound, 0.40, 3*time.Hour)
	recordSevenDay(manager, clock, other, 0.90, 4*24*time.Hour)
	executeClaudeThread(t, manager, "thread-1")
	recordQuotaReading(manager, clock, bound, exhaustedFiveHourWindow(clock, 2*time.Hour))

	hook := captureInfoLogs(t)
	if got := executeClaudeThread(t, manager, "thread-1"); got != other {
		t.Fatalf("credential = %s, want the thread moved to %s", got, other)
	}

	want := "affinity binding ended | thread=header:thread-1 credential=" + bound +
		" provider=claude model=" + expiringFirstTestModel + " reason=quota_window_exhausted"
	if lines := bindingEndedLines(hook); len(lines) != 1 || lines[0] != want {
		t.Fatalf("binding-end lines = %q, want exactly %q", lines, want)
	}
}

func TestBindingEndLogRecordsTheReasonACredentialFailed(t *testing.T) {
	tests := []struct {
		name   string
		status int
		reason string
	}{
		{name: "429", status: http.StatusTooManyRequests, reason: "quota_exceeded_429"},
		{name: "401", status: http.StatusUnauthorized, reason: "unauthorized"},
		{name: "403", status: http.StatusForbidden, reason: "forbidden"},
		{name: "402", status: http.StatusPaymentRequired, reason: "cooldown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newDetourHarness(t, false)
			thread := "thread-end-" + tc.name
			warm, _ := h.startThread(t, thread)

			hook := captureInfoLogs(t)
			h.upstream.failNext(warm, &Error{HTTPStatus: tc.status, Message: "credential failed"})
			if _, errServe := h.serve(thread); errServe != nil {
				t.Fatalf("request after %d: %v", tc.status, errServe)
			}

			want := "affinity binding ended | thread=header:" + thread + " credential=" + warm +
				" provider=codex model=" + detourTestModel + " reason=" + tc.reason
			if lines := bindingEndedLines(hook); len(lines) != 1 || lines[0] != want {
				t.Fatalf("binding-end lines = %q, want exactly %q", lines, want)
			}
		})
	}
}

func TestBindingEndLogRecordsACooldownFromAnotherThread(t *testing.T) {
	h := newDetourHarness(t, false)
	thread := "thread-end-cooldown"
	warm, _ := h.startThread(t, thread)
	// Another thread exhausts the warm credential's quota with a 429.
	h.MarkResult(context.Background(), Result{
		AuthID:   warm,
		Provider: "codex",
		Model:    detourTestModel,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	hook := captureInfoLogs(t)
	if _, errServe := h.serve(thread); errServe != nil {
		t.Fatalf("request: %v", errServe)
	}

	want := "affinity binding ended | thread=header:" + thread + " credential=" + warm +
		" provider=codex model=" + detourTestModel + " reason=quota_exceeded_429"
	if lines := bindingEndedLines(hook); len(lines) != 1 || lines[0] != want {
		t.Fatalf("binding-end lines = %q, want exactly %q", lines, want)
	}
}

func TestBindingEndLogRecordsADisabledCredential(t *testing.T) {
	h := newDetourHarness(t, false)
	thread := "thread-end-disabled"
	warm, _ := h.startThread(t, thread)
	auth, _ := h.GetByID(warm)
	auth.Disabled = true
	auth.Status = StatusDisabled
	if _, errUpdate := h.Update(WithSkipPersist(context.Background()), auth); errUpdate != nil {
		t.Fatalf("disable: %v", errUpdate)
	}

	hook := captureInfoLogs(t)
	if _, errServe := h.serve(thread); errServe != nil {
		t.Fatalf("request: %v", errServe)
	}

	want := "affinity binding ended | thread=header:" + thread + " credential=" + warm +
		" provider=codex model=" + detourTestModel + " reason=disabled"
	if lines := bindingEndedLines(hook); len(lines) != 1 || lines[0] != want {
		t.Fatalf("binding-end lines = %q, want exactly %q", lines, want)
	}
}

func TestBindingKeptThroughAnOverloadDetourLogsNoBindingEnd(t *testing.T) {
	h := newDetourHarness(t, false)
	thread := "thread-end-detour"
	warm, _ := h.startThread(t, thread)

	hook := captureInfoLogs(t)
	h.upstream.failNext(warm, &Error{HTTPStatus: 529, Message: "overloaded_error"})
	if _, errServe := h.serve(thread); errServe != nil {
		t.Fatalf("request after 529: %v", errServe)
	}
	if _, errServe := h.serve(thread); errServe != nil {
		t.Fatalf("next request: %v", errServe)
	}

	if lines := bindingEndedLines(hook); len(lines) != 0 {
		t.Fatalf("binding-end lines = %q, want none while the binding is kept", lines)
	}
}

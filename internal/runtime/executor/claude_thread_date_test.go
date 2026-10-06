package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const claudeThreadDateTextPayload = `{"model":"claude-opus-4-6","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`

// claudeThreadDateHarness sends cloaked requests to an httptest upstream with an
// injected clock and reports the date block each upstream body carried.
type claudeThreadDateHarness struct {
	t        *testing.T
	executor *ClaudeExecutor
	auth     *cliproxyauth.Auth
	mu       sync.Mutex
	now      time.Time
	lastBody []byte
}

func newClaudeThreadDateHarness(t *testing.T, cfg *config.Config) *claudeThreadDateHarness {
	t.Helper()
	h := &claudeThreadDateHarness{t: t}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.lastBody = body
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	t.Cleanup(server.Close)
	t.Cleanup(setClaudeCodeNowForTest(func() time.Time {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.now
	}))
	if cfg == nil {
		cfg = &config.Config{}
	}
	h.executor = NewClaudeExecutor(cfg)
	h.auth = &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":    "key-thread-date",
		"base_url":   server.URL,
		"cloak_mode": "always",
		"timezone":   "UTC",
	}}
	return h
}

// send executes one request at the given instant. An empty thread sends no
// thread identifier header.
func (h *claudeThreadDateHarness) send(at time.Time, thread, payload string) string {
	h.t.Helper()
	h.mu.Lock()
	h.now = at
	h.mu.Unlock()
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
	if thread != "" {
		opts.Headers = http.Header{"X-Session-Id": []string{thread}}
	}
	if _, errExecute := h.executor.Execute(context.Background(), h.auth, cliproxyexecutor.Request{
		Model:   "claude-opus-4-6",
		Payload: []byte(payload),
	}, opts); errExecute != nil {
		h.t.Fatalf("Execute() error = %v", errExecute)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, block := range gjson.GetBytes(h.lastBody, "messages.0.content").Array() {
		if text := block.Get("text").String(); isClaudeCodeCurrentDateReminder(text) {
			return text
		}
	}
	h.t.Fatalf("upstream body has no date block: %s", h.lastBody)
	return ""
}

func dateBlockFor(day string) string {
	at, errParse := time.Parse("2006-01-02", day)
	if errParse != nil {
		panic(errParse)
	}
	return claudeCodeCurrentDateReminder(at)
}

func TestClaudeCloakedDateBlockStaysPinnedAcrossMidnightForOneThread(t *testing.T) {
	h := newClaudeThreadDateHarness(t, nil)
	thread := "thread-date-midnight-" + t.Name()

	before := time.Date(2025, 3, 1, 23, 50, 0, 0, time.UTC)
	after := time.Date(2025, 3, 2, 0, 10, 0, 0, time.UTC)

	if got, want := h.send(before, thread, claudeThreadDateTextPayload), dateBlockFor("2025-03-01"); got != want {
		t.Fatalf("first request date block = %q, want %q", got, want)
	}
	if got, want := h.send(after, thread, claudeThreadDateTextPayload), dateBlockFor("2025-03-01"); got != want {
		t.Fatalf("after midnight the thread's date block = %q, want pinned %q", got, want)
	}
}

func TestClaudeCloakedDateBlockNewThreadAfterMidnightGetsNewDate(t *testing.T) {
	h := newClaudeThreadDateHarness(t, nil)
	oldThread := "thread-date-old-" + t.Name()
	newThread := "thread-date-new-" + t.Name()

	h.send(time.Date(2025, 4, 1, 23, 50, 0, 0, time.UTC), oldThread, claudeThreadDateTextPayload)
	if got, want := h.send(time.Date(2025, 4, 2, 0, 10, 0, 0, time.UTC), newThread, claudeThreadDateTextPayload), dateBlockFor("2025-04-02"); got != want {
		t.Fatalf("new thread after midnight date block = %q, want %q", got, want)
	}
}

// A first user message with no text gives the affinity resolver nothing to
// derive a thread identifier from, so this request has no thread.
const claudeThreadDateNoThreadPayload = `{"model":"claude-opus-4-6","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}]}`

func TestClaudeCloakedDateBlockWithoutThreadFollowsClock(t *testing.T) {
	h := newClaudeThreadDateHarness(t, nil)

	if got, want := h.send(time.Date(2025, 5, 1, 23, 50, 0, 0, time.UTC), "", claudeThreadDateNoThreadPayload), dateBlockFor("2025-05-01"); got != want {
		t.Fatalf("before midnight date block = %q, want %q", got, want)
	}
	if got, want := h.send(time.Date(2025, 5, 2, 0, 10, 0, 0, time.UTC), "", claudeThreadDateNoThreadPayload), dateBlockFor("2025-05-02"); got != want {
		t.Fatalf("after midnight date block without a thread = %q, want current %q", got, want)
	}
}

func TestClaudeCloakedDateBlockForgottenAfterBindingLifetimeIdle(t *testing.T) {
	h := newClaudeThreadDateHarness(t, nil)
	thread := "thread-date-idle-" + t.Name()

	h.send(time.Date(2025, 6, 1, 23, 50, 0, 0, time.UTC), thread, claudeThreadDateTextPayload)
	// Each request slides the pin: 01:05 is 75 min after the first request but
	// only 55 min after the previous one.
	h.send(time.Date(2025, 6, 2, 0, 10, 0, 0, time.UTC), thread, claudeThreadDateTextPayload)
	if got, want := h.send(time.Date(2025, 6, 2, 1, 5, 0, 0, time.UTC), thread, claudeThreadDateTextPayload), dateBlockFor("2025-06-01"); got != want {
		t.Fatalf("active thread date block = %q, want pinned %q", got, want)
	}
	// 61 min idle exceeds the 1h binding lifetime.
	if got, want := h.send(time.Date(2025, 6, 2, 2, 6, 0, 0, time.UTC), thread, claudeThreadDateTextPayload), dateBlockFor("2025-06-02"); got != want {
		t.Fatalf("idle thread date block = %q, want current %q", got, want)
	}
}

func TestClaudeCloakedDateBlockLifetimeFollowsSessionAffinityTTL(t *testing.T) {
	cfg := &config.Config{}
	cfg.Routing.SessionAffinityTTL = "3h"
	h := newClaudeThreadDateHarness(t, cfg)
	thread := "thread-date-ttl-" + t.Name()

	h.send(time.Date(2025, 7, 1, 23, 50, 0, 0, time.UTC), thread, claudeThreadDateTextPayload)
	if got, want := h.send(time.Date(2025, 7, 2, 2, 0, 0, 0, time.UTC), thread, claudeThreadDateTextPayload), dateBlockFor("2025-07-01"); got != want {
		t.Fatalf("date block after 130 min idle with a 3h binding lifetime = %q, want pinned %q", got, want)
	}
}

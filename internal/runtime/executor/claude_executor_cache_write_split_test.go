package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// claudeCacheWriteSplitUsage reports a cache write of 1500 tokens: 500 with the
// 5-minute TTL and 1000 with the 1-hour TTL.
const claudeCacheWriteSplitUsage = `{"input_tokens":10,"cache_creation_input_tokens":1500,"cache_read_input_tokens":2000,` +
	`"cache_creation":{"ephemeral_5m_input_tokens":500,"ephemeral_1h_input_tokens":1000},"output_tokens":20}`

// claudeCacheWriteTotalOnlyUsage reports a cache write without the TTL split.
const claudeCacheWriteTotalOnlyUsage = `{"input_tokens":10,"cache_creation_input_tokens":1500,"cache_read_input_tokens":2000,"output_tokens":20}`

func captureClaudeUsageRecord(t *testing.T, name string) *captureClaudeUsagePlugin {
	t.Helper()
	plugin := &captureClaudeUsagePlugin{records: make(chan usage.Record, 4)}
	usage.RegisterNamedPlugin(name, plugin)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(name, noopClaudeUsagePlugin{})
	})
	return plugin
}

func waitClaudeUsageRecord(t *testing.T, plugin *captureClaudeUsagePlugin) usage.Record {
	t.Helper()
	select {
	case record := <-plugin.records:
		return record
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for usage record")
		return usage.Record{}
	}
}

func claudeCacheWriteSplitAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": baseURL,
	}}
}

func executeClaudeNonStreamWithUsage(t *testing.T, pluginName, usageJSON string) usage.Record {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5",` +
			`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":` + usageJSON + `}`))
	}))
	defer server.Close()

	plugin := captureClaudeUsageRecord(t, pluginName)
	exec := NewClaudeExecutor(&config.Config{})
	_, err := exec.Execute(context.Background(), claudeCacheWriteSplitAuth(server.URL), cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	return waitClaudeUsageRecord(t, plugin)
}

func executeClaudeStreamWithUsage(t *testing.T, pluginName, usageJSON string) usage.Record {
	t.Helper()
	streamData := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-opus-5","stop_reason":null,"usage":` + usageJSON + `}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(streamData))
	}))
	defer server.Close()

	plugin := captureClaudeUsageRecord(t, pluginName)
	exec := NewClaudeExecutor(&config.Config{})
	result, err := exec.ExecuteStream(context.Background(), claudeCacheWriteSplitAuth(server.URL), cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
	}
	return waitClaudeUsageRecord(t, plugin)
}

func assertClaudeCacheWriteDetail(t *testing.T, got usage.Detail, want5m, want1h int64) {
	t.Helper()
	if got.CacheCreation5mTokens != want5m {
		t.Errorf("CacheCreation5mTokens = %d, want %d", got.CacheCreation5mTokens, want5m)
	}
	if got.CacheCreation1hTokens != want1h {
		t.Errorf("CacheCreation1hTokens = %d, want %d", got.CacheCreation1hTokens, want1h)
	}
	// The cache-write total and the other counters do not depend on the split.
	if got.CacheCreationTokens != 1500 {
		t.Errorf("CacheCreationTokens = %d, want 1500", got.CacheCreationTokens)
	}
	if got.CacheReadTokens != 2000 {
		t.Errorf("CacheReadTokens = %d, want 2000", got.CacheReadTokens)
	}
	if got.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10", got.InputTokens)
	}
	if got.OutputTokens != 20 {
		t.Errorf("OutputTokens = %d, want 20", got.OutputTokens)
	}
	if got.TotalTokens != 3530 {
		t.Errorf("TotalTokens = %d, want 3530", got.TotalTokens)
	}
}

func TestClaudeExecutor_CacheWriteSplitLandsInUsageRecord(t *testing.T) {
	record := executeClaudeNonStreamWithUsage(t, "test-claude-cache-write-split-execute", claudeCacheWriteSplitUsage)
	assertClaudeCacheWriteDetail(t, record.Detail, 500, 1000)
}

func TestClaudeExecutor_CacheWriteWithoutSplitRecordsZeros(t *testing.T) {
	record := executeClaudeNonStreamWithUsage(t, "test-claude-cache-write-total-only-execute", claudeCacheWriteTotalOnlyUsage)
	assertClaudeCacheWriteDetail(t, record.Detail, 0, 0)
}

func TestClaudeExecutor_ExecuteStream_CacheWriteSplitLandsInUsageRecord(t *testing.T) {
	record := executeClaudeStreamWithUsage(t, "test-claude-cache-write-split-stream", claudeCacheWriteSplitUsage)
	assertClaudeCacheWriteDetail(t, record.Detail, 500, 1000)
}

func TestClaudeExecutor_ExecuteStream_CacheWriteWithoutSplitRecordsZeros(t *testing.T) {
	record := executeClaudeStreamWithUsage(t, "test-claude-cache-write-total-only-stream", claudeCacheWriteTotalOnlyUsage)
	assertClaudeCacheWriteDetail(t, record.Detail, 0, 0)
}

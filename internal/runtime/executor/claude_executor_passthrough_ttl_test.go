package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const claudePassthroughTTLMainBetas = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05"

// claudePassthroughTTLPayload builds a native Claude Code Messages body whose
// system and last user blocks carry the given cache_control objects ("" means
// no marker on that block).
func claudePassthroughTTLPayload(systemMarker, messageMarker, extra string) []byte {
	block := func(text, marker string) string {
		if marker == "" {
			return `{"type":"text","text":"` + text + `"}`
		}
		return `{"type":"text","text":"` + text + `","cache_control":` + marker + `}`
	}
	userID := strings.ReplaceAll(claudeNativeHelperUserID, `"`, `\"`)
	return []byte(`{"model":"claude-opus-4-6","max_tokens":32000,` +
		`"system":[` + block("You are Claude Code, Anthropic's official CLI for Claude.", systemMarker) + `],` +
		`"messages":[{"role":"user","content":[` + block("fix the failing test", messageMarker) + `]}],` +
		`"metadata":{"user_id":"` + userID + `"}` + extra + `}`)
}

// claudePassthroughTTLProbePayload builds a native quota probe (max_tokens 1,
// one "quota" user message) whose blocks carry the given cache_control object.
func claudePassthroughTTLProbePayload(marker string) []byte {
	payload := string(claudePassthroughTTLPayload(marker, marker, ""))
	payload = strings.Replace(payload, `"max_tokens":32000`, `"max_tokens":1`, 1)
	return []byte(strings.Replace(payload, "fix the failing test", "quota", 1))
}

func claudePassthroughTTLAuth(baseURL, apiKey string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID: "passthrough-ttl-" + apiKey,
		Attributes: map[string]string{
			"api_key":  apiKey,
			"base_url": baseURL,
		},
		Metadata: claudeOAuthTestMetadata(),
	}
}

// claudeUpstreamCacheTTLs lists the cache_control ttl of every marker in
// evaluation order (tools, system, messages); a marker without ttl is "".
func claudeUpstreamCacheTTLs(body []byte) []string {
	var ttls []string
	forEachClaudeCacheControlBlock(body, func(_ string, block gjson.Result) {
		if cc := block.Get("cache_control"); cc.Exists() {
			ttls = append(ttls, cc.Get("ttl").String())
		}
	})
	return ttls
}

// sendClaudePassthroughRequest sends one request through the Claude executor to
// an httptest upstream and returns the upstream body and headers.
func sendClaudePassthroughRequest(t *testing.T, stream bool, apiKey string, headers http.Header, payload []byte) ([]byte, http.Header) {
	t.Helper()
	var seenBody []byte
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		seenHeaders = r.Header.Clone()
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-4-6\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := claudePassthroughTTLAuth(server.URL, apiKey)
	req := cliproxyexecutor.Request{Model: "claude-opus-4-6", Payload: payload}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers:         headers,
	}
	if stream {
		result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
		if errStream != nil {
			t.Fatalf("ExecuteStream() error = %v", errStream)
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatalf("stream chunk error = %v", chunk.Err)
			}
		}
	} else if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if seenBody == nil {
		t.Fatal("upstream received no request")
	}
	return seenBody, seenHeaders
}

func TestClaudeExecutor_PassthroughCacheTTLSafetyNet(t *testing.T) {
	const (
		oauthKey  = "sk-ant-oat-passthrough-ttl"
		apiKey    = "sk-ant-api03-passthrough-ttl"
		bare      = `{"type":"ephemeral"}`
		explicit5 = `{"type":"ephemeral","ttl":"5m"}`
	)
	subagentHeaders := claudeNativeHelperHeaders(claudePassthroughTTLMainBetas, "gzip, deflate, br, zstd")
	subagentHeaders.Set("X-Claude-Code-Agent-Id", "agent-sub-passthrough")

	tests := []struct {
		name     string
		apiKey   string
		headers  http.Header
		payload  []byte
		wantTTLs []string
		want1h   bool
	}{
		{
			name:     "main thread markers without ttl are upgraded to 1h",
			apiKey:   oauthKey,
			payload:  claudePassthroughTTLPayload(bare, bare, ""),
			wantTTLs: []string{"1h", "1h"},
			want1h:   true,
		},
		{
			name:     "explicit client ttl is never changed",
			apiKey:   oauthKey,
			payload:  claudePassthroughTTLPayload(bare, explicit5, ""),
			wantTTLs: []string{"1h", "5m"},
			want1h:   true,
		},
		{
			name:     "ttl ordering normalization still runs after the upgrade",
			apiKey:   oauthKey,
			payload:  claudePassthroughTTLPayload(explicit5, bare, ""),
			wantTTLs: []string{"5m", ""},
		},
		{
			name:     "subagent keeps the client's choice",
			apiKey:   oauthKey,
			headers:  subagentHeaders,
			payload:  claudePassthroughTTLPayload(bare, bare, ""),
			wantTTLs: []string{"", ""},
		},
		{
			name:     "probe is sent as the client wrote it",
			apiKey:   oauthKey,
			payload:  claudePassthroughTTLProbePayload(bare),
			wantTTLs: []string{"", ""},
		},
		{
			name:     "title helper is sent as the client wrote it",
			apiKey:   oauthKey,
			payload:  []byte(strings.Replace(string(claudePassthroughTTLPayload(bare, bare, "")), "You are Claude Code, Anthropic's official CLI for Claude.", "Return a short title.", 1)),
			wantTTLs: []string{"", ""},
		},
		{
			name:     "API key credential is not upgraded",
			apiKey:   apiKey,
			payload:  claudePassthroughTTLPayload(bare, bare, ""),
			wantTTLs: []string{"", ""},
		},
	}

	for _, tt := range tests {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tt.name, stream), func(t *testing.T) {
				headers := tt.headers
				if headers == nil {
					headers = claudeNativeHelperHeaders(claudePassthroughTTLMainBetas, "gzip, deflate, br, zstd")
				}
				headers = headers.Clone()
				payload := tt.payload
				if stream {
					payload = []byte(strings.Replace(string(payload), `"max_tokens":`, `"stream":true,"max_tokens":`, 1))
				}
				if !helps.DetectClaudeCodeRequest(headers, payload, false).Confirmed {
					t.Fatal("test request is not confirmed native Claude Code; it would take the cloaked path")
				}

				body, upstreamHeaders := sendClaudePassthroughRequest(t, stream, tt.apiKey, headers, payload)

				got := claudeUpstreamCacheTTLs(body)
				if strings.Join(got, ",") != strings.Join(tt.wantTTLs, ",") {
					t.Fatalf("upstream cache ttls = %q, want %q; body=%s", got, tt.wantTTLs, body)
				}
				if tt.want1h {
					betas := helps.HeaderValueCaseInsensitive(upstreamHeaders, "Anthropic-Beta")
					if !strings.Contains(betas, claudeExtendedCacheTTLBeta) {
						t.Fatalf("Anthropic-Beta = %q, want %s alongside the 1h body ttl", betas, claudeExtendedCacheTTLBeta)
					}
				}
			})
		}
	}
}

package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// upstreamTransportRecorder answers every request with 500 and records whether
// the first request tried a WebSocket upgrade.
type upstreamTransportRecorder struct {
	requests  atomic.Int32
	upgrades  atomic.Int32
	server    *httptest.Server
	firstSeen atomic.Value
}

func newUpstreamTransportRecorder(t *testing.T) *upstreamTransportRecorder {
	t.Helper()
	rec := &upstreamTransportRecorder{}
	rec.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isUpgrade := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
		if rec.requests.Add(1) == 1 {
			if isUpgrade {
				rec.firstSeen.Store("websocket")
			} else {
				rec.firstSeen.Store("http")
			}
		}
		if isUpgrade {
			rec.upgrades.Add(1)
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"test upstream"}}`))
	}))
	t.Cleanup(rec.server.Close)
	return rec
}

func (r *upstreamTransportRecorder) firstTransport() string {
	value, _ := r.firstSeen.Load().(string)
	return value
}

func TestCodexAutoExecutorUpstreamTransportForDownstreamWebsocket(t *testing.T) {
	tests := []struct {
		name      string
		auth      func(baseURL string) *cliproxyauth.Auth
		wantFirst string
	}{
		{
			name: "codex credential without flag uses websocket",
			auth: func(baseURL string) *cliproxyauth.Auth {
				return &cliproxyauth.Auth{
					ID:         "codex-default.json",
					Provider:   "codex",
					Attributes: map[string]string{"base_url": baseURL},
					Metadata:   map[string]any{"type": "codex", "access_token": "tok", "email": "a@example.com"},
				}
			},
			wantFirst: "websocket",
		},
		{
			name: "codex credential file with websockets false uses http",
			auth: func(baseURL string) *cliproxyauth.Auth {
				return &cliproxyauth.Auth{
					ID:         "codex-off.json",
					Provider:   "codex",
					Attributes: map[string]string{"base_url": baseURL},
					Metadata:   map[string]any{"type": "codex", "access_token": "tok", "email": "b@example.com", "websockets": false},
				}
			},
			wantFirst: "http",
		},
		{
			name: "codex api key without flag stays on http",
			auth: func(baseURL string) *cliproxyauth.Auth {
				return &cliproxyauth.Auth{
					ID:         "codex-apikey",
					Provider:   "codex",
					Attributes: map[string]string{"base_url": baseURL, "api_key": "sk-test"},
				}
			},
			wantFirst: "http",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := newUpstreamTransportRecorder(t)
			exec := NewCodexAutoExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
			ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
			_, _ = exec.Execute(ctx, tc.auth(rec.server.URL), cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
			if got := rec.firstTransport(); got != tc.wantFirst {
				t.Fatalf("first upstream transport = %q, want %q", got, tc.wantFirst)
			}
		})
	}
}

func TestXAIAutoExecutorCredentialWithoutFlagStaysOnHTTP(t *testing.T) {
	rec := newUpstreamTransportRecorder(t)
	exec := NewXAIAutoExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:         "xai-default.json",
		Provider:   "xai",
		Attributes: map[string]string{"base_url": rec.server.URL},
		Metadata:   map[string]any{"type": "xai", "access_token": "tok", "email": "c@example.com"},
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	_, _ = exec.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "grok-4",
		Payload: []byte(`{"model":"grok-4","input":"hello"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")})
	if got := rec.firstTransport(); got != "http" {
		t.Fatalf("first upstream transport = %q, want %q", got, "http")
	}
	if got := rec.upgrades.Load(); got != 0 {
		t.Fatalf("websocket upgrade attempts = %d, want 0", got)
	}
}

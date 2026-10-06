package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

func TestManagerDownstreamWebsocketRoutesToCodexCredentialWithoutFlag(t *testing.T) {
	ctx := context.Background()
	const model = "codex-ws-default-model"
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	for _, credential := range []*Auth{
		{ID: "codex-ws-off.json", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"type": "codex", "access_token": "tok-a", "websockets": false}},
		{ID: "codex-new-login.json", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"type": "codex", "access_token": "tok-b"}},
	} {
		registry.GetGlobalRegistry().RegisterClient(credential.ID, "codex", []*registry.ModelInfo{{ID: model}})
		id := credential.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, errRegister := manager.Register(ctx, credential); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	var served []string
	manager.RegisterExecutor(&mockCustomErrorExecutor{
		identifier: "codex",
		executeFn: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			served = append(served, auth.ID)
			return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
		},
	})

	wsCtx := cliproxyexecutor.WithDownstreamWebsocket(ctx)
	for i := 0; i < 4; i++ {
		if _, errExecute := manager.Execute(wsCtx, []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute != nil {
			t.Fatalf("Execute #%d error = %v", i, errExecute)
		}
	}
	for i, id := range served {
		if id != "codex-new-login.json" {
			t.Fatalf("request #%d served by %q, want codex-new-login.json (served=%v)", i, id, served)
		}
	}
}

func TestManagerLoadWarnsOncePerCodexCredentialWithWebsocketsOff(t *testing.T) {
	hook := setupTestLoggerHook(t)
	ctx := context.Background()
	store := newMemoryAuthTestStore()
	for _, credential := range []*Auth{
		{ID: "codex-default.json", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"type": "codex", "access_token": "tok-a"}},
		{ID: "codex-off.json", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"type": "codex", "access_token": "tok-b", "websockets": false}},
		{ID: "codex-off-string.json", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"type": "codex", "access_token": "tok-c", "websockets": "false"}},
		{ID: "codex-on.json", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"type": "codex", "access_token": "tok-d", "websockets": true}},
		{ID: "xai-default.json", Provider: "xai", Status: StatusActive, Metadata: map[string]any{"type": "xai", "access_token": "tok-e"}},
	} {
		if _, errSave := store.Save(ctx, credential); errSave != nil {
			t.Fatal(errSave)
		}
	}
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	if errLoad := manager.Load(ctx); errLoad != nil {
		t.Fatalf("Load() error = %v", errLoad)
	}

	warned := map[string]int{}
	for _, entry := range hook.AllEntries() {
		if entry.Level != log.WarnLevel || !strings.Contains(strings.ToLower(entry.Message), "websocket") {
			continue
		}
		id, _ := entry.Data["auth_id"].(string)
		warned[id]++
	}
	want := map[string]int{"codex-off.json": 1, "codex-off-string.json": 1}
	if len(warned) != len(want) {
		t.Fatalf("websocket warnings = %v, want %v", warned, want)
	}
	for id, count := range want {
		if warned[id] != count {
			t.Fatalf("websocket warnings = %v, want %v", warned, want)
		}
	}
}

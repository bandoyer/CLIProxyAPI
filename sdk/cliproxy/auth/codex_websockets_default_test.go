package auth

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
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

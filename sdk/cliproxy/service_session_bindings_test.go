package cliproxy

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestServiceRestoresBindingsAtStartupAndSavesThemOnShutdown(t *testing.T) {
	ctx := context.Background()
	authDir := t.TempDir()
	cfg := &config.Config{AuthDir: authDir}
	cfg.Routing.SessionAffinity = true

	manager := coreauth.NewManager(nil, newRoutingSelector(normalizedRoutingRuntimeState(cfg), nil), nil)
	if _, errRegister := manager.Register(coreauth.WithSkipPersist(ctx), &coreauth.Auth{ID: "codex-a", Provider: "codex", Status: coreauth.StatusActive}); errRegister != nil {
		t.Fatal(errRegister)
	}
	store := coreauth.NewFileSessionBindingStore(authDir)
	saved := []coreauth.SessionBindingRecord{{
		AuthID:    "codex-a",
		ExpiresAt: time.Now().Add(30 * time.Minute),
		Keys:      []string{"codex::header:thread-1::gpt-5"},
	}}
	if errSave := store.Save(ctx, saved); errSave != nil {
		t.Fatal(errSave)
	}

	service := &Service{cfg: cfg, coreManager: manager}
	service.restoreSessionBindings(ctx)
	if errShutdown := service.Shutdown(ctx); errShutdown != nil {
		t.Fatalf("shutdown: %v", errShutdown)
	}

	got, errLoad := store.Load(ctx)
	if errLoad != nil {
		t.Fatalf("load bindings written on shutdown: %v", errLoad)
	}
	if len(got) != 1 || got[0].AuthID != "codex-a" || len(got[0].Keys) != 1 || got[0].Keys[0] != saved[0].Keys[0] {
		t.Fatalf("bindings written on shutdown = %+v, want the restored binding", got)
	}
}

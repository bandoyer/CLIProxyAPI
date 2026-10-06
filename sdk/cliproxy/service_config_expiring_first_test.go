package cliproxy

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotareading"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const expiringFirstConfigTestModel = "expiring-first-config-model"

// credentialEchoExecutor answers every request with the credential ID.
type credentialEchoExecutor struct{ provider string }

func (e credentialEchoExecutor) Identifier() string { return e.provider }

func (credentialEchoExecutor) Execute(_ context.Context, auth *coreauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (credentialEchoExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (credentialEchoExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (credentialEchoExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (credentialEchoExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestExpiringFirstStrategyIsAcceptedByConfigAndHotReload(t *testing.T) {
	for _, tc := range []struct {
		name            string
		sessionAffinity bool
	}{
		{name: "wrapped by session affinity", sessionAffinity: true},
		{name: "on its own", sessionAffinity: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(credentialEchoExecutor{provider: "claude"})
			lessUrgent, moreUrgent := "ef-config-1-"+t.Name(), "ef-config-2-"+t.Name()
			for _, id := range []string{lessUrgent, moreUrgent} {
				authID := id
				registry.GetGlobalRegistry().RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: expiringFirstConfigTestModel}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
				if _, errRegister := manager.Register(coreauth.WithSkipPersist(ctx), &coreauth.Auth{ID: authID, Provider: "claude", Status: coreauth.StatusActive}); errRegister != nil {
					t.Fatal(errRegister)
				}
			}
			now := time.Now()
			for id, reading := range map[string]struct {
				shareLeft float64
				resetIn   time.Duration
			}{
				lessUrgent: {shareLeft: 0.90, resetIn: 4 * 24 * time.Hour},
				moreUrgent: {shareLeft: 0.40, resetIn: 3 * time.Hour},
			} {
				manager.QuotaReadings().Record(id, quotareading.Reading{
					Window: quotareading.ClaudeSevenDayWindow, Kind: quotareading.KindRanking, Length: 7 * 24 * time.Hour,
					ShareLeft: reading.shareLeft, ResetAt: now.Add(reading.resetIn), LearnedAt: now, Source: quotareading.SourceHeader,
				})
			}

			service := &Service{coreManager: manager}
			cfg := &internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: "expiring-first", SessionAffinity: tc.sessionAffinity}}
			if !service.applyManagerConfig(ctx, configCommit{cfg: cfg, sequence: 1}) {
				t.Fatal("applyManagerConfig rejected routing.strategy: expiring-first")
			}

			opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"config-thread"}}, Metadata: map[string]any{}}
			response, errExecute := manager.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: expiringFirstConfigTestModel}, opts)
			if errExecute != nil {
				t.Fatalf("execute: %v", errExecute)
			}
			if got := string(response.Payload); got != moreUrgent {
				t.Fatalf("credential = %s, want the more urgent %s", got, moreUrgent)
			}
		})
	}
}

func TestExpiringFirstStrategyNameIsNormalized(t *testing.T) {
	for _, raw := range []string{"expiring-first", " Expiring-First ", "expiringfirst", "ef"} {
		state := normalizedRoutingRuntimeState(&internalconfig.Config{Routing: internalconfig.RoutingConfig{Strategy: raw}})
		if state.strategy != "expiring-first" {
			t.Fatalf("strategy %q normalized to %q, want expiring-first", raw, state.strategy)
		}
	}
}

package usagepoll_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotareading"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagepoll"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// xaiCreditsBody is a /v1/billing?format=credits body for a free account:
// a weekly credit period with 40% used.
const xaiCreditsBody = `{"config": {
	"currentPeriod": {"type": "USAGE_PERIOD_TYPE_WEEKLY", "start": "2026-10-03T00:00:00+00:00", "end": "2026-10-10T00:00:00+00:00"},
	"creditUsagePercent": 40,
	"billingPeriodStart": "2026-10-03T00:00:00+00:00",
	"billingPeriodEnd": "2026-10-10T00:00:00+00:00",
	"onDemandCap": {"val": 0}
}}`

// xaiCall is one request the fake xAI upstream received.
type xaiCall struct {
	Method        string
	Path          string
	Query         string
	Authorization string
	TokenAuth     string
	ClientVersion string
}

// xaiUpstream is an httptest stand-in for cli-chat-proxy.grok.com/v1. It
// answers GET /v1/billing with xaiCreditsBody unless a failure is set for the
// calling token. Every other path fails, and the tests check that none is
// called.
type xaiUpstream struct {
	server *httptest.Server

	mu       sync.Mutex
	calls    []xaiCall
	failures map[string]func(http.ResponseWriter)
}

func newXAIUpstream(t *testing.T) *xaiUpstream {
	t.Helper()
	upstream := &xaiUpstream{failures: make(map[string]func(http.ResponseWriter))}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := xaiCall{
			Method:        r.Method,
			Path:          r.URL.Path,
			Query:         r.URL.RawQuery,
			Authorization: r.Header.Get("Authorization"),
			TokenAuth:     r.Header.Get("X-Xai-Token-Auth"),
			ClientVersion: r.Header.Get("X-Grok-Client-Version"),
		}
		upstream.mu.Lock()
		upstream.calls = append(upstream.calls, call)
		fail := upstream.failures[call.Authorization]
		upstream.mu.Unlock()
		if r.Method != http.MethodGet || r.URL.Path != "/v1/billing" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if fail != nil {
			fail(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(xaiCreditsBody))
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

func (u *xaiUpstream) failFor(token string, respond func(http.ResponseWriter)) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.failures["Bearer "+token] = respond
}

// takeCalls returns the calls received since the last takeCalls.
func (u *xaiUpstream) takeCalls() []xaiCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	calls := u.calls
	u.calls = nil
	return calls
}

type xaiPollHarness struct {
	clock    *fakeClock
	upstream *xaiUpstream
	manager  *coreauth.Manager
	poller   *usagepoll.Poller
}

// newXAIPollHarness builds a manager with the real xAI executor, so the poll
// goes through the same token injection and HTTP client as requests, and a
// poller with an xAI fetcher pointed at an httptest upstream.
func newXAIPollHarness(t *testing.T) *xaiPollHarness {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, time.October, 6, 12, 0, 0, 0, time.UTC)}
	upstream := newXAIUpstream(t)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewXAIExecutor(&config.Config{}))
	poller := usagepoll.New(usagepoll.Config{
		Credentials: manager,
		Readings:    manager.QuotaReadings(),
		NowFunc:     clock.Now,
		Fetchers:    []usagepoll.Fetcher{usagepoll.NewXAIFetcher(manager, upstream.server.URL+"/v1")},
	})
	return &xaiPollHarness{clock: clock, upstream: upstream, manager: manager, poller: poller}
}

// xaiAccessToken builds an unsigned JWT with the given claims, the shape of
// an xAI OAuth access token.
func xaiAccessToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, errMarshal := json.Marshal(claims)
	if errMarshal != nil {
		t.Fatalf("marshal claims: %v", errMarshal)
	}
	encode := base64.RawURLEncoding.EncodeToString
	return encode([]byte(`{"alg":"none"}`)) + "." + encode(payload) + ".sig"
}

// freeXAIToken is an access token whose tier claim says the free tier.
func freeXAIToken(t *testing.T, subject string) string {
	return xaiAccessToken(t, map[string]any{"sub": subject, "tier": 0})
}

// addXAICredential registers an xAI OAuth credential with the given access
// token.
func (h *xaiPollHarness) addXAICredential(t *testing.T, id, token string, mutate ...func(*coreauth.Auth)) {
	t.Helper()
	auth := &coreauth.Auth{
		ID:       id,
		Provider: "xai",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":         "xai",
			"auth_kind":    "oauth",
			"access_token": token,
		},
	}
	for _, fn := range mutate {
		fn(auth)
	}
	if _, errRegister := h.manager.Register(coreauth.WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register(%s) error = %v", id, errRegister)
	}
}

func (h *xaiPollHarness) pollDue(t *testing.T) []xaiCall {
	t.Helper()
	h.poller.PollDue(context.Background())
	return h.upstream.takeCalls()
}

// assertBillingOnly fails unless every call is the billing read.
func assertBillingOnly(t *testing.T, calls []xaiCall) {
	t.Helper()
	for _, call := range calls {
		if call.Method != http.MethodGet || call.Path != "/v1/billing" || call.Query != "format=credits" {
			t.Fatalf("poll sent %s %s?%s, want only GET /v1/billing?format=credits (no chat-completion probe)", call.Method, call.Path, call.Query)
		}
	}
}

func TestUsagePollerPollsFreeXAICredentialsThroughBillingOnly(t *testing.T) {
	h := newXAIPollHarness(t)
	tokenA := freeXAIToken(t, "user-a")
	tokenB := freeXAIToken(t, "user-b")
	h.addXAICredential(t, "xai-a.json", tokenA)
	h.addXAICredential(t, "xai-b.json", tokenB)

	calls := h.pollDue(t)

	if len(calls) != 2 {
		t.Fatalf("startup poll made %d calls, want 2: %+v", len(calls), calls)
	}
	assertBillingOnly(t, calls)
	tokens := map[string]int{}
	for _, call := range calls {
		tokens[call.Authorization]++
		if call.TokenAuth != "xai-grok-cli" || call.ClientVersion == "" {
			t.Errorf("billing call headers token-auth=%q client-version=%q, want the Grok CLI identity", call.TokenAuth, call.ClientVersion)
		}
	}
	if tokens["Bearer "+tokenA] != 1 || tokens["Bearer "+tokenB] != 1 {
		t.Fatalf("startup poll sent tokens %v, want each credential's token once", tokens)
	}

	wantReset := time.Date(2026, time.October, 10, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"xai-a.json", "xai-b.json"} {
		reading, ok := readingByWindow(h.manager.QuotaReadings().Readings(id, ""), quotareading.XAICreditPeriodWindow)
		if !ok {
			t.Fatalf("credential %s has no %s reading after the startup poll", id, quotareading.XAICreditPeriodWindow)
		}
		if reading.Kind != quotareading.KindRanking || reading.Source != quotareading.SourcePoll || !reading.LearnedAt.Equal(h.clock.Now()) {
			t.Errorf("credential %s reading kind=%v source=%v learnedAt=%v, want a ranking poll reading at %v", id, reading.Kind, reading.Source, reading.LearnedAt, h.clock.Now())
		}
		if reading.ShareLeft < 0.59 || reading.ShareLeft > 0.61 || !reading.ResetAt.Equal(wantReset) {
			t.Errorf("credential %s share left=%v reset=%v, want 0.60 until %v", id, reading.ShareLeft, reading.ResetAt, wantReset)
		}
	}
}

func TestUsagePollerNeverPollsPaidXAIMetaDisabledOrUnknownTierCredentials(t *testing.T) {
	h := newXAIPollHarness(t)
	freeToken := freeXAIToken(t, "user-free")
	h.addXAICredential(t, "xai-free.json", freeToken)
	h.addXAICredential(t, "xai-paid-tier.json", xaiAccessToken(t, map[string]any{"sub": "user-paid", "tier": 1}))
	h.addXAICredential(t, "xai-paid-namespaced-tier.json", xaiAccessToken(t, map[string]any{"https://x.ai/tier": "2"}))
	h.addXAICredential(t, "xai-paid-pool.json", freeXAIToken(t, "user-pool"), func(a *coreauth.Auth) {
		a.Prefix = "paid"
		a.Attributes = map[string]string{"using_api": "true"}
	})
	h.addXAICredential(t, "xai-unknown-tier.json", xaiAccessToken(t, map[string]any{"sub": "user-unknown"}))
	h.addXAICredential(t, "xai-opaque-token.json", "opaque-token")
	h.addXAICredential(t, "xai-api-key", "", func(a *coreauth.Auth) {
		a.Metadata = nil
		a.Attributes = map[string]string{"api_key": freeXAIToken(t, "user-key")}
	})
	h.addXAICredential(t, "xai-disabled.json", freeXAIToken(t, "user-disabled"), func(a *coreauth.Auth) { a.Disabled = true })
	h.addXAICredential(t, "meta.json", freeXAIToken(t, "user-meta"), func(a *coreauth.Auth) { a.Provider = "meta" })

	for i := 0; i < 3; i++ {
		calls := h.pollDue(t)
		assertBillingOnly(t, calls)
		if len(calls) != 1 || calls[0].Authorization != "Bearer "+freeToken {
			t.Fatalf("poll %d made calls %+v, want only the free credential polled once", i, calls)
		}
		h.clock.Advance(usagepoll.XAIInterval)
	}
}

func TestUsagePollerPollsOnTheTimerOnlyFreeXAICredentialsWithoutAReadingNewerThanFifteenMinutes(t *testing.T) {
	h := newXAIPollHarness(t)
	busyToken := freeXAIToken(t, "user-busy")
	idleToken := freeXAIToken(t, "user-idle")
	h.addXAICredential(t, "xai-busy.json", busyToken)
	h.addXAICredential(t, "xai-idle.json", idleToken)
	h.pollDue(t) // startup poll at t0

	h.clock.Advance(10 * time.Minute)
	// Traffic at t0+10m gives the busy credential a newer reading.
	h.manager.QuotaReadings().Record("xai-busy.json", quotareading.Reading{
		Window:    quotareading.XAICreditPeriodWindow,
		Kind:      quotareading.KindRanking,
		Length:    7 * 24 * time.Hour,
		ShareLeft: 0.5,
		ResetAt:   h.clock.Now().Add(24 * time.Hour),
		LearnedAt: h.clock.Now(),
		Source:    quotareading.SourceHeader,
	})

	h.clock.Advance(4 * time.Minute) // t0+14m: every reading is newer than 15 minutes
	if calls := h.pollDue(t); len(calls) != 0 {
		t.Fatalf("poll at t0+14m made calls %+v, want none", calls)
	}

	h.clock.Advance(time.Minute) // t0+15m: only the idle credential is due
	calls := h.pollDue(t)
	assertBillingOnly(t, calls)
	if len(calls) != 1 || calls[0].Authorization != "Bearer "+idleToken {
		t.Fatalf("poll at t0+15m made calls %+v, want one billing call for the idle credential", calls)
	}

	h.clock.Advance(10 * time.Minute) // t0+25m: the busy credential has been idle for 15 minutes
	calls = h.pollDue(t)
	assertBillingOnly(t, calls)
	if len(calls) != 1 || calls[0].Authorization != "Bearer "+busyToken {
		t.Fatalf("poll at t0+25m made calls %+v, want one billing call for the busy credential", calls)
	}
}

func TestUsagePollerKeepsTheLastXAIReadingAndSendsNoChatCompletionWhenBillingFails(t *testing.T) {
	h := newXAIPollHarness(t)
	token := freeXAIToken(t, "user-a")
	h.addXAICredential(t, "xai-a.json", token)
	h.pollDue(t) // startup poll at t0 succeeds
	startup := h.clock.Now()

	failures := []struct {
		name    string
		respond func(http.ResponseWriter)
	}{
		{name: "server error", respond: func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) }},
		{name: "rate limited", respond: func(w http.ResponseWriter) { w.WriteHeader(http.StatusTooManyRequests) }},
		{name: "unauthorized", respond: func(w http.ResponseWriter) { w.WriteHeader(http.StatusUnauthorized) }},
		{name: "client too old", respond: func(w http.ResponseWriter) { w.WriteHeader(http.StatusUpgradeRequired) }},
		{name: "login page instead of JSON", respond: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>login</html>"))
		}},
	}
	for _, failure := range failures {
		h.upstream.failFor(token, failure.respond)
		h.clock.Advance(usagepoll.XAIInterval)
		calls := h.pollDue(t)
		assertBillingOnly(t, calls)
		if len(calls) != 1 {
			t.Fatalf("%s: poll made calls %+v, want one billing call and no fallback", failure.name, calls)
		}
		reading, ok := readingByWindow(h.manager.QuotaReadings().Readings("xai-a.json", ""), quotareading.XAICreditPeriodWindow)
		if !ok || !reading.LearnedAt.Equal(startup) || reading.ShareLeft < 0.59 || reading.ShareLeft > 0.61 {
			t.Fatalf("%s: reading after the failed poll = %+v (found %v), want the startup reading kept", failure.name, reading, ok)
		}
	}
}

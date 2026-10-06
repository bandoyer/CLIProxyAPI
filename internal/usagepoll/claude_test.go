package usagepoll_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotareading"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagepoll"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// claudeUsageBody is a /api/oauth/usage body for a Max plan: 20% of the
// 5-hour window and 50% of the 7-day window used, and 80% of the 7-day Opus
// window, which only this endpoint reports.
const claudeUsageBody = `{
	"five_hour": {"utilization": 20.0, "resets_at": "2026-10-06T15:00:00.000000+00:00"},
	"seven_day": {"utilization": 50.0, "resets_at": "2026-10-09T10:00:00.000000+00:00"},
	"seven_day_oauth_apps": null,
	"seven_day_opus": {"utilization": 80.0, "resets_at": "2026-10-09T10:00:00.000000+00:00"},
	"seven_day_sonnet": null,
	"extra_usage": {"is_enabled": false, "monthly_limit": null, "used_credits": null, "utilization": null}
}`

// claudeUsageCall is one request the fake Claude upstream received.
type claudeUsageCall struct {
	Path          string
	Authorization string
	Beta          string
	UserAgent     string
	// Credential is the credential whose token sent the call.
	Credential string
}

// claudeUpstream is an httptest stand-in for api.anthropic.com. It answers
// /api/oauth/usage with claudeUsageBody unless a response is queued for the
// calling credential.
type claudeUpstream struct {
	server *httptest.Server

	mu        sync.Mutex
	calls     []claudeUsageCall
	responses map[string][]func(http.ResponseWriter)
}

func newClaudeUpstream(t *testing.T) *claudeUpstream {
	t.Helper()
	upstream := &claudeUpstream{responses: make(map[string][]func(http.ResponseWriter))}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		credential := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer token-")
		upstream.mu.Lock()
		upstream.calls = append(upstream.calls, claudeUsageCall{
			Path:          r.URL.Path,
			Authorization: r.Header.Get("Authorization"),
			Beta:          r.Header.Get("Anthropic-Beta"),
			UserAgent:     r.Header.Get("User-Agent"),
			Credential:    credential,
		})
		var respond func(http.ResponseWriter)
		if queued := upstream.responses[credential]; len(queued) > 0 {
			respond, upstream.responses[credential] = queued[0], queued[1:]
		}
		upstream.mu.Unlock()
		if respond != nil {
			respond(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(claudeUsageBody))
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

// respondTo queues responses for the credential's next calls, in order.
func (u *claudeUpstream) respondTo(credential string, responses ...func(http.ResponseWriter)) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.responses[credential] = append(u.responses[credential], responses...)
}

func (u *claudeUpstream) takeCalls() []claudeUsageCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	calls := u.calls
	u.calls = nil
	return calls
}

func rateLimited(w http.ResponseWriter) { w.WriteHeader(http.StatusTooManyRequests) }

type claudePollHarness struct {
	start    time.Time
	clock    *fakeClock
	upstream *claudeUpstream
	manager  *coreauth.Manager
	poller   *usagepoll.Poller
}

// newClaudePollHarness builds a manager with the real Claude executor, so the
// poll goes through the same token injection and HTTP client as requests,
// and a poller with a Claude fetcher pointed at an httptest upstream.
func newClaudePollHarness(t *testing.T) *claudePollHarness {
	t.Helper()
	start := time.Date(2026, time.October, 6, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: start}
	upstream := newClaudeUpstream(t)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor.NewClaudeExecutor(&config.Config{}))
	poller := usagepoll.New(usagepoll.Config{
		Credentials: manager,
		Readings:    manager.QuotaReadings(),
		NowFunc:     clock.Now,
		Fetchers:    []usagepoll.Fetcher{usagepoll.NewClaudeFetcher(manager, upstream.server.URL)},
	})
	return &claudePollHarness{start: start, clock: clock, upstream: upstream, manager: manager, poller: poller}
}

// addClaudeCredential registers a Claude login credential named name whose
// access token is "token-<name>".
func (h *claudePollHarness) addClaudeCredential(t *testing.T, name string, mutate ...func(*coreauth.Auth)) {
	t.Helper()
	auth := &coreauth.Auth{
		ID:       name,
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type":         "claude",
			"access_token": "token-" + name,
			"scope":        "user:inference user:profile user:sessions:claude_code",
		},
	}
	for _, fn := range mutate {
		fn(auth)
	}
	if _, errRegister := h.manager.Register(coreauth.WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register(%s) error = %v", name, errRegister)
	}
}

// timedCall is a usage call and the clock minute it was made at.
type timedCall struct {
	Minute     int
	Credential string
}

// runMinutes runs the poller's once-a-minute check for the given minutes,
// starting with a check at the current clock time, and returns every call
// with its minute since the harness start.
func (h *claudePollHarness) runMinutes(minutes int, eachMinute func(minute int)) []timedCall {
	var calls []timedCall
	for i := 0; i < minutes; i++ {
		minute := int(h.clock.Now().Sub(h.start) / time.Minute)
		if eachMinute != nil {
			eachMinute(minute)
		}
		h.poller.PollDue(context.Background())
		for _, call := range h.upstream.takeCalls() {
			calls = append(calls, timedCall{Minute: minute, Credential: call.Credential})
		}
		h.clock.Advance(time.Minute)
	}
	return calls
}

func callMinutes(calls []timedCall, credential string) []int {
	var minutes []int
	for _, call := range calls {
		if credential == "" || call.Credential == credential {
			minutes = append(minutes, call.Minute)
		}
	}
	return minutes
}

func equalMinutes(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestClaudeUsagePollSendsClaudeCodeHeadersAndRecordsPollOnlyWindows(t *testing.T) {
	h := newClaudePollHarness(t)
	h.addClaudeCredential(t, "claude-a.json")

	h.poller.PollDue(context.Background())

	calls := h.upstream.takeCalls()
	if len(calls) != 1 {
		t.Fatalf("startup poll made %d calls, want 1: %+v", len(calls), calls)
	}
	call := calls[0]
	if call.Path != "/api/oauth/usage" || call.Authorization != "Bearer token-claude-a.json" {
		t.Errorf("poll = %s with Authorization %q, want /api/oauth/usage with the credential's bearer token", call.Path, call.Authorization)
	}
	if !strings.Contains(call.Beta, "oauth-2025-04-20") {
		t.Errorf("poll Anthropic-Beta = %q, want oauth-2025-04-20", call.Beta)
	}
	if !strings.HasPrefix(call.UserAgent, "claude-cli/") {
		t.Errorf("poll User-Agent = %q, want a Claude Code user agent", call.UserAgent)
	}

	readings := h.manager.QuotaReadings().Readings("claude-a.json", "claude-opus-4-5-20251101")
	sevenDay, okSevenDay := readingByWindow(readings, quotareading.ClaudeSevenDayWindow)
	opus, okOpus := readingByWindow(readings, "claude-opus/seven_day")
	if !okSevenDay || !okOpus {
		t.Fatalf("readings for an Opus model after the poll = %+v, want seven_day and the 7-day Opus window", readings)
	}
	if sevenDay.Source != quotareading.SourcePoll || !sevenDay.LearnedAt.Equal(h.start) || sevenDay.ShareLeft < 0.49 || sevenDay.ShareLeft > 0.51 {
		t.Errorf("seven_day reading = %+v, want 50%% left, polled at %v", sevenDay, h.start)
	}
	if opus.Kind != quotareading.KindPerModel || opus.ShareLeft < 0.19 || opus.ShareLeft > 0.21 {
		t.Errorf("7-day Opus reading = %+v, want a per-model window with 20%% left", opus)
	}
}

func TestClaudeUsagePollSpacesCallsTenMinutesApartAndPollsEachIdleCredentialEveryThirtyMinutes(t *testing.T) {
	h := newClaudePollHarness(t)
	for _, name := range []string{"claude-a.json", "claude-b.json", "claude-c.json"} {
		h.addClaudeCredential(t, name)
	}

	calls := h.runMinutes(90, nil)

	if got, want := callMinutes(calls, ""), []int{0, 10, 20, 30, 40, 50, 60, 70, 80}; !equalMinutes(got, want) {
		t.Fatalf("Claude usage calls at minutes %v, want one every 10 minutes %v", got, want)
	}
	for name, want := range map[string][]int{
		"claude-a.json": {0, 30, 60},
		"claude-b.json": {10, 40, 70},
		"claude-c.json": {20, 50, 80},
	} {
		if got := callMinutes(calls, name); !equalMinutes(got, want) {
			t.Errorf("%s polled at minutes %v, want every 30 minutes, staggered: %v", name, got, want)
		}
	}
}

func TestClaudeUsagePollSkipsCredentialsWithFreshHeaderReadings(t *testing.T) {
	h := newClaudePollHarness(t)
	h.addClaudeCredential(t, "claude-busy.json")
	h.addClaudeCredential(t, "claude-idle.json")

	calls := h.runMinutes(90, func(minute int) {
		if minute < 25 {
			return
		}
		// From minute 25 the busy credential serves traffic every minute,
		// and its response headers keep its reading fresh.
		h.manager.QuotaReadings().Record("claude-busy.json", quotareading.ParseClaudeHeaderSignals(map[string]string{
			"Anthropic-Ratelimit-Unified-5h-Utilization": "0.3",
			"Anthropic-Ratelimit-Unified-7d-Utilization": "0.4",
		}, h.clock.Now())...)
	})

	if got, want := callMinutes(calls, "claude-busy.json"), []int{0}; !equalMinutes(got, want) {
		t.Errorf("busy credential polled at minutes %v, want only the startup poll %v", got, want)
	}
	if got, want := callMinutes(calls, "claude-idle.json"), []int{10, 40, 70}; !equalMinutes(got, want) {
		t.Errorf("idle credential polled at minutes %v, want %v", got, want)
	}
}

func TestClaudeUsagePollBacksOffAllClaudePollingAfterA429(t *testing.T) {
	h := newClaudePollHarness(t)
	names := []string{"claude-a.json", "claude-b.json", "claude-c.json", "claude-d.json", "claude-e.json", "claude-f.json", "claude-g.json", "claude-h.json"}
	for _, name := range names {
		h.addClaudeCredential(t, name)
	}
	// The first six calls are rate limited, the seventh succeeds, the
	// eighth is rate limited again.
	for _, name := range names[:6] {
		h.upstream.respondTo(name, rateLimited)
	}
	h.upstream.respondTo("claude-h.json", rateLimited)

	calls := h.runMinutes(240, nil)

	// After each 429 the backoff is 5, 10, 20, 40, then capped at 60 minutes,
	// never shorter than the 10-minute gap. The success at minute 210 resets
	// it, so the call after the next 429 waits the base backoff again.
	got := callMinutes(calls, "")
	if want := []int{0, 10, 20, 40, 80, 140, 200, 210, 220}; len(got) < len(want) || !equalMinutes(got[:len(want)], want) {
		t.Fatalf("Claude usage calls at minutes %v, want %v first", got, want)
	}
	if calls[6].Credential != "claude-g.json" || calls[7].Credential != "claude-h.json" {
		t.Fatalf("calls %+v, want the seventh call (success) from claude-g.json and the eighth (429) from claude-h.json", calls)
	}
}

func TestClaudeUsagePollWaitsForRetryAfterOnA429(t *testing.T) {
	h := newClaudePollHarness(t)
	h.addClaudeCredential(t, "claude-a.json")
	h.addClaudeCredential(t, "claude-b.json")
	h.upstream.respondTo("claude-a.json", func(w http.ResponseWriter) {
		w.Header().Set("Retry-After", "1500")
		rateLimited(w)
	})

	calls := h.runMinutes(30, nil)

	if got, want := callMinutes(calls, ""), []int{0, 25}; !equalMinutes(got, want) {
		t.Fatalf("Claude usage calls at minutes %v, want %v (Retry-After 25 minutes is longer than the backoff)", got, want)
	}
}

func TestClaudeUsagePollNeverPollsAPIKeysSetupTokensOrDisabledCredentials(t *testing.T) {
	h := newClaudePollHarness(t)
	h.addClaudeCredential(t, "claude-login.json")
	h.addClaudeCredential(t, "claude-api-key", func(a *coreauth.Auth) {
		a.Attributes = map[string]string{"api_key": "sk-ant-api03-test"}
	})
	h.addClaudeCredential(t, "claude-setup-token.json", func(a *coreauth.Auth) {
		a.Metadata["scope"] = "user:inference"
	})
	h.addClaudeCredential(t, "claude-disabled.json", func(a *coreauth.Auth) { a.Disabled = true })

	calls := h.runMinutes(60, nil)

	for _, call := range calls {
		if call.Credential != "claude-login.json" {
			t.Fatalf("calls %+v, want only the enabled Claude login polled", calls)
		}
	}
	if got, want := callMinutes(calls, "claude-login.json"), []int{0, 30}; !equalMinutes(got, want) {
		t.Fatalf("login credential polled at minutes %v, want %v", got, want)
	}
}

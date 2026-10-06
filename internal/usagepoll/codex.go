package usagepoll

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotareading"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const (
	// CodexBaseURL is the ChatGPT backend that serves /wham/usage.
	CodexBaseURL = "https://chatgpt.com/backend-api"
	// CodexInterval is the per-credential Codex poll interval.
	CodexInterval = 5 * time.Minute
	// codexUsageUserAgent matches the Codex TUI, as the management panel
	// sends for the same call.
	codexUsageUserAgent = "codex-tui/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)"
)

// CodexFetcher polls GET {base}/wham/usage for Codex login credentials. Codex
// needs no provider-wide throttle.
type CodexFetcher struct {
	requester Requester
	baseURL   string
}

// NewCodexFetcher returns a Codex fetcher that sends its calls through
// requester. An empty baseURL means CodexBaseURL.
func NewCodexFetcher(requester Requester, baseURL string) *CodexFetcher {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = CodexBaseURL
	}
	return &CodexFetcher{requester: requester, baseURL: baseURL}
}

// Provider implements Fetcher.
func (f *CodexFetcher) Provider() string { return "codex" }

// Interval implements Fetcher.
func (f *CodexFetcher) Interval() time.Duration { return CodexInterval }

// Eligible implements Fetcher. Only ChatGPT logins have a usage endpoint;
// Codex API keys are never polled.
func (f *CodexFetcher) Eligible(auth *coreauth.Auth) bool {
	if auth == nil || auth.AuthKind() == coreauth.AuthKindAPIKey {
		return false
	}
	if auth.Attributes != nil && strings.TrimSpace(auth.Attributes["api_key"]) != "" {
		return false
	}
	token, _ := auth.Metadata["access_token"].(string)
	return strings.TrimSpace(token) != ""
}

// Fetch implements Fetcher. The executor injects the bearer token; the
// account header is added here because Codex PrepareRequest does not set it.
func (f *CodexFetcher) Fetch(ctx context.Context, auth *coreauth.Auth, now time.Time) ([]quotareading.Reading, error) {
	header := http.Header{}
	header.Set("Accept", "application/json")
	header.Set("User-Agent", codexUsageUserAgent)
	if accountID, _ := auth.Metadata["account_id"].(string); strings.TrimSpace(accountID) != "" {
		header.Set("Chatgpt-Account-Id", strings.TrimSpace(accountID))
	}
	body, errGet := getUsage(ctx, f.requester, auth, f.baseURL+"/wham/usage", header)
	if errGet != nil {
		return nil, errGet
	}
	return quotareading.ParseCodexUsageBody(body, now)
}

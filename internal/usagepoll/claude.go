package usagepoll

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotareading"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const (
	// ClaudeBaseURL is the Anthropic API host that serves /api/oauth/usage.
	ClaudeBaseURL = "https://api.anthropic.com"
	// ClaudeInterval is the per-credential Claude poll interval.
	ClaudeInterval = 30 * time.Minute
	// ClaudeCallGap is the least time between any two Claude usage calls,
	// across all credentials. Claude throttles this endpoint hard, and it is
	// not known whether per token or per IP.
	ClaudeCallGap = 10 * time.Minute
	// ClaudeBaseBackoff is the provider-wide wait after the first 429. Each
	// further 429 doubles it, up to ClaudeMaxBackoff. A success resets it.
	ClaudeBaseBackoff = 5 * time.Minute
	// ClaudeMaxBackoff caps the backoff, and also any Retry-After.
	ClaudeMaxBackoff = time.Hour

	// claudeUsageBeta is the beta Claude Code sends on OAuth usage calls.
	claudeUsageBeta = "oauth-2025-04-20"
	// claudeUsageUserAgent matches Claude Code, as the management panel and
	// the executor's default device profile send.
	claudeUsageUserAgent = "claude-cli/2.1.280 (external, cli)"
)

// ClaudeFetcher polls GET {base}/api/oauth/usage for Claude login
// credentials. It is also the provider's throttle: one call per
// ClaudeCallGap across all credentials, and a shared backoff after a 429.
type ClaudeFetcher struct {
	requester Requester
	baseURL   string

	mu      sync.Mutex
	next    time.Time
	backoff time.Duration
}

// NewClaudeFetcher returns a Claude fetcher that sends its calls through
// requester. An empty baseURL means ClaudeBaseURL.
func NewClaudeFetcher(requester Requester, baseURL string) *ClaudeFetcher {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = ClaudeBaseURL
	}
	return &ClaudeFetcher{requester: requester, baseURL: baseURL}
}

// Provider implements Fetcher.
func (f *ClaudeFetcher) Provider() string { return "claude" }

// Interval implements Fetcher.
func (f *ClaudeFetcher) Interval() time.Duration { return ClaudeInterval }

// Eligible implements Fetcher. Only Claude logins with the user:profile scope
// can read usage. API keys and setup tokens (inference scope only) are never
// polled, so a credential that always fails cannot hold the shared backoff.
func (f *ClaudeFetcher) Eligible(auth *coreauth.Auth) bool {
	if auth == nil || auth.AuthKind() == coreauth.AuthKindAPIKey {
		return false
	}
	if auth.Attributes != nil {
		if strings.TrimSpace(auth.Attributes["api_key"]) != "" {
			return false
		}
		if kind := strings.ToLower(auth.Attributes["auth_kind"]); kind == "setup_token" || kind == "setup-token" {
			return false
		}
	}
	token, _ := auth.Metadata["access_token"].(string)
	if strings.TrimSpace(token) == "" {
		return false
	}
	for _, key := range []string{"skip_account_profile", "is_setup_token", "setup_token"} {
		if flag, _ := auth.Metadata[key].(bool); flag {
			return false
		}
	}
	scopes := claudeScopes(auth.Metadata)
	return scopes == "" || strings.Contains(scopes, "user:profile")
}

// claudeScopes returns the credential's OAuth scopes, lowercased and
// space-separated, or "" when the file does not list them.
func claudeScopes(metadata map[string]any) string {
	for _, key := range []string{"scopes", "scope"} {
		switch value := metadata[key].(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				return strings.ToLower(value)
			}
		case []any:
			parts := make([]string, 0, len(value))
			for _, item := range value {
				parts = append(parts, fmt.Sprint(item))
			}
			if len(parts) > 0 {
				return strings.ToLower(strings.Join(parts, " "))
			}
		}
	}
	return ""
}

// Fetch implements Fetcher. The executor injects the bearer token; the beta
// and user agent are added here because Claude PrepareRequest does not set
// them.
func (f *ClaudeFetcher) Fetch(ctx context.Context, auth *coreauth.Auth, now time.Time) ([]quotareading.Reading, error) {
	header := http.Header{}
	header.Set("Accept", "application/json")
	header.Set("Content-Type", "application/json")
	header.Set("Anthropic-Beta", claudeUsageBeta)
	header.Set("User-Agent", claudeUsageUserAgent)
	body, errGet := getUsage(ctx, f.requester, auth, f.baseURL+"/api/oauth/usage", header)
	if errGet != nil {
		return nil, errGet
	}
	return quotareading.ParseClaudeUsageBody(body, now)
}

// Ready implements ProviderThrottle.
func (f *ClaudeFetcher) Ready(now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !now.Before(f.next)
}

// Observe implements ProviderThrottle. Every call closes the provider for
// ClaudeCallGap. A 429 doubles the backoff (from ClaudeBaseBackoff, up to
// ClaudeMaxBackoff) and closes the provider for the longest of the gap, the
// backoff and Retry-After (capped at ClaudeMaxBackoff). A success resets the
// backoff; other failures leave it as it is.
func (f *ClaudeFetcher) Observe(now time.Time, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	wait := ClaudeCallGap
	var statusErr *StatusError
	switch {
	case err == nil:
		f.backoff = 0
	case errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusTooManyRequests:
		if f.backoff == 0 {
			f.backoff = ClaudeBaseBackoff
		} else {
			f.backoff = min(2*f.backoff, ClaudeMaxBackoff)
		}
		wait = max(wait, f.backoff, min(statusErr.RetryAfter, ClaudeMaxBackoff))
	}
	f.next = now.Add(wait)
}

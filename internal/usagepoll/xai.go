package usagepoll

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotareading"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const (
	// XAIInterval is the per-credential poll interval for free xAI credentials.
	XAIInterval = 15 * time.Minute
	// xaiBillingPath reads the weekly credit period, the window that ranks.
	xaiBillingPath = "/billing?format=credits"
	// The Grok CLI identity the chat-proxy expects. Keep in sync with
	// xaiClientVersionValue in internal/runtime/executor/xai_executor.go: the
	// chat-proxy rejects old client versions with HTTP 426.
	xaiClientVersion    = "1.0.44"
	xaiClientIdentifier = "grok-shell"
)

// XAIFetcher polls GET {base}/billing for free xAI OAuth credentials. It
// makes one billing call per poll and never sends a chat completion, so a
// poll spends no tokens. A failed billing call is an error; the poller then
// keeps the last reading.
type XAIFetcher struct {
	requester Requester
	baseURL   string
}

// NewXAIFetcher returns an xAI fetcher that sends its calls through
// requester. An empty baseURL means the Grok CLI chat-proxy.
func NewXAIFetcher(requester Requester, baseURL string) *XAIFetcher {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = xaiauth.CLIChatProxyBaseURL
	}
	return &XAIFetcher{requester: requester, baseURL: baseURL}
}

// Provider implements Fetcher.
func (f *XAIFetcher) Provider() string { return "xai" }

// Interval implements Fetcher.
func (f *XAIFetcher) Interval() time.Duration { return XAIInterval }

// Eligible implements Fetcher. Only free xAI OAuth credentials are polled.
// The tier comes from the OAuth tokens' tier claim, as the management panel
// reads it: 0 is free, 1 or more is paid. A credential without a tier claim
// is of unknown tier and is not polled. API keys and the documented paid pool
// (using_api with prefix "paid") are not polled either.
func (f *XAIFetcher) Eligible(auth *coreauth.Auth) bool {
	if auth == nil || auth.AuthKind() == coreauth.AuthKindAPIKey {
		return false
	}
	if auth.Attributes != nil && strings.TrimSpace(auth.Attributes["api_key"]) != "" {
		return false
	}
	accessToken := xaiMetadataString(auth, "access_token")
	if accessToken == "" {
		return false
	}
	if xaiUsingAPI(auth) && strings.EqualFold(strings.TrimSpace(auth.Prefix), "paid") {
		return false
	}
	free := false
	for _, token := range []string{accessToken, xaiMetadataString(auth, "id_token")} {
		tier, ok := xaiTokenTier(token)
		if !ok {
			continue
		}
		if tier >= 1 {
			return false
		}
		free = true
	}
	return free
}

// Fetch implements Fetcher. The executor injects the bearer token; the Grok
// CLI identity headers are added here.
func (f *XAIFetcher) Fetch(ctx context.Context, auth *coreauth.Auth, now time.Time) ([]quotareading.Reading, error) {
	header := http.Header{}
	header.Set("Accept", "application/json")
	header.Set("User-Agent", "xai-grok-workspace/"+xaiClientVersion)
	header.Set("X-XAI-Token-Auth", "xai-grok-cli")
	header.Set("x-grok-client-version", xaiClientVersion)
	header.Set("x-grok-client-identifier", xaiClientIdentifier)
	if subject := xaiMetadataString(auth, "sub"); subject != "" {
		header.Set("x-userid", subject)
	}
	body, errGet := getUsage(ctx, f.requester, auth, f.baseURL+xaiBillingPath, header)
	if errGet != nil {
		return nil, errGet
	}
	return quotareading.ParseXAIBillingBody(body, now)
}

// xaiTokenTier reads the tier claim of a JWT: a claim named "tier", or one
// ending in "/tier" or ":tier", given as a number or a numeric string. The
// signature is not checked; the tier only decides whether to poll.
func xaiTokenTier(token string) (float64, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return 0, false
	}
	payload, errDecode := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if errDecode != nil {
		return 0, false
	}
	var claims map[string]any
	if errUnmarshal := json.Unmarshal(payload, &claims); errUnmarshal != nil {
		return 0, false
	}
	for key, value := range claims {
		name := strings.ToLower(key)
		if name != "tier" && !strings.HasSuffix(name, "/tier") && !strings.HasSuffix(name, ":tier") {
			continue
		}
		switch tier := value.(type) {
		case float64:
			return tier, true
		case string:
			parsed, errParse := strconv.ParseFloat(strings.TrimSpace(tier), 64)
			if errParse == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

// xaiUsingAPI reports whether the credential's using_api flag is set, in its
// attributes or its metadata.
func xaiUsingAPI(auth *coreauth.Auth) bool {
	if auth.Attributes != nil {
		if raw := strings.TrimSpace(auth.Attributes["using_api"]); raw != "" {
			parsed, _ := strconv.ParseBool(raw)
			return parsed
		}
	}
	switch value := auth.Metadata["using_api"].(type) {
	case bool:
		return value
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(value))
		return parsed
	}
	return false
}

func xaiMetadataString(auth *coreauth.Auth, key string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	value, _ := auth.Metadata[key].(string)
	return strings.TrimSpace(value)
}

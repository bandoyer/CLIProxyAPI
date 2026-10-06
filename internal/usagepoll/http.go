package usagepoll

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// maxUsageBodyBytes caps how much of a usage response is read.
const maxUsageBodyBytes = 1 << 20

// StatusError is the Fetch error for a usage call that got a non-2xx
// response. A ProviderThrottle can read StatusCode (for example 429) and
// RetryAfter to back off.
type StatusError struct {
	StatusCode int
	// RetryAfter is the parsed Retry-After header, zero when absent.
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("usage endpoint returned HTTP %d", e.StatusCode)
}

// getUsage sends GET url for the credential through requester and returns
// the body of a 2xx response. Other statuses return a *StatusError.
func getUsage(ctx context.Context, requester Requester, auth *coreauth.Auth, url string, header http.Header) ([]byte, error) {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if errReq != nil {
		return nil, fmt.Errorf("build usage request: %w", errReq)
	}
	for key, values := range header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	resp, errDo := requester.HttpRequest(ctx, auth, req)
	if errDo != nil {
		return nil, fmt.Errorf("usage request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("usage poll: close response body: %v", errClose)
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxUsageBodyBytes))
		return nil, &StatusError{StatusCode: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	}
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxUsageBodyBytes))
	if errRead != nil {
		return nil, fmt.Errorf("read usage response: %w", errRead)
	}
	return body, nil
}

// parseRetryAfter reads a Retry-After header given in seconds. Other forms
// give zero.
func parseRetryAfter(value string) time.Duration {
	seconds, errParse := strconv.Atoi(strings.TrimSpace(value))
	if errParse != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

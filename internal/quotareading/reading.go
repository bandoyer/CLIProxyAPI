// Package quotareading holds the proxy's quota readings: what it last learned
// about each credential's quota windows (share left, reset time, when it was
// learned), whatever the source. Per-provider parsers turn response-header
// signals and polled usage bodies into readings, so routing can rank
// credentials without knowing provider details.
package quotareading

import (
	"strings"
	"time"
)

// Kind tells routing how a quota window counts.
type Kind int

const (
	// KindGating is a shorter window. It only decides whether the credential
	// can be used; it never sets urgency.
	KindGating Kind = iota + 1
	// KindRanking is the credential's longest window. It sets urgency.
	KindRanking
	// KindPerModel is a window that applies only to requests for Reading.Model.
	KindPerModel
	// KindCredit is a credit balance. It never sets urgency.
	KindCredit
)

// String returns the kind's log name.
func (k Kind) String() string {
	switch k {
	case KindGating:
		return "gating"
	case KindRanking:
		return "ranking"
	case KindPerModel:
		return "per-model"
	case KindCredit:
		return "credit"
	default:
		return "unknown"
	}
}

// Source records where a reading came from. Routing treats every source the
// same; the source is kept for logs and tests.
type Source string

const (
	// SourceHeader is a reading parsed from upstream response headers.
	SourceHeader Source = "header"
	// SourcePoll is a reading parsed from a polled usage response.
	SourcePoll Source = "poll"
)

// Reading is one quota window of one credential, as last learned.
type Reading struct {
	// Window names the quota window, stable across sources for one provider
	// (for example "five_hour" or "seven_day" for Claude), so a polled reading
	// and a header reading of the same window merge.
	Window string
	// Kind tells routing how the window counts.
	Kind Kind
	// Model is the model a KindPerModel window applies to: an exact model ID or
	// a model family prefix such as "claude-opus". Empty for other kinds.
	Model string
	// Length is the window's duration, used to estimate the next reset once
	// ResetAt has passed.
	Length time.Duration
	// ShareLeft is the fraction of the window still left, from 0 (exhausted)
	// to 1 (full).
	ShareLeft float64
	// ResetAt is when the window resets. Zero means no reset is pending.
	ResetAt time.Time
	// LearnedAt is when the proxy learned this reading.
	LearnedAt time.Time
	// Source records where the reading came from.
	Source Source
}

// Exhausted reports whether the window has no share left.
func (r Reading) Exhausted() bool {
	return r.ShareLeft <= 0
}

// AppliesToModel reports whether the reading counts for a request on model.
// Only KindPerModel readings are model-specific; they apply when model equals
// Reading.Model or starts with Reading.Model followed by "-".
func (r Reading) AppliesToModel(model string) bool {
	if r.Kind != KindPerModel {
		return true
	}
	scope := strings.ToLower(strings.TrimSpace(r.Model))
	model = strings.ToLower(strings.TrimSpace(model))
	if scope == "" || model == "" {
		return false
	}
	return model == scope || strings.HasPrefix(model, scope+"-")
}

// FromHeaderSignals turns one provider's stored response-header signals into
// readings. Providers without a header parser return nil.
func FromHeaderSignals(provider string, signals map[string]string, learnedAt time.Time) []Reading {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		return ParseClaudeHeaderSignals(signals, learnedAt)
	default:
		return nil
	}
}

func clampShare(share float64) float64 {
	if share < 0 {
		return 0
	}
	if share > 1 {
		return 1
	}
	return share
}

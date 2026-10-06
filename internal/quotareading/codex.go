package quotareading

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// Codex window names match the keys of Codex's /wham/usage body
// (rate_limit.primary_window, rate_limit.secondary_window, credits), so a
// polled reading and a header reading of the same window merge. A per-model
// window is named "<model>/<window>", for example
// "gpt-5.3-codex-spark/primary_window".
const (
	CodexPrimaryWindow   = "primary_window"
	CodexSecondaryWindow = "secondary_window"
	CodexCreditsWindow   = "credits"
)

const codexHeaderPrefix = "x-codex-"

// codexWindow is one Codex rate-limit window, as the response headers, the
// WebSocket codex.rate_limits event and the /wham/usage body all report it.
type codexWindow struct {
	window      string
	usedPercent float64
	length      time.Duration
	resetAt     time.Time
}

// codexLimit is one Codex rate limit: the main limit (model empty) or an
// additional limit for one model.
type codexLimit struct {
	model   string
	windows []codexWindow
}

// codexCredits is the Codex credit balance. A nil *codexCredits means the
// source reported no credits.
type codexCredits struct {
	hasCredits bool
	unlimited  bool
	balance    float64
}

// ParseCodexHeaderSignals turns Codex's x-codex-* signals into readings. The
// signals come from HTTP response headers or from a WebSocket
// codex.rate_limits event, which the executor stores under the same header
// names. Used percentages run from 0 to 100; reset times are unix seconds,
// or seconds after learnedAt. A window without a used percentage or a window
// length gives no reading. Code-review limits give no reading: they don't
// apply to model requests.
func ParseCodexHeaderSignals(signals map[string]string, learnedAt time.Time) []Reading {
	if len(signals) == 0 {
		return nil
	}
	lower := make(map[string]string, len(signals))
	for key, value := range signals {
		key = strings.ToLower(strings.TrimSpace(key))
		if strings.HasPrefix(key, codexHeaderPrefix) {
			lower[strings.TrimPrefix(key, codexHeaderPrefix)] = strings.TrimSpace(value)
		}
	}
	if len(lower) == 0 {
		return nil
	}
	limits := []codexLimit{{windows: codexHeaderWindows(lower, "", learnedAt)}}
	// Each additional limit sits under its own namespace with a limit name:
	// a short name on HTTP (x-codex-bengalfox-*), "additional-<limit name>"
	// on the WebSocket event. The limit name is the model it applies to.
	var namespaces []string
	for key := range lower {
		if namespace, ok := strings.CutSuffix(key, "-limit-name"); ok && namespace != "" {
			namespaces = append(namespaces, namespace)
		}
	}
	sort.Strings(namespaces)
	for _, namespace := range namespaces {
		model := codexLimitModel(lower[namespace+"-limit-name"])
		if model == "" {
			continue
		}
		limits = append(limits, codexLimit{model: model, windows: codexHeaderWindows(lower, namespace+"-", learnedAt)})
	}
	return codexReadings(limits, codexHeaderCredits(lower), learnedAt, SourceHeader)
}

// codexHeaderWindows reads the primary and secondary windows under one
// header namespace ("" for the main limit, else "<namespace>-").
func codexHeaderWindows(lower map[string]string, namespace string, learnedAt time.Time) []codexWindow {
	var windows []codexWindow
	for _, slot := range []struct{ header, window string }{
		{header: "primary-", window: CodexPrimaryWindow},
		{header: "secondary-", window: CodexSecondaryWindow},
	} {
		prefix := namespace + slot.header
		used, okUsed := parseFloat(lower[prefix+"used-percent"])
		minutes, okMinutes := parseFloat(lower[prefix+"window-minutes"])
		if !okUsed || !okMinutes || minutes <= 0 {
			continue
		}
		windows = append(windows, codexWindow{
			window:      slot.window,
			usedPercent: used,
			length:      time.Duration(minutes * float64(time.Minute)),
			resetAt:     codexResetAt(lower[prefix+"reset-at"], lower[prefix+"reset-after-seconds"], learnedAt),
		})
	}
	return windows
}

func codexHeaderCredits(lower map[string]string) *codexCredits {
	hasCredits, errHas := strconv.ParseBool(lower["credits-has-credits"])
	unlimited, errUnlimited := strconv.ParseBool(lower["credits-unlimited"])
	balance, okBalance := parseFloat(lower["credits-balance"])
	if errHas != nil && errUnlimited != nil && !okBalance {
		return nil
	}
	return &codexCredits{hasCredits: errHas == nil && hasCredits, unlimited: errUnlimited == nil && unlimited, balance: balance}
}

// ParseCodexUsageBody turns a Codex GET /backend-api/wham/usage body into
// readings, with the same windows, kinds and names as
// ParseCodexHeaderSignals, so poll and header readings merge. It returns an
// error for a body that is not a JSON object; the caller then records
// nothing, which keeps the last reading.
func ParseCodexUsageBody(body []byte, learnedAt time.Time) ([]Reading, error) {
	if !gjson.ValidBytes(body) {
		return nil, errors.New("codex usage body is not valid JSON")
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return nil, errors.New("codex usage body is not a JSON object")
	}
	limits := []codexLimit{{windows: codexBodyWindows(codexBodyField(root, "rate_limit", "rateLimit"), learnedAt)}}
	codexBodyField(root, "additional_rate_limits", "additionalRateLimits").ForEach(func(_, item gjson.Result) bool {
		model := codexLimitModel(codexBodyField(item, "limit_name", "limitName").String())
		if model != "" {
			limits = append(limits, codexLimit{model: model, windows: codexBodyWindows(codexBodyField(item, "rate_limit", "rateLimit"), learnedAt)})
		}
		return true
	})
	var credits *codexCredits
	if node := codexBodyField(root, "credits"); node.IsObject() {
		credits = &codexCredits{
			hasCredits: codexBodyField(node, "has_credits", "hasCredits").Bool(),
			unlimited:  node.Get("unlimited").Bool(),
			balance:    node.Get("balance").Float(),
		}
	}
	return codexReadings(limits, credits, learnedAt, SourcePoll), nil
}

// codexBodyWindows reads the primary and secondary windows of one
// /wham/usage rate_limit object.
func codexBodyWindows(rateLimit gjson.Result, learnedAt time.Time) []codexWindow {
	if !rateLimit.IsObject() {
		return nil
	}
	var windows []codexWindow
	for _, slot := range []struct{ snake, camel, window string }{
		{snake: "primary_window", camel: "primaryWindow", window: CodexPrimaryWindow},
		{snake: "secondary_window", camel: "secondaryWindow", window: CodexSecondaryWindow},
	} {
		node := codexBodyField(rateLimit, slot.snake, slot.camel)
		used := codexBodyField(node, "used_percent", "usedPercent")
		seconds := codexBodyField(node, "limit_window_seconds", "limitWindowSeconds").Float()
		if !node.IsObject() || !used.Exists() || seconds <= 0 {
			continue
		}
		windows = append(windows, codexWindow{
			window:      slot.window,
			usedPercent: used.Float(),
			length:      time.Duration(seconds * float64(time.Second)),
			resetAt: codexResetAt(
				codexBodyField(node, "reset_at", "resetAt").String(),
				codexBodyField(node, "reset_after_seconds", "resetAfterSeconds").String(),
				learnedAt,
			),
		})
	}
	return windows
}

// codexBodyField returns the first non-null field among names (snake_case,
// then camelCase).
func codexBodyField(node gjson.Result, names ...string) gjson.Result {
	for _, name := range names {
		if value := node.Get(name); value.Exists() && value.Type != gjson.Null {
			return value
		}
	}
	return gjson.Result{}
}

// codexLimitModel turns a Codex limit name ("GPT-5.3-Codex-Spark") into the
// model ID it applies to ("gpt-5.3-codex-spark").
func codexLimitModel(limitName string) string {
	return strings.ToLower(strings.Join(strings.Fields(limitName), "-"))
}

// codexReadings turns parsed Codex limits and credits into readings. Within
// the main limit, the longest window ranks and shorter windows gate. Every
// window of an additional limit is per-model. The credit balance is a credit
// reading: share 1 while credits are left, else 0.
func codexReadings(limits []codexLimit, credits *codexCredits, learnedAt time.Time, source Source) []Reading {
	var readings []Reading
	for _, limit := range limits {
		var longest time.Duration
		for _, window := range limit.windows {
			if window.length > longest {
				longest = window.length
			}
		}
		for _, window := range limit.windows {
			reading := Reading{
				Window:    window.window,
				Kind:      KindGating,
				Length:    window.length,
				ShareLeft: clampShare(1 - window.usedPercent/100),
				ResetAt:   window.resetAt,
				LearnedAt: learnedAt,
				Source:    source,
			}
			switch {
			case limit.model != "":
				reading.Window = limit.model + "/" + window.window
				reading.Kind = KindPerModel
				reading.Model = limit.model
			case window.length == longest:
				reading.Kind = KindRanking
			}
			readings = append(readings, reading)
		}
	}
	if credits != nil {
		shareLeft := 0.0
		if credits.hasCredits || credits.unlimited || credits.balance > 0 {
			shareLeft = 1
		}
		readings = append(readings, Reading{Window: CodexCreditsWindow, Kind: KindCredit, ShareLeft: shareLeft, LearnedAt: learnedAt, Source: source})
	}
	return readings
}

// codexResetAt prefers the absolute reset time (unix seconds) and falls back
// to seconds after learnedAt.
func codexResetAt(resetAt, resetAfterSeconds string, learnedAt time.Time) time.Time {
	if at := parseUnixSeconds(resetAt); !at.IsZero() {
		return at
	}
	after, ok := parseFloat(resetAfterSeconds)
	if !ok || after < 0 {
		return time.Time{}
	}
	return learnedAt.Add(time.Duration(after * float64(time.Second)))
}

func parseFloat(raw string) (float64, bool) {
	if raw == "" {
		return 0, false
	}
	value, errParse := strconv.ParseFloat(raw, 64)
	if errParse != nil {
		return 0, false
	}
	return value, true
}

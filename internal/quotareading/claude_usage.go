package quotareading

import (
	"errors"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// ClaudeExtraUsageWindow names the extra-usage (monthly credit) balance, after
// its /api/oauth/usage body key.
const ClaudeExtraUsageWindow = "extra_usage"

const claudeWeek = 7 * 24 * time.Hour

// claudeModelWindows lists the 7-day windows that apply to one model family.
// Only the usage endpoint reports them. The window name follows the per-model
// naming of the Codex parser: "<model>/<window>". legacyKey is the body key
// that carries the window when no weekly_scoped entry in limits[] does.
var claudeModelWindows = []struct {
	family    string
	model     string
	legacyKey string
}{
	{family: "opus", model: "claude-opus", legacyKey: "seven_day_opus"},
	{family: "sonnet", model: "claude-sonnet", legacyKey: "seven_day_sonnet"},
	{family: "fable", model: "claude-fable", legacyKey: "iguana_necktie"},
}

// ParseClaudeUsageBody turns a Claude GET /api/oauth/usage body into readings.
// five_hour (gating) and seven_day (ranking) use the header parser's window
// names, so poll and header readings merge. The per-model 7-day windows
// (Opus, Sonnet, Fable) are KindPerModel readings named "<model>/seven_day".
// extra_usage is a credit reading. Utilization is a percentage (0-100);
// resets_at is an RFC 3339 time. seven_day_oauth_apps and seven_day_cowork are
// not read: they limit other Claude products, not a model this proxy serves.
// It returns an error for a body that is not a JSON object; the caller then
// records nothing, which keeps the last reading.
func ParseClaudeUsageBody(body []byte, learnedAt time.Time) ([]Reading, error) {
	if !gjson.ValidBytes(body) {
		return nil, errors.New("claude usage body is not valid JSON")
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return nil, errors.New("claude usage body is not a JSON object")
	}

	var readings []Reading
	add := func(window string, kind Kind, model string, length time.Duration, used, resetsAt gjson.Result) {
		if used.Type != gjson.Number {
			return
		}
		readings = append(readings, Reading{
			Window:    window,
			Kind:      kind,
			Model:     model,
			Length:    length,
			ShareLeft: clampShare(1 - used.Float()/100),
			ResetAt:   parseClaudeResetsAt(resetsAt),
			LearnedAt: learnedAt,
			Source:    SourcePoll,
		})
	}

	fiveHour := root.Get(ClaudeFiveHourWindow)
	add(ClaudeFiveHourWindow, KindGating, "", 5*time.Hour, fiveHour.Get("utilization"), fiveHour.Get("resets_at"))
	sevenDay := root.Get(ClaudeSevenDayWindow)
	add(ClaudeSevenDayWindow, KindRanking, "", claudeWeek, sevenDay.Get("utilization"), sevenDay.Get("resets_at"))

	for _, family := range claudeModelWindows {
		window := family.model + "/" + ClaudeSevenDayWindow
		if limit, ok := claudeScopedLimit(root.Get("limits"), family.family); ok {
			add(window, KindPerModel, family.model, claudeWeek, limit.Get("percent"), limit.Get("resets_at"))
			continue
		}
		legacy := root.Get(family.legacyKey)
		add(window, KindPerModel, family.model, claudeWeek, legacy.Get("utilization"), legacy.Get("resets_at"))
	}

	if extra := root.Get(ClaudeExtraUsageWindow); extra.IsObject() {
		readings = append(readings, Reading{
			Window:    ClaudeExtraUsageWindow,
			Kind:      KindCredit,
			ShareLeft: claudeExtraUsageShareLeft(extra),
			LearnedAt: learnedAt,
			Source:    SourcePoll,
		})
	}
	return readings, nil
}

// claudeScopedLimit finds the weekly_scoped limits[] entry with a percent for
// a model family, matched by the first word of the scope's display name
// ("Fable", "Fable 5"). An active entry wins over an inactive one.
func claudeScopedLimit(limits gjson.Result, family string) (gjson.Result, bool) {
	var found gjson.Result
	ok := false
	limits.ForEach(func(_, limit gjson.Result) bool {
		if !strings.EqualFold(strings.TrimSpace(limit.Get("kind").String()), "weekly_scoped") || limit.Get("percent").Type != gjson.Number {
			return true
		}
		words := strings.Fields(strings.ToLower(limit.Get("scope.model.display_name").String()))
		if len(words) == 0 || words[0] != family {
			return true
		}
		if !ok || limit.Get("is_active").Bool() {
			found, ok = limit, true
		}
		return !limit.Get("is_active").Bool()
	})
	return found, ok
}

// claudeExtraUsageShareLeft is the share of the monthly extra-usage limit
// left: 0 when extra usage is off, 1 when it is on with no known limit.
func claudeExtraUsageShareLeft(extra gjson.Result) float64 {
	if !extra.Get("is_enabled").Bool() {
		return 0
	}
	if utilization := extra.Get("utilization"); utilization.Type == gjson.Number {
		return clampShare(1 - utilization.Float()/100)
	}
	limit := extra.Get("monthly_limit").Float()
	if used := extra.Get("used_credits"); limit > 0 && used.Type == gjson.Number {
		return clampShare(1 - used.Float()/limit)
	}
	return 1
}

// parseClaudeResetsAt reads an RFC 3339 resets_at, or unix seconds. Null or
// unreadable gives zero (no reset pending).
func parseClaudeResetsAt(value gjson.Result) time.Time {
	switch value.Type {
	case gjson.Number:
		return parseUnixSeconds(value.Raw)
	case gjson.String:
		if at, errParse := time.Parse(time.RFC3339Nano, strings.TrimSpace(value.String())); errParse == nil {
			return at
		}
	}
	return time.Time{}
}

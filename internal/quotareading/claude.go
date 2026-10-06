package quotareading

import (
	"strconv"
	"strings"
	"time"
)

// Claude window names match the keys of Claude's /api/oauth/usage body, so a
// polled reading and a header reading of the same window merge.
const (
	ClaudeFiveHourWindow = "five_hour"
	ClaudeSevenDayWindow = "seven_day"
)

const claudeUnifiedHeaderPrefix = "anthropic-ratelimit-unified-"

// claudeHeaderWindows lists the windows Claude reports in its unified
// rate-limit headers, shortest first. The Fable 7-day window (7d_oi) has the
// same name and model as the usage body's Fable window, so the two merge.
var claudeHeaderWindows = []struct {
	headerKey string
	window    string
	kind      Kind
	model     string
	length    time.Duration
}{
	{headerKey: "5h", window: ClaudeFiveHourWindow, kind: KindGating, length: 5 * time.Hour},
	{headerKey: "7d", window: ClaudeSevenDayWindow, kind: KindRanking, length: claudeWeek},
	{headerKey: "7d_oi", window: "claude-fable/" + ClaudeSevenDayWindow, kind: KindPerModel, model: "claude-fable", length: claudeWeek},
}

// ParseClaudeHeaderSignals turns Claude's anthropic-ratelimit-unified-*
// signals into readings for the 5-hour (gating), 7-day (ranking) and Fable
// 7-day (per-model) windows. Utilization is the used fraction (0-1); reset
// times are unix seconds. A window whose status is "rejected" is exhausted. A
// window without a usable utilization and not rejected gives no reading.
func ParseClaudeHeaderSignals(signals map[string]string, learnedAt time.Time) []Reading {
	if len(signals) == 0 {
		return nil
	}
	lower := make(map[string]string, len(signals))
	for key, value := range signals {
		key = strings.ToLower(strings.TrimSpace(key))
		if strings.HasPrefix(key, claudeUnifiedHeaderPrefix) {
			lower[strings.TrimPrefix(key, claudeUnifiedHeaderPrefix)] = strings.TrimSpace(value)
		}
	}
	if len(lower) == 0 {
		return nil
	}

	var readings []Reading
	for _, window := range claudeHeaderWindows {
		shareLeft, ok := claudeShareLeft(lower[window.headerKey+"-utilization"], lower[window.headerKey+"-status"])
		if !ok {
			continue
		}
		readings = append(readings, Reading{
			Window:    window.window,
			Kind:      window.kind,
			Model:     window.model,
			Length:    window.length,
			ShareLeft: shareLeft,
			ResetAt:   parseUnixSeconds(lower[window.headerKey+"-reset"]),
			LearnedAt: learnedAt,
			Source:    SourceHeader,
		})
	}
	return readings
}

func claudeShareLeft(utilization, status string) (float64, bool) {
	if strings.EqualFold(status, "rejected") {
		return 0, true
	}
	if utilization == "" {
		return 0, false
	}
	used, errParse := strconv.ParseFloat(utilization, 64)
	if errParse != nil {
		return 0, false
	}
	return clampShare(1 - used), true
}

func parseUnixSeconds(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	seconds, errParse := strconv.ParseInt(raw, 10, 64)
	if errParse != nil || seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

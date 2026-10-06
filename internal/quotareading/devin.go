package quotareading

import (
	"strconv"
	"strings"
	"time"
)

// Devin window names.
const (
	DevinDailyWindow  = "daily"
	DevinWeeklyWindow = "weekly"
)

// devinWindows lists the windows Devin's GetUserStatus reports, shortest
// first, by their Quota.Signals key prefix.
var devinWindows = []struct {
	signalPrefix string
	window       string
	kind         Kind
	length       time.Duration
}{
	{signalPrefix: "daily_quota_", window: DevinDailyWindow, kind: KindGating, length: 24 * time.Hour},
	{signalPrefix: "weekly_quota_", window: DevinWeeklyWindow, kind: KindRanking, length: 7 * 24 * time.Hour},
}

// FromRefreshSignals turns the quota signals a provider's credential refresh
// stores (rather than its response headers) into readings. Only Devin's
// refresh fetches usage today. Providers without a refresh parser return nil.
func FromRefreshSignals(provider string, signals map[string]string, learnedAt time.Time) []Reading {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "devin":
		return ParseDevinRefreshSignals(signals, learnedAt)
	default:
		return nil
	}
}

// ParseDevinRefreshSignals turns the quota signals Devin's refresh stores
// (daily_quota_remaining_percent "NN%", daily_quota_reset_at RFC 3339, and the
// weekly_quota_* pair) into readings for the daily (gating) and weekly
// (ranking) windows. Devin's status omits unset fields, which the refresh
// writes as "0%", so a window without a reset time gives no reading.
func ParseDevinRefreshSignals(signals map[string]string, learnedAt time.Time) []Reading {
	if len(signals) == 0 {
		return nil
	}
	var readings []Reading
	for _, window := range devinWindows {
		resetAt, errReset := time.Parse(time.RFC3339, strings.TrimSpace(signals[window.signalPrefix+"reset_at"]))
		if errReset != nil || resetAt.IsZero() {
			continue
		}
		remaining := strings.TrimSuffix(strings.TrimSpace(signals[window.signalPrefix+"remaining_percent"]), "%")
		percent, errPercent := strconv.ParseFloat(strings.TrimSpace(remaining), 64)
		if errPercent != nil {
			continue
		}
		readings = append(readings, Reading{
			Window:    window.window,
			Kind:      window.kind,
			Length:    window.length,
			ShareLeft: clampShare(percent / 100),
			ResetAt:   resetAt,
			LearnedAt: learnedAt,
			Source:    SourcePoll,
		})
	}
	return readings
}

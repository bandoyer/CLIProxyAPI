package quotareading

import (
	"testing"
	"time"
)

func TestParseDevinRefreshSignals(t *testing.T) {
	learnedAt := time.Date(2026, time.October, 6, 12, 0, 0, 0, time.UTC)
	dailyReset := time.Date(2026, time.October, 7, 0, 0, 0, 0, time.UTC)
	weeklyReset := time.Date(2026, time.October, 12, 0, 0, 0, 0, time.UTC)

	daily := func(shareLeft float64) Reading {
		return Reading{Window: DevinDailyWindow, Kind: KindGating, Length: 24 * time.Hour, ShareLeft: shareLeft, ResetAt: dailyReset, LearnedAt: learnedAt, Source: SourcePoll}
	}
	weekly := func(shareLeft float64) Reading {
		return Reading{Window: DevinWeeklyWindow, Kind: KindRanking, Length: 7 * 24 * time.Hour, ShareLeft: shareLeft, ResetAt: weeklyReset, LearnedAt: learnedAt, Source: SourcePoll}
	}

	tests := []struct {
		name    string
		signals map[string]string
		want    []Reading
	}{
		{
			name: "refresh data: the daily window gates and the weekly window ranks",
			signals: map[string]string{
				"plan":                           "pro",
				"daily_quota_remaining_percent":  "80%",
				"weekly_quota_remaining_percent": "35%",
				"daily_quota_reset_at":           "2026-10-07T00:00:00Z",
				"weekly_quota_reset_at":          "2026-10-12T00:00:00Z",
				"plan_start":                     "2026-09-12T00:00:00Z",
				"plan_end":                       "2026-10-12T00:00:00Z",
			},
			want: []Reading{daily(0.80), weekly(0.35)},
		},
		{
			name: "zero percent remaining is an exhausted window",
			signals: map[string]string{
				"daily_quota_remaining_percent":  "0%",
				"daily_quota_reset_at":           "2026-10-07T00:00:00Z",
				"weekly_quota_remaining_percent": "100%",
				"weekly_quota_reset_at":          "2026-10-12T00:00:00Z",
			},
			want: []Reading{daily(0), weekly(1)},
		},
		{
			// Devin's status omits unset fields, which the refresh writes as
			// 0%, so a window without a reset time is not reported at all.
			name: "window without a reset time gives no reading",
			signals: map[string]string{
				"daily_quota_remaining_percent":  "0%",
				"weekly_quota_remaining_percent": "60%",
				"weekly_quota_reset_at":          "2026-10-12T00:00:00Z",
			},
			want: []Reading{weekly(0.60)},
		},
		{
			name: "unparseable percentage gives no reading",
			signals: map[string]string{
				"weekly_quota_remaining_percent": "plenty",
				"weekly_quota_reset_at":          "2026-10-12T00:00:00Z",
			},
			want: nil,
		},
		{
			name:    "no Devin quota signals",
			signals: map[string]string{"plan": "free"},
			want:    nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertReadings(t, ParseDevinRefreshSignals(tc.signals, learnedAt), tc.want)
		})
	}
}

func TestFromRefreshSignalsDispatchesByProvider(t *testing.T) {
	learnedAt := time.Date(2026, time.October, 6, 12, 0, 0, 0, time.UTC)
	signals := map[string]string{
		"weekly_quota_remaining_percent": "60%",
		"weekly_quota_reset_at":          "2026-10-12T00:00:00Z",
	}
	if got := FromRefreshSignals("Devin", signals, learnedAt); len(got) != 1 || got[0].Window != DevinWeeklyWindow {
		t.Fatalf("FromRefreshSignals(devin) = %+v, want one weekly reading", got)
	}
	if got := FromRefreshSignals("codex", signals, learnedAt); len(got) != 0 {
		t.Fatalf("FromRefreshSignals(codex) = %+v, want no readings for a provider without a refresh parser", got)
	}
}

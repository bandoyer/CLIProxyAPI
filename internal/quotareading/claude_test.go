package quotareading

import (
	"testing"
	"time"
)

func TestParseClaudeHeaderSignals(t *testing.T) {
	learnedAt := time.Date(2026, time.October, 6, 12, 0, 0, 0, time.UTC)
	fiveHourReset := time.Unix(1790980200, 0)
	sevenDayReset := time.Unix(1791381600, 0)

	fiveHour := func(shareLeft float64) Reading {
		return Reading{Window: ClaudeFiveHourWindow, Kind: KindGating, Length: 5 * time.Hour, ShareLeft: shareLeft, ResetAt: fiveHourReset, LearnedAt: learnedAt, Source: SourceHeader}
	}
	sevenDay := func(shareLeft float64) Reading {
		return Reading{Window: ClaudeSevenDayWindow, Kind: KindRanking, Length: 7 * 24 * time.Hour, ShareLeft: shareLeft, ResetAt: sevenDayReset, LearnedAt: learnedAt, Source: SourceHeader}
	}
	extraUsage := func(shareLeft float64) Reading {
		return Reading{Window: ClaudeExtraUsageWindow, Kind: KindCredit, ShareLeft: shareLeft, LearnedAt: learnedAt, Source: SourceHeader}
	}
	fableSevenDay := func(shareLeft float64) Reading {
		return Reading{Window: "claude-fable/seven_day", Kind: KindPerModel, Model: "claude-fable", Length: 7 * 24 * time.Hour, ShareLeft: shareLeft, ResetAt: sevenDayReset, LearnedAt: learnedAt, Source: SourceHeader}
	}

	tests := []struct {
		name    string
		signals map[string]string
		want    []Reading
	}{
		{
			name: "captured response with both windows",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-Status":                  "allowed",
				"Anthropic-Ratelimit-Unified-5h-Status":               "allowed",
				"Anthropic-Ratelimit-Unified-5h-Utilization":          "0.24",
				"Anthropic-Ratelimit-Unified-5h-Reset":                "1790980200",
				"Anthropic-Ratelimit-Unified-7d-Status":               "allowed",
				"Anthropic-Ratelimit-Unified-7d-Utilization":          "0.44",
				"Anthropic-Ratelimit-Unified-7d-Reset":                "1791381600",
				"Anthropic-Ratelimit-Unified-Representative-Claim":    "five_hour",
				"Anthropic-Ratelimit-Unified-Fallback-Percentage":     "0.5",
				"Anthropic-Ratelimit-Unified-Overage-Status":          "rejected",
				"Anthropic-Ratelimit-Unified-Overage-Disabled-Reason": "org_level_disabled",
				"Anthropic-Ratelimit-Unified-Reset":                   "1790980200",
			},
			want: []Reading{fiveHour(0.76), sevenDay(0.56), extraUsage(0)},
		},
		{
			name: "header names in any case",
			signals: map[string]string{
				"anthropic-ratelimit-unified-7d-utilization": "0.1",
				"anthropic-ratelimit-unified-7d-reset":       "1791381600",
			},
			want: []Reading{sevenDay(0.9)},
		},
		{
			name: "negative zero utilization is a full window",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Utilization": "-0.0",
				"Anthropic-Ratelimit-Unified-5h-Reset":       "1790980200",
			},
			want: []Reading{fiveHour(1)},
		},
		{
			name: "utilization above one is clamped to an exhausted window",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Utilization": "1.07",
				"Anthropic-Ratelimit-Unified-7d-Reset":       "1791381600",
			},
			want: []Reading{sevenDay(0)},
		},
		{
			name: "rejected window without utilization is exhausted",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Status": "rejected",
				"Anthropic-Ratelimit-Unified-7d-Reset":  "1791381600",
			},
			want: []Reading{sevenDay(0)},
		},
		{
			name: "rejected window wins over a stale utilization",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Status":      "rejected",
				"Anthropic-Ratelimit-Unified-5h-Utilization": "0.98",
				"Anthropic-Ratelimit-Unified-5h-Reset":       "1790980200",
			},
			want: []Reading{fiveHour(0)},
		},
		{
			name: "allowed status without utilization gives no reading",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Status": "allowed",
				"Anthropic-Ratelimit-Unified-5h-Reset":  "1790980200",
			},
			want: nil,
		},
		{
			name: "missing reset time leaves no reset pending",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Utilization": "0.5",
			},
			want: []Reading{{Window: ClaudeSevenDayWindow, Kind: KindRanking, Length: 7 * 24 * time.Hour, ShareLeft: 0.5, LearnedAt: learnedAt, Source: SourceHeader}},
		},
		{
			name: "unparseable utilization gives no reading",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Utilization": "lots",
				"Anthropic-Ratelimit-Unified-7d-Reset":       "1791381600",
			},
			want: nil,
		},
		{
			name: "unparseable reset time leaves no reset pending",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Utilization": "0.5",
				"Anthropic-Ratelimit-Unified-7d-Reset":       "soon",
			},
			want: []Reading{{Window: ClaudeSevenDayWindow, Kind: KindRanking, Length: 7 * 24 * time.Hour, ShareLeft: 0.5, LearnedAt: learnedAt, Source: SourceHeader}},
		},
		{
			name: "Fable 7-day window is a per-model reading",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-7d_oi-Status":      "allowed",
				"Anthropic-Ratelimit-Unified-7d_oi-Utilization": "0.3",
				"Anthropic-Ratelimit-Unified-7d_oi-Reset":       "1791381600",
			},
			want: []Reading{fableSevenDay(0.7)},
		},
		{
			name: "rejected Fable 7-day window is exhausted",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-Status":            "rejected",
				"Anthropic-Ratelimit-Unified-5h-Status":         "allowed",
				"Anthropic-Ratelimit-Unified-5h-Utilization":    "0.24",
				"Anthropic-Ratelimit-Unified-5h-Reset":          "1790980200",
				"Anthropic-Ratelimit-Unified-7d-Status":         "allowed",
				"Anthropic-Ratelimit-Unified-7d-Utilization":    "0.44",
				"Anthropic-Ratelimit-Unified-7d-Reset":          "1791381600",
				"Anthropic-Ratelimit-Unified-7d_oi-Status":      "rejected",
				"Anthropic-Ratelimit-Unified-7d_oi-Utilization": "1.02",
				"Anthropic-Ratelimit-Unified-7d_oi-Reset":       "1791381600",
			},
			want: []Reading{fiveHour(0.76), sevenDay(0.56), fableSevenDay(0)},
		},
		{
			name: "allowed overage is extra usage left while the five-hour window is used up",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-Status":            "allowed",
				"Anthropic-Ratelimit-Unified-5h-Status":         "rejected",
				"Anthropic-Ratelimit-Unified-5h-Reset":          "1790980200",
				"Anthropic-Ratelimit-Unified-Overage-Status":    "allowed",
				"Anthropic-Ratelimit-Unified-Overage-Something": "ignored",
			},
			want: []Reading{fiveHour(0), extraUsage(1)},
		},
		{
			name: "overage in its warning band is extra usage left",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-Overage-Status": "allowed_warning",
			},
			want: []Reading{extraUsage(1)},
		},
		{
			name: "rejected overage is no extra usage left",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-Overage-Status": "rejected",
			},
			want: []Reading{extraUsage(0)},
		},
		{
			name: "a disabled reason without a status is no extra usage left",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-Overage-Disabled-Reason": "member_zero_credit_limit",
			},
			want: []Reading{extraUsage(0)},
		},
		{
			name: "unknown overage status gives no extra usage reading",
			signals: map[string]string{
				"Anthropic-Ratelimit-Unified-Overage-Status": "paused",
			},
			want: nil,
		},
		{
			name:    "no Claude signals",
			signals: map[string]string{"Retry-After": "30"},
			want:    nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseClaudeHeaderSignals(tc.signals, learnedAt)
			assertReadings(t, got, tc.want)
		})
	}
}

func TestFromHeaderSignalsDispatchesByProvider(t *testing.T) {
	learnedAt := time.Date(2026, time.October, 6, 12, 0, 0, 0, time.UTC)
	signals := map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Utilization": "0.25",
		"Anthropic-Ratelimit-Unified-7d-Reset":       "1791381600",
	}
	if got := FromHeaderSignals(" Claude ", signals, learnedAt); len(got) != 1 || got[0].Window != ClaudeSevenDayWindow {
		t.Fatalf("FromHeaderSignals(claude) = %+v, want one seven-day reading", got)
	}
	if got := FromHeaderSignals("gemini", signals, learnedAt); len(got) != 0 {
		t.Fatalf("FromHeaderSignals(gemini) = %+v, want no readings for a provider without a parser", got)
	}
}

func assertReadings(t *testing.T, got, want []Reading) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("readings = %+v, want %+v", got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Window != w.Window || g.Kind != w.Kind || g.Model != w.Model || g.Length != w.Length ||
			!almostEqual(g.ShareLeft, w.ShareLeft) || !g.ResetAt.Equal(w.ResetAt) || !g.LearnedAt.Equal(w.LearnedAt) || g.Source != w.Source {
			t.Fatalf("reading %d = %+v, want %+v", i, g, w)
		}
	}
}

func almostEqual(a, b float64) bool {
	diff := a - b
	return diff < 1e-9 && diff > -1e-9
}

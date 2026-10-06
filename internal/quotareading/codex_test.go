package quotareading

import (
	"testing"
	"time"
)

func TestParseCodexHeaderSignals(t *testing.T) {
	learnedAt := time.Date(2026, time.October, 6, 12, 0, 0, 0, time.UTC)
	week := 7 * 24 * time.Hour

	tests := []struct {
		name    string
		signals map[string]string
		want    []Reading
	}{
		{
			name: "plus plan: the shorter window gates and the longer window ranks",
			signals: map[string]string{
				"X-Codex-Plan-Type":                     "plus",
				"X-Codex-Primary-Used-Percent":          "25",
				"X-Codex-Primary-Window-Minutes":        "300",
				"X-Codex-Primary-Reset-At":              "1791381600",
				"X-Codex-Secondary-Used-Percent":        "60",
				"X-Codex-Secondary-Window-Minutes":      "10080",
				"X-Codex-Secondary-Reset-After-Seconds": "86400",
			},
			want: []Reading{
				{Window: CodexPrimaryWindow, Kind: KindGating, Length: 5 * time.Hour, ShareLeft: 0.75, ResetAt: time.Unix(1791381600, 0), LearnedAt: learnedAt, Source: SourceHeader},
				{Window: CodexSecondaryWindow, Kind: KindRanking, Length: week, ShareLeft: 0.40, ResetAt: learnedAt.Add(24 * time.Hour), LearnedAt: learnedAt, Source: SourceHeader},
			},
		},
		{
			name: "pro plan HTTP response: one weekly window ranks, the Spark limit is per-model, credits are a balance",
			signals: map[string]string{
				"X-Codex-Plan-Type":                              "pro",
				"X-Codex-Active-Limit":                           "codex_bengalfox",
				"X-Codex-Primary-Used-Percent":                   "51",
				"X-Codex-Primary-Window-Minutes":                 "10080",
				"X-Codex-Primary-Reset-After-Seconds":            "309718",
				"X-Codex-Primary-Reset-At":                       "1787588999",
				"X-Codex-Bengalfox-Limit-Name":                   "GPT-5.3-Codex-Spark",
				"X-Codex-Bengalfox-Primary-Used-Percent":         "35",
				"X-Codex-Bengalfox-Primary-Window-Minutes":       "10080",
				"X-Codex-Bengalfox-Primary-Reset-At":             "1787590000",
				"X-Codex-Code-Review-Primary-Used-Percent":       "90",
				"X-Codex-Code-Review-Primary-Window-Minutes":     "10080",
				"X-Codex-Code-Review-Primary-Reset-At":           "1787590000",
				"X-Codex-Credits-Has-Credits":                    "False",
				"X-Codex-Credits-Unlimited":                      "False",
				"X-Codex-Bengalfox-Over-Secondary-Limit-Percent": "0",
			},
			want: []Reading{
				{Window: CodexPrimaryWindow, Kind: KindRanking, Length: week, ShareLeft: 0.49, ResetAt: time.Unix(1787588999, 0), LearnedAt: learnedAt, Source: SourceHeader},
				{Window: "gpt-5.3-codex-spark/" + CodexPrimaryWindow, Kind: KindPerModel, Model: "gpt-5.3-codex-spark", Length: week, ShareLeft: 0.65, ResetAt: time.Unix(1787590000, 0), LearnedAt: learnedAt, Source: SourceHeader},
				{Window: CodexCreditsWindow, Kind: KindCredit, ShareLeft: 0, LearnedAt: learnedAt, Source: SourceHeader},
			},
		},
		{
			name: "WebSocket codex.rate_limits event names the additional limit by its limit name",
			signals: map[string]string{
				"X-Codex-Primary-Used-Percent":                                    "2",
				"X-Codex-Primary-Window-Minutes":                                  "10080",
				"X-Codex-Primary-Reset-At":                                        "1782951970",
				"X-Codex-Additional-Gpt-5.3-Codex-Spark-Limit-Name":               "GPT-5.3-Codex-Spark",
				"X-Codex-Additional-Gpt-5.3-Codex-Spark-Primary-Used-Percent":     "100",
				"X-Codex-Additional-Gpt-5.3-Codex-Spark-Primary-Window-Minutes":   "300",
				"X-Codex-Additional-Gpt-5.3-Codex-Spark-Primary-Reset-At":         "1782900000",
				"X-Codex-Additional-Gpt-5.3-Codex-Spark-Secondary-Used-Percent":   "40",
				"X-Codex-Additional-Gpt-5.3-Codex-Spark-Secondary-Window-Minutes": "10080",
				"X-Codex-Additional-Gpt-5.3-Codex-Spark-Secondary-Reset-At":       "1782951970",
				"X-Codex-Credits-Has-Credits":                                     "true",
				"X-Codex-Credits-Balance":                                         "12.5",
			},
			want: []Reading{
				{Window: CodexPrimaryWindow, Kind: KindRanking, Length: week, ShareLeft: 0.98, ResetAt: time.Unix(1782951970, 0), LearnedAt: learnedAt, Source: SourceHeader},
				{Window: "gpt-5.3-codex-spark/" + CodexPrimaryWindow, Kind: KindPerModel, Model: "gpt-5.3-codex-spark", Length: 5 * time.Hour, ShareLeft: 0, ResetAt: time.Unix(1782900000, 0), LearnedAt: learnedAt, Source: SourceHeader},
				{Window: "gpt-5.3-codex-spark/" + CodexSecondaryWindow, Kind: KindPerModel, Model: "gpt-5.3-codex-spark", Length: week, ShareLeft: 0.60, ResetAt: time.Unix(1782951970, 0), LearnedAt: learnedAt, Source: SourceHeader},
				{Window: CodexCreditsWindow, Kind: KindCredit, ShareLeft: 1, LearnedAt: learnedAt, Source: SourceHeader},
			},
		},
		{
			name: "additional limit without a limit name gives no reading",
			signals: map[string]string{
				"X-Codex-Bengalfox-Primary-Used-Percent":   "35",
				"X-Codex-Bengalfox-Primary-Window-Minutes": "10080",
				"X-Codex-Bengalfox-Primary-Reset-At":       "1787590000",
			},
			want: nil,
		},
		{
			name: "unlimited credits count as a balance left",
			signals: map[string]string{
				"X-Codex-Credits-Has-Credits": "false",
				"X-Codex-Credits-Unlimited":   "true",
			},
			want: []Reading{{Window: CodexCreditsWindow, Kind: KindCredit, ShareLeft: 1, LearnedAt: learnedAt, Source: SourceHeader}},
		},
		{
			name: "used percentage above 100 is clamped to an exhausted window",
			signals: map[string]string{
				"X-Codex-Primary-Used-Percent":   "104",
				"X-Codex-Primary-Window-Minutes": "10080",
				"X-Codex-Primary-Reset-At":       "1787588999",
			},
			want: []Reading{{Window: CodexPrimaryWindow, Kind: KindRanking, Length: week, ShareLeft: 0, ResetAt: time.Unix(1787588999, 0), LearnedAt: learnedAt, Source: SourceHeader}},
		},
		{
			name: "window without a length gives no reading",
			signals: map[string]string{
				"X-Codex-Primary-Used-Percent": "10",
				"X-Codex-Primary-Reset-At":     "1787588999",
			},
			want: nil,
		},
		{
			name: "window without a reset time leaves no reset pending",
			signals: map[string]string{
				"x-codex-primary-used-percent":   "10",
				"x-codex-primary-window-minutes": "10080",
			},
			want: []Reading{{Window: CodexPrimaryWindow, Kind: KindRanking, Length: week, ShareLeft: 0.9, LearnedAt: learnedAt, Source: SourceHeader}},
		},
		{
			name:    "no Codex signals",
			signals: map[string]string{"Retry-After": "30"},
			want:    nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertReadings(t, ParseCodexHeaderSignals(tc.signals, learnedAt), tc.want)
		})
	}
}

func TestFromHeaderSignalsParsesCodexSignals(t *testing.T) {
	learnedAt := time.Date(2026, time.October, 6, 12, 0, 0, 0, time.UTC)
	signals := map[string]string{
		"X-Codex-Primary-Used-Percent":   "20",
		"X-Codex-Primary-Window-Minutes": "10080",
		"X-Codex-Primary-Reset-At":       "1787588999",
	}
	got := FromHeaderSignals("codex", signals, learnedAt)
	if len(got) != 1 || got[0].Window != CodexPrimaryWindow || got[0].Kind != KindRanking {
		t.Fatalf("FromHeaderSignals(codex) = %+v, want one ranking primary-window reading", got)
	}
}

func TestParseCodexUsageBody(t *testing.T) {
	learnedAt := time.Date(2026, time.October, 6, 12, 0, 0, 0, time.UTC)
	week := 7 * 24 * time.Hour

	tests := []struct {
		name string
		body string
		want []Reading
	}{
		{
			name: "pro plan usage body: same windows as the response headers",
			body: `{
				"plan_type": "pro",
				"rate_limit": {
					"allowed": true, "limit_reached": false,
					"primary_window": {"used_percent": 1, "limit_window_seconds": 604800, "reset_after_seconds": 601888, "reset_at": 1785902974},
					"secondary_window": null
				},
				"code_review_rate_limit": {
					"primary_window": {"used_percent": 80, "limit_window_seconds": 604800, "reset_at": 1785902974}
				},
				"additional_rate_limits": [{
					"limit_name": "GPT-5.3-Codex-Spark",
					"metered_feature": "codex_bengalfox",
					"rate_limit": {
						"allowed": true, "limit_reached": false,
						"primary_window": {"used_percent": 0, "limit_window_seconds": 604800, "reset_after_seconds": 602111, "reset_at": 1785903197},
						"secondary_window": null
					}
				}],
				"credits": {"has_credits": false, "unlimited": false, "balance": "0"},
				"rate_limit_reset_credits": {"available_count": 1, "applicable_available_count": 0}
			}`,
			want: []Reading{
				{Window: CodexPrimaryWindow, Kind: KindRanking, Length: week, ShareLeft: 0.99, ResetAt: time.Unix(1785902974, 0), LearnedAt: learnedAt, Source: SourcePoll},
				{Window: "gpt-5.3-codex-spark/" + CodexPrimaryWindow, Kind: KindPerModel, Model: "gpt-5.3-codex-spark", Length: week, ShareLeft: 1, ResetAt: time.Unix(1785903197, 0), LearnedAt: learnedAt, Source: SourcePoll},
				{Window: CodexCreditsWindow, Kind: KindCredit, ShareLeft: 0, LearnedAt: learnedAt, Source: SourcePoll},
			},
		},
		{
			name: "plus plan usage body with reset given only as seconds from now",
			body: `{
				"rate_limit": {
					"primary_window": {"used_percent": "30", "limit_window_seconds": 18000, "reset_after_seconds": 3600},
					"secondary_window": {"used_percent": 70, "limit_window_seconds": 604800, "reset_at": 1785902974}
				},
				"credits": {"has_credits": true, "unlimited": false, "balance": "4.20"}
			}`,
			want: []Reading{
				{Window: CodexPrimaryWindow, Kind: KindGating, Length: 5 * time.Hour, ShareLeft: 0.70, ResetAt: learnedAt.Add(time.Hour), LearnedAt: learnedAt, Source: SourcePoll},
				{Window: CodexSecondaryWindow, Kind: KindRanking, Length: week, ShareLeft: 0.30, ResetAt: time.Unix(1785902974, 0), LearnedAt: learnedAt, Source: SourcePoll},
				{Window: CodexCreditsWindow, Kind: KindCredit, ShareLeft: 1, LearnedAt: learnedAt, Source: SourcePoll},
			},
		},
		{
			name: "body without rate limits gives no reading",
			body: `{"plan_type": "free"}`,
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, errParse := ParseCodexUsageBody([]byte(tc.body), learnedAt)
			if errParse != nil {
				t.Fatalf("ParseCodexUsageBody() error = %v", errParse)
			}
			assertReadings(t, got, tc.want)
		})
	}
}

func TestParseCodexUsageBodyRejectsAnInvalidBody(t *testing.T) {
	if _, errParse := ParseCodexUsageBody([]byte(`<html>login</html>`), time.Now()); errParse == nil {
		t.Fatal("ParseCodexUsageBody() error = nil, want an error so the poller keeps the last reading")
	}
}

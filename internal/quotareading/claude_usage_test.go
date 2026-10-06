package quotareading

import (
	"testing"
	"time"
)

func TestParseClaudeUsageBody(t *testing.T) {
	learnedAt := time.Date(2026, time.October, 6, 12, 0, 0, 0, time.UTC)
	week := 7 * 24 * time.Hour
	fiveHourReset := time.Date(2026, time.October, 6, 15, 0, 0, 0, time.UTC)
	weekReset := time.Date(2026, time.October, 9, 10, 0, 0, 0, time.UTC)
	fableReset := time.Date(2026, time.October, 10, 8, 30, 0, 0, time.UTC)

	tests := []struct {
		name string
		body string
		want []Reading
	}{
		{
			name: "Max plan: the 5-hour window gates, the 7-day window ranks, Opus and Sonnet windows are per-model, extra usage is a credit balance",
			body: `{
				"five_hour": {"utilization": 25.0, "resets_at": "2026-10-06T15:00:00.000000+00:00"},
				"seven_day": {"utilization": 60.0, "resets_at": "2026-10-09T10:00:00.000000+00:00"},
				"seven_day_oauth_apps": null,
				"seven_day_opus": {"utilization": 90.0, "resets_at": "2026-10-09T10:00:00.000000+00:00"},
				"seven_day_sonnet": {"utilization": 0.0, "resets_at": null},
				"seven_day_cowork": {"utilization": 5.0, "resets_at": "2026-10-09T10:00:00.000000+00:00"},
				"extra_usage": {"is_enabled": true, "monthly_limit": 5000, "used_credits": 1250, "utilization": 25.0}
			}`,
			want: []Reading{
				{Window: ClaudeFiveHourWindow, Kind: KindGating, Length: 5 * time.Hour, ShareLeft: 0.75, ResetAt: fiveHourReset, LearnedAt: learnedAt, Source: SourcePoll},
				{Window: ClaudeSevenDayWindow, Kind: KindRanking, Length: week, ShareLeft: 0.40, ResetAt: weekReset, LearnedAt: learnedAt, Source: SourcePoll},
				{Window: "claude-opus/seven_day", Kind: KindPerModel, Model: "claude-opus", Length: week, ShareLeft: 0.10, ResetAt: weekReset, LearnedAt: learnedAt, Source: SourcePoll},
				{Window: "claude-sonnet/seven_day", Kind: KindPerModel, Model: "claude-sonnet", Length: week, ShareLeft: 1, LearnedAt: learnedAt, Source: SourcePoll},
				{Window: ClaudeExtraUsageWindow, Kind: KindCredit, ShareLeft: 0.75, LearnedAt: learnedAt, Source: SourcePoll},
			},
		},
		{
			name: "the Fable window comes from the active weekly_scoped limit and wins over the legacy key",
			body: `{
				"seven_day": {"utilization": 10, "resets_at": "2026-10-09T10:00:00Z"},
				"iguana_necktie": {"utilization": 41, "resets_at": "2026-10-09T10:00:00Z"},
				"limits": [
					{"kind": "weekly_scoped", "percent": 12, "resets_at": "2026-10-09T10:00:00Z", "is_active": false, "scope": {"model": {"display_name": "Fable 5"}}},
					{"kind": "weekly_scoped", "group": "weekly", "percent": 64, "resets_at": "2026-10-10T08:30:00Z", "is_active": true, "scope": {"model": {"id": null, "display_name": "Fable"}}}
				]
			}`,
			want: []Reading{
				{Window: ClaudeSevenDayWindow, Kind: KindRanking, Length: week, ShareLeft: 0.90, ResetAt: weekReset, LearnedAt: learnedAt, Source: SourcePoll},
				{Window: "claude-fable/seven_day", Kind: KindPerModel, Model: "claude-fable", Length: week, ShareLeft: 0.36, ResetAt: fableReset, LearnedAt: learnedAt, Source: SourcePoll},
			},
		},
		{
			name: "the legacy Fable key is used when no scoped limit has a percent",
			body: `{
				"iguana_necktie": {"utilization": 41, "resets_at": "2026-10-10T08:30:00Z"},
				"limits": [{"kind": "weekly_scoped", "percent": null, "is_active": true, "scope": {"model": {"display_name": "Fable"}}}]
			}`,
			want: []Reading{
				{Window: "claude-fable/seven_day", Kind: KindPerModel, Model: "claude-fable", Length: week, ShareLeft: 0.59, ResetAt: fableReset, LearnedAt: learnedAt, Source: SourcePoll},
			},
		},
		{
			name: "a full window is exhausted and extra usage that is off leaves no credit",
			body: `{
				"five_hour": {"utilization": 100, "resets_at": "2026-10-06T15:00:00Z"},
				"seven_day": {"utilization": 130, "resets_at": "2026-10-09T10:00:00Z"},
				"extra_usage": {"is_enabled": false, "monthly_limit": null, "used_credits": null, "utilization": null}
			}`,
			want: []Reading{
				{Window: ClaudeFiveHourWindow, Kind: KindGating, Length: 5 * time.Hour, ShareLeft: 0, ResetAt: fiveHourReset, LearnedAt: learnedAt, Source: SourcePoll},
				{Window: ClaudeSevenDayWindow, Kind: KindRanking, Length: week, ShareLeft: 0, ResetAt: weekReset, LearnedAt: learnedAt, Source: SourcePoll},
				{Window: ClaudeExtraUsageWindow, Kind: KindCredit, ShareLeft: 0, LearnedAt: learnedAt, Source: SourcePoll},
			},
		},
		{
			name: "extra usage without a utilization uses used credits over the monthly limit",
			body: `{"extra_usage": {"is_enabled": true, "monthly_limit": 2000, "used_credits": 1500}}`,
			want: []Reading{
				{Window: ClaudeExtraUsageWindow, Kind: KindCredit, ShareLeft: 0.25, LearnedAt: learnedAt, Source: SourcePoll},
			},
		},
		{
			name: "windows without a utilization give no reading",
			body: `{"five_hour": null, "seven_day": {"resets_at": "2026-10-09T10:00:00Z"}, "seven_day_opus": {"utilization": null, "resets_at": null}}`,
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, errParse := ParseClaudeUsageBody([]byte(tc.body), learnedAt)
			if errParse != nil {
				t.Fatalf("ParseClaudeUsageBody() error = %v", errParse)
			}
			assertReadings(t, got, tc.want)
		})
	}
}

func TestParseClaudeUsageBodyRejectsAnInvalidBody(t *testing.T) {
	for _, body := range []string{"<html>login</html>", `["five_hour"]`, ""} {
		if readings, errParse := ParseClaudeUsageBody([]byte(body), time.Now()); errParse == nil {
			t.Errorf("ParseClaudeUsageBody(%q) = %+v, want an error", body, readings)
		}
	}
}

func TestClaudePollReadingsMergeWithHeaderReadings(t *testing.T) {
	headerAt := time.Date(2026, time.October, 6, 12, 0, 0, 0, time.UTC)
	pollAt := headerAt.Add(30 * time.Minute)
	store := NewStore()
	store.Record("claude-a.json", ParseClaudeHeaderSignals(map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Utilization": "0.2",
		"Anthropic-Ratelimit-Unified-5h-Reset":       "1791306000",
		"Anthropic-Ratelimit-Unified-7d-Utilization": "0.5",
		"Anthropic-Ratelimit-Unified-7d-Reset":       "1791626400",
	}, headerAt)...)

	polled, errParse := ParseClaudeUsageBody([]byte(`{
		"five_hour": {"utilization": 30, "resets_at": "2026-10-06T15:00:00Z"},
		"seven_day": {"utilization": 55, "resets_at": "2026-10-09T10:00:00Z"},
		"seven_day_opus": {"utilization": 80, "resets_at": "2026-10-09T10:00:00Z"}
	}`), pollAt)
	if errParse != nil {
		t.Fatalf("ParseClaudeUsageBody() error = %v", errParse)
	}
	store.Record("claude-a.json", polled...)

	snapshot := store.Snapshot()["claude-a.json"]
	if len(snapshot) != 3 {
		t.Fatalf("stored windows = %+v, want five_hour, seven_day and the Opus window once each", snapshot)
	}
	for _, reading := range snapshot {
		if reading.Source != SourcePoll || !reading.LearnedAt.Equal(pollAt) {
			t.Errorf("window %s = %+v, want the newer poll reading", reading.Window, reading)
		}
	}
	if opus := store.Readings("claude-a.json", "claude-opus-4-5-20251101"); len(opus) != 3 {
		t.Errorf("readings for an Opus model = %+v, want the Opus window with the shared windows", opus)
	}
	if sonnet := store.Readings("claude-a.json", "claude-sonnet-4-5"); len(sonnet) != 2 {
		t.Errorf("readings for a Sonnet model = %+v, want only the shared windows", sonnet)
	}
}

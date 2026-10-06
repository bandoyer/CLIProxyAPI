package quotareading

import (
	"testing"
	"time"
)

func TestParseXAIBillingBody(t *testing.T) {
	learnedAt := time.Date(2026, time.September, 20, 12, 0, 0, 0, time.UTC)
	weekStart := time.Date(2026, time.September, 17, 13, 32, 42, 93205000, time.UTC)
	weekEnd := weekStart.Add(7 * 24 * time.Hour)
	monthEnd := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		body string
		want []Reading
	}{
		{
			name: "weekly credit period ranks: share left is 1 minus the credit usage, reset at the period end",
			body: `{"config": {
				"currentPeriod": {"type": "USAGE_PERIOD_TYPE_WEEKLY", "start": "2026-09-17T13:32:42.093205+00:00", "end": "2026-09-24T13:32:42.093205+00:00"},
				"creditUsagePercent": 37,
				"productUsage": [{"product": "Grok Build", "usagePercent": 37}],
				"billingPeriodStart": "2026-09-17T13:32:42.093205+00:00",
				"billingPeriodEnd": "2026-09-24T13:32:42.093205+00:00",
				"onDemandCap": {"val": 0}
			}}`,
			want: []Reading{
				{Window: XAICreditPeriodWindow, Kind: KindRanking, Length: 7 * 24 * time.Hour, ShareLeft: 0.63, ResetAt: weekEnd, LearnedAt: learnedAt, Source: SourcePoll},
			},
		},
		{
			name: "monthly billing period gates on the monthly limit plus the on-demand cap, reset at the billing period end",
			body: `{"config": {
				"monthlyLimit": {"val": 15000},
				"used": {"val": "3000"},
				"onDemandCap": {"val": 5000},
				"prepaidBalance": {"val": 0},
				"billingPeriodStart": "2026-09-01T00:00:00+00:00",
				"billingPeriodEnd": "2026-10-01T00:00:00+00:00"
			}}`,
			want: []Reading{
				{Window: XAIBillingPeriodWindow, Kind: KindGating, Length: 30 * 24 * time.Hour, ShareLeft: 0.85, ResetAt: monthEnd, LearnedAt: learnedAt, Source: SourcePoll},
			},
		},
		{
			name: "spending past the monthly limit uses the on-demand cap",
			body: `{"config": {
				"monthly_limit": 15000, "used": 19000, "on_demand_cap": 5000,
				"billing_period_start": "2026-09-01T00:00:00Z", "billing_period_end": "2026-10-01T00:00:00Z"
			}}`,
			want: []Reading{
				{Window: XAIBillingPeriodWindow, Kind: KindGating, Length: 30 * 24 * time.Hour, ShareLeft: 0.05, ResetAt: monthEnd, LearnedAt: learnedAt, Source: SourcePoll},
			},
		},
		{
			name: "free account: the zero monthly limit gives no reading, the credit period still ranks",
			body: `{"config": {
				"current_period": {"type": "weekly", "start": "2026-09-17T13:32:42.093205Z", "end": "2026-09-24T13:32:42.093205Z"},
				"credit_usage_percent": "100",
				"monthlyLimit": {"val": 0}, "used": {"val": 0}, "onDemandCap": {"val": 0}
			}}`,
			want: []Reading{
				{Window: XAICreditPeriodWindow, Kind: KindRanking, Length: 7 * 24 * time.Hour, ShareLeft: 0, ResetAt: weekEnd, LearnedAt: learnedAt, Source: SourcePoll},
			},
		},
		{
			name: "credit period without a usage percent gives no reading: unknown usage is not zero",
			body: `{"config": {
				"currentPeriod": {"type": "USAGE_PERIOD_TYPE_WEEKLY", "start": "2026-09-17T13:32:42Z", "end": "2026-09-24T13:32:42Z"},
				"creditUsagePercent": "not-a-number"
			}}`,
			want: nil,
		},
		{
			name: "body without config gives no reading",
			body: `{}`,
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, errParse := ParseXAIBillingBody([]byte(tc.body), learnedAt)
			if errParse != nil {
				t.Fatalf("ParseXAIBillingBody() error = %v", errParse)
			}
			assertReadings(t, got, tc.want)
		})
	}
}

func TestParseXAIBillingBodyRejectsAnInvalidBody(t *testing.T) {
	for _, body := range []string{`<html>login</html>`, `[1, 2]`} {
		if _, errParse := ParseXAIBillingBody([]byte(body), time.Now()); errParse == nil {
			t.Fatalf("ParseXAIBillingBody(%s) error = nil, want an error so the poller keeps the last reading", body)
		}
	}
}

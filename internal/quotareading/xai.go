package quotareading

import (
	"errors"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// xAI window names for the periods of a cli-chat-proxy.grok.com /v1/billing
// body. The credit period is config.currentPeriod (weekly on today's plans).
// The billing period is the monthly spending period (config.monthlyLimit,
// used, onDemandCap, billingPeriodStart/End).
const (
	XAICreditPeriodWindow  = "credit_period"
	XAIBillingPeriodWindow = "billing_period"
)

// ParseXAIBillingBody turns an xAI /v1/billing body into readings. The
// credit period ranks: its share left is 1 minus creditUsagePercent/100 and
// it resets at the period end. The monthly billing period only gates, even
// though it is longer: its share left is what is left of the monthly limit
// plus the on-demand cap, and a zero allowance (free accounts) gives no
// reading. Product usage and the prepaid balance are not read. Fields are
// read in camelCase or snake_case. A body that is not a JSON object returns
// an error, so the poller keeps the last reading.
func ParseXAIBillingBody(body []byte, learnedAt time.Time) ([]Reading, error) {
	if !gjson.ValidBytes(body) {
		return nil, errors.New("xai billing body is not valid JSON")
	}
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return nil, errors.New("xai billing body is not a JSON object")
	}
	config := codexBodyField(root, "config")
	var readings []Reading
	if reading, ok := xaiCreditPeriodReading(config, learnedAt); ok {
		readings = append(readings, reading)
	}
	if reading, ok := xaiBillingPeriodReading(config, learnedAt); ok {
		readings = append(readings, reading)
	}
	return readings, nil
}

// xaiCreditPeriodReading reads the credit period. A period without a
// numeric creditUsagePercent gives no reading: unknown usage is not zero.
func xaiCreditPeriodReading(config gjson.Result, learnedAt time.Time) (Reading, bool) {
	usedPercent, ok := xaiNumber(codexBodyField(config, "creditUsagePercent", "credit_usage_percent"))
	if !ok {
		return Reading{}, false
	}
	period := codexBodyField(config, "currentPeriod", "current_period")
	start := xaiTime(period.Get("start"))
	if start.IsZero() {
		start = xaiTime(codexBodyField(config, "billingPeriodStart", "billing_period_start"))
	}
	end := xaiTime(period.Get("end"))
	if end.IsZero() {
		end = xaiTime(codexBodyField(config, "billingPeriodEnd", "billing_period_end"))
	}
	length := 7 * 24 * time.Hour
	if !start.IsZero() && end.After(start) {
		length = end.Sub(start)
	}
	return Reading{
		Window:    XAICreditPeriodWindow,
		Kind:      KindRanking,
		Length:    length,
		ShareLeft: clampShare(1 - usedPercent/100),
		ResetAt:   end,
		LearnedAt: learnedAt,
		Source:    SourcePoll,
	}, true
}

// xaiBillingPeriodReading reads the monthly billing period. Spending past
// the monthly limit draws on the on-demand cap, so the window is exhausted
// only when both are spent. A zero allowance or a missing used amount gives
// no reading.
func xaiBillingPeriodReading(config gjson.Result, learnedAt time.Time) (Reading, bool) {
	limit, _ := xaiNumber(codexBodyField(config, "monthlyLimit", "monthly_limit"))
	onDemandCap, _ := xaiNumber(codexBodyField(config, "onDemandCap", "on_demand_cap"))
	used, okUsed := xaiNumber(config.Get("used"))
	allowance := limit + onDemandCap
	if !okUsed || allowance <= 0 {
		return Reading{}, false
	}
	start := xaiTime(codexBodyField(config, "billingPeriodStart", "billing_period_start"))
	end := xaiTime(codexBodyField(config, "billingPeriodEnd", "billing_period_end"))
	length := 30 * 24 * time.Hour
	if !start.IsZero() && end.After(start) {
		length = end.Sub(start)
	}
	return Reading{
		Window:    XAIBillingPeriodWindow,
		Kind:      KindGating,
		Length:    length,
		ShareLeft: clampShare(1 - used/allowance),
		ResetAt:   end,
		LearnedAt: learnedAt,
		Source:    SourcePoll,
	}, true
}

// xaiNumber reads a number given as a JSON number, a numeric string, or a
// {"val": ...} object (xAI's cent amounts).
func xaiNumber(value gjson.Result) (float64, bool) {
	switch value.Type {
	case gjson.Number:
		return value.Num, true
	case gjson.String:
		return parseFloat(strings.TrimSpace(value.Str))
	case gjson.JSON:
		if value.IsObject() {
			return xaiNumber(value.Get("val"))
		}
	}
	return 0, false
}

// xaiTime reads an RFC 3339 timestamp. Anything else gives the zero time.
func xaiTime(value gjson.Result) time.Time {
	at, errParse := time.Parse(time.RFC3339Nano, strings.TrimSpace(value.String()))
	if errParse != nil {
		return time.Time{}
	}
	return at
}

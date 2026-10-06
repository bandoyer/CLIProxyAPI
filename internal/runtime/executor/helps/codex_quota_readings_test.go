package helps

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/quotareading"
)

// The WebSocket codex.rate_limits event reaches routing as the header
// signals ParseCodexQuotaEventHeaders produces; this checks that the
// quota-reading parser understands them.
func TestCodexWebsocketRateLimitEventBecomesQuotaReadings(t *testing.T) {
	headers := ParseCodexQuotaEventHeaders([]byte(`{
		"type":"codex.rate_limits",
		"plan_type":"plus",
		"rate_limits":{
			"primary":{"used_percent":30,"window_minutes":300,"reset_at":1787231961},
			"secondary":{"used_percent":48,"window_minutes":10080,"reset_at":1786677299}
		},
		"additional_rate_limits":{
			"GPT-5.3-Codex-Spark":{
				"primary":{"used_percent":3,"window_minutes":300,"reset_at":1787231961},
				"secondary":{"used_percent":63,"window_minutes":10080,"reset_at":1787290791}
			}
		},
		"credits":{"has_credits":false,"unlimited":false,"balance":"0"}
	}`))
	signals := make(map[string]string, len(headers))
	for key := range headers {
		signals[key] = headers.Get(key)
	}

	got := quotareading.ParseCodexHeaderSignals(signals, time.Unix(1786600000, 0))
	type windowShare struct {
		window string
		kind   quotareading.Kind
		model  string
		share  float64
	}
	want := []windowShare{
		{window: quotareading.CodexPrimaryWindow, kind: quotareading.KindGating, share: 0.70},
		{window: quotareading.CodexSecondaryWindow, kind: quotareading.KindRanking, share: 0.52},
		{window: "gpt-5.3-codex-spark/" + quotareading.CodexPrimaryWindow, kind: quotareading.KindPerModel, model: "gpt-5.3-codex-spark", share: 0.97},
		{window: "gpt-5.3-codex-spark/" + quotareading.CodexSecondaryWindow, kind: quotareading.KindPerModel, model: "gpt-5.3-codex-spark", share: 0.37},
		{window: quotareading.CodexCreditsWindow, kind: quotareading.KindCredit, share: 0},
	}
	if len(got) != len(want) {
		t.Fatalf("readings = %+v, want %d readings", got, len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Window != w.window || g.Kind != w.kind || g.Model != w.model || g.ShareLeft < w.share-1e-9 || g.ShareLeft > w.share+1e-9 {
			t.Fatalf("reading %d = %+v, want %+v", i, g, w)
		}
	}
}

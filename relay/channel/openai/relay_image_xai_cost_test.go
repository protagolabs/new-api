package openai

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
)

func xaiInfo(channelType int, modelPrice float64, usePrice bool, ratios map[string]float64) *relaycommon.RelayInfo {
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelType: channelType}}
	info.PriceData = types.PriceData{ModelPrice: modelPrice, UsePrice: usePrice}
	for k, v := range ratios {
		info.PriceData.AddOtherRatio(k, v)
	}
	return info
}

// effectiveUSD is what the user ends up being charged, in dollars.
func effectiveUSD(info *relaycommon.RelayInfo) float64 {
	v := info.PriceData.ModelPrice
	for _, r := range info.PriceData.OtherRatios() {
		v *= r
	}
	return v
}

// grok-imagine-image-quality is $0.05 at 1K but $0.07 at 2K, and the tier is
// chosen upstream — a single configured ModelPrice is wrong for one of them.
// The reported cost must win regardless of what was configured.
func TestXaiImageCostOverridesConfiguredPrice(t *testing.T) {
	cases := []struct {
		name       string
		modelPrice float64
		ratios     map[string]float64
		body       string
		wantUSD    float64
	}{
		{
			// Configured at the 2K ceiling, upstream served 1K.
			name: "configured high, upstream 1K", modelPrice: 0.07,
			body: `{"data":[{"url":"u"}],"usage":{"cost_in_usd_ticks":500000000}}`, wantUSD: 0.05,
		},
		{
			name: "configured low, upstream 2K", modelPrice: 0.05,
			body: `{"data":[{"url":"u"}],"usage":{"cost_in_usd_ticks":700000000}}`, wantUSD: 0.07,
		},
		{
			// The n ratio is applied before this hook; the final charge must
			// still be exactly what the upstream reported for the whole call.
			name: "with n ratio", modelPrice: 0.02, ratios: map[string]float64{"n": 3},
			body:    `{"data":[{"url":"a"},{"url":"b"},{"url":"c"}],"usage":{"cost_in_usd_ticks":600000000}}`,
			wantUSD: 0.06,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := xaiInfo(constant.ChannelTypeXai, tc.modelPrice, true, tc.ratios)
			applyXaiImageUpstreamCost(info, []byte(tc.body))
			if got := effectiveUSD(info); math.Abs(got-tc.wantUSD) > 1e-9 {
				t.Errorf("charge: got $%v, want $%v", got, tc.wantUSD)
			}
		})
	}
}

func TestXaiImageCostNoOpCases(t *testing.T) {
	body := `{"data":[{"url":"u"}],"usage":{"cost_in_usd_ticks":500000000}}`

	// Other vendors must be untouched — this hook sits in shared OpenAI code.
	info := xaiInfo(constant.ChannelTypeOpenAI, 0.07, true, nil)
	applyXaiImageUpstreamCost(info, []byte(body))
	if got := effectiveUSD(info); math.Abs(got-0.07) > 1e-9 {
		t.Errorf("non-xai channel must be untouched: got $%v, want $0.07", got)
	}

	// Token-billed requests do not use ModelPrice at all.
	info = xaiInfo(constant.ChannelTypeXai, 0.07, false, nil)
	applyXaiImageUpstreamCost(info, []byte(body))
	if _, ok := info.PriceData.OtherRatios()["upstream_cost"]; ok {
		t.Error("token-billed request must not get an upstream_cost ratio")
	}

	// No usage block (older or non-conforming response) — keep configured price.
	info = xaiInfo(constant.ChannelTypeXai, 0.07, true, nil)
	applyXaiImageUpstreamCost(info, []byte(`{"data":[{"url":"u"}]}`))
	if got := effectiveUSD(info); math.Abs(got-0.07) > 1e-9 {
		t.Errorf("missing usage must fall back to configured: got $%v", got)
	}

	// An implausible cost must be refused rather than charged.
	info = xaiInfo(constant.ChannelTypeXai, 0.07, true, nil)
	applyXaiImageUpstreamCost(info, []byte(`{"usage":{"cost_in_usd_ticks":999999999999}}`))
	if got := effectiveUSD(info); math.Abs(got-0.07) > 1e-9 {
		t.Errorf("implausible cost must be refused: got $%v", got)
	}
}

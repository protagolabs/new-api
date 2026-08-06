package openai

import (
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/tidwall/gjson"
)

// xAI image responses carry the provider's own charge for the request:
//
//	{"data":[...],"usage":{"cost_in_usd_ticks":200000000}}
//
// Ticks are USD scaled by 1e10 (verified live: 200000000 == $0.02 for
// grok-imagine-image, 500000000 == $0.05 for grok-imagine-image-quality at 1K).
//
// This matters because several grok image models are tiered by output
// resolution (image-quality is $0.05 at 1K but $0.07 at 2K) and the tier is
// chosen upstream, so a single configured ModelPrice is wrong for one tier or
// the other. Deriving the charge from the reported cost is exact for every
// tier, needs no per-vendor size table, and also picks up the per-image input
// charge automatically.
const xaiCostTicksPerUSD = 10_000_000_000.0

// xaiMaxImageCostUSD bounds a cost echoed back by the upstream before it lands
// on a user's bill. The dearest documented grok image is $0.07; this leaves
// wide headroom while still refusing an absurd value.
const xaiMaxImageCostUSD = 5.0

// applyXaiImageUpstreamCost rewrites the billing ratios so the charge equals
// the cost xAI reported. No-op for other channels, for token-billed requests,
// or when the response carries no usable cost.
func applyXaiImageUpstreamCost(info *relaycommon.RelayInfo, responseBody []byte) {
	if info == nil || info.ChannelType != constant.ChannelTypeXai || !info.PriceData.UsePrice {
		return
	}

	ticks := gjson.GetBytes(responseBody, "usage.cost_in_usd_ticks").Float()
	if ticks <= 0 {
		return
	}
	costUSD := ticks / xaiCostTicksPerUSD
	if costUSD > xaiMaxImageCostUSD {
		return
	}

	// Final charge is ModelPrice * groupRatio * product(otherRatios). Solve for
	// the factor that makes it land exactly on costUSD, leaving the existing
	// ratios (notably "n") visible in the log for reconciliation.
	effective := info.PriceData.ModelPrice
	for _, r := range info.PriceData.OtherRatios() {
		effective *= r
	}
	if effective <= 0 {
		return
	}
	info.PriceData.AddOtherRatio("upstream_cost", costUSD/effective)
}

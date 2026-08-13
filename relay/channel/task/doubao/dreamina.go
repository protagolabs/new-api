package doubao

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// Dreamina Seedance 2.0 is priced per output token, per resolution tier -- the
// vendor's billing unit is `usage.completion_tokens` at a flat $/token rate, not
// per call and not per second. A flat ModelPrice set PerCallBilling, which
// short-circuits settleTaskBillingOnComplete before duration or resolution are
// considered, so a 10s video cost us twice a 5s one and sold for the same price
// (measured in production 2026-08-13: 5s/720p and 10s/720p both charged $2.10).
//
// Billing by token (ModelRatio) rather than by second keeps our cost aligned
// with the vendor's invoice exactly: 10s/720p is 216.9K tokens, not 2x the 5s
// 108.9K, and only token billing reproduces that without a drifting margin.

const (
	Dreamina480P  = "480p"
	Dreamina720P  = "720p"
	Dreamina1080P = "1080p"
	Dreamina4K    = "4k"

	// The vendor's own default when no resolution is given. Billing must assume
	// the same tier the vendor will render, or the pre-charge starts out wrong.
	dreaminaDefaultResolution = Dreamina720P
)

// dreaminaResolutionRatios multiply ModelRatio, which is configured as the 720p
// per-token rate. These are the vendor's *unit prices per token* by output
// resolution, relative to the 480p/720p no-video rate of $7.0/M tokens:
// $7.0 (480p/720p) / $7.7 (1080p) / $4.0 (4K).
//
// They must NOT be the "5s per-video price ratio" ($0.35 / $0.76 / $1.87 /
// $3.89): usage.completion_tokens already encodes resolution, duration and
// frame rate, so the 5s ratio would double-count the resolution's effect on
// token count -- undercharging 480p by ~54% and overcharging 4K by ~8x.
var dreaminaResolutionRatios = map[string]float64{
	Dreamina480P:  7.0 / 7.0,
	Dreamina720P:  7.0 / 7.0,
	Dreamina1080P: 7.7 / 7.0,
	Dreamina4K:    4.0 / 7.0,
}

// dreaminaVideoInputRatios are the unit prices per token when the request
// carries a video input, again relative to the 720p no-video base: $4.3
// (480p/720p) / $4.7 (1080p) / $2.4 (4K).
//
// The unit price is *lower* with a video input, but total cost is still higher
// because the input video's duration adds to token consumption -- the token
// count already captures that, so no separate input-duration factor is needed.
var dreaminaVideoInputRatios = map[string]float64{
	Dreamina480P:  4.3 / 7.0,
	Dreamina720P:  4.3 / 7.0,
	Dreamina1080P: 4.7 / 7.0,
	Dreamina4K:    2.4 / 7.0,
}

// IsDreaminaSeedance2 reports whether a model is priced by the Dreamina USD
// list. Prefix-matched so point releases do not silently fall back to the
// per-call pricing this replaces.
func IsDreaminaSeedance2(modelName string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(modelName)), "dreamina-seedance-2")
}

// NormalizeDreaminaResolution maps the ways a caller can spell a tier onto the
// vendor's own values. Returns "" when nothing usable is given.
func NormalizeDreaminaResolution(v string) string {
	s := strings.ToLower(strings.TrimSpace(v))
	switch s {
	case "480p", "480":
		return Dreamina480P
	case "720p", "720":
		return Dreamina720P
	case "1080p", "1080", "fhd":
		return Dreamina1080P
	case "4k", "2160p", "2160", "uhd":
		return Dreamina4K
	}
	return ""
}

// dreaminaTierRatio picks the per-token multiplier for a resolution tier,
// falling back to the vendor's default tier when the caller gave nothing usable.
func dreaminaTierRatio(resolution string, hasVideo bool) float64 {
	res := NormalizeDreaminaResolution(resolution)
	if res == "" {
		res = dreaminaDefaultResolution
	}
	table := dreaminaResolutionRatios
	if hasVideo {
		table = dreaminaVideoInputRatios
	}
	if r, ok := table[res]; ok {
		return r
	}
	return 1.0
}

// dreaminaRequestedResolution resolves the tier a caller asked for, accepting
// both the vendor-native `metadata.resolution` and the OpenAI-style `size`.
//
// It must stay in lockstep with applyDreaminaResolution: billing the tier from
// `size` while sending nothing upstream would pre-charge for 4K and receive the
// vendor's 720p default.
func dreaminaRequestedResolution(req *relaycommon.TaskSubmitReq) string {
	if req == nil {
		return ""
	}
	if v, _ := req.Metadata["resolution"].(string); v != "" {
		if res := NormalizeDreaminaResolution(v); res != "" {
			return res
		}
	}
	return NormalizeDreaminaResolution(req.Size)
}

// applyDreaminaResolution copies the requested tier into the field the vendor
// actually reads. Without this, `size` is silently dropped -- the request is
// accepted, the video renders at the 720p default, and the caller never learns
// their 1080p was ignored.
func applyDreaminaResolution(req *relaycommon.TaskSubmitReq, out *requestPayload) {
	if out == nil || out.Resolution != "" {
		return
	}
	if res := dreaminaRequestedResolution(req); res != "" {
		out.Resolution = res
	}
}

// dreaminaRatios prices a request as ModelRatio x resolution tier. The duration
// deliberately does NOT appear here: the token pre-charge already assumes a fixed
// token budget, and the completion-time settlement reconciles against the actual
// completion_tokens, so baking seconds into the pre-charge would double-count it.
func dreaminaRatios(resolution string, hasVideo bool) map[string]float64 {
	if r := dreaminaTierRatio(resolution, hasVideo); r != 1.0 {
		return map[string]float64{"resolution": r}
	}
	return nil
}

// DreaminaSettleQuota re-prices a finished task against the tokens the vendor
// actually billed and the resolution tier it reported.
//
// Reading the resolution back is the point. Trusting the requested value would
// be a money-losing bug in both directions -- and for this vendor the request is
// especially untrustworthy, since it ignores the OpenAI-style `size` field
// entirely and renders its 720p default. Ask for 4K, get 720p, and billing the
// requested tier would charge 5.1x the correct amount.
//
// Whether the input carried a video is taken from submit time instead: the
// vendor cannot retroactively change what we sent it, and the response says
// nothing about the input.
//
// Returns 0 to leave the pre-charge untouched -- for other models, for
// unfinished or failed tasks, for per-second-priced models (ModelRatio is zero),
// and whenever the response is unusable.
func DreaminaSettleQuota(task *model.Task, taskResult *relaycommon.TaskInfo) int {
	if task == nil || taskResult == nil || taskResult.Status != model.TaskStatusSuccess {
		return 0
	}
	if !IsDreaminaSeedance2(task.Properties.OriginModelName) &&
		!IsDreaminaSeedance2(task.Properties.UpstreamModelName) {
		return 0
	}
	bc := task.PrivateData.BillingContext
	// ModelRatio is only set when the model is billed per token; a per-second
	// ModelPrice leaves it at zero and there is nothing to settle here.
	if bc == nil || bc.ModelRatio <= 0 {
		return 0
	}

	var resp responseTask
	if len(task.Data) == 0 || common.Unmarshal(task.Data, &resp) != nil {
		return 0
	}
	tokens := resp.Usage.CompletionTokens
	if tokens <= 0 {
		tokens = resp.Usage.TotalTokens
	}
	if tokens <= 0 {
		return 0
	}

	// hasVideo is recoverable from the submit-time ratio: the video-input table
	// has no entry equal to the no-video one, so a resolution ratio that matches
	// a video-input tier means the request carried a video.
	hasVideo := dreaminaHadVideoInput(bc.OtherRatios["resolution"])

	groupRatio := bc.GroupRatio
	if groupRatio <= 0 {
		groupRatio = 1
	}

	// ModelRatio is quota per token, so the result is already in quota -- no
	// QuotaPerUnit scaling, unlike the per-second path it replaces.
	total := float64(tokens) * bc.ModelRatio * dreaminaTierRatio(resp.Resolution, hasVideo) * groupRatio
	if total <= 0 {
		return 0
	}
	return int(total)
}

// dreaminaHadVideoInput recognises a submit-time resolution ratio as belonging
// to the video-input table. The two tables share no values, so the match is
// unambiguous.
func dreaminaHadVideoInput(submitRatio float64) bool {
	if submitRatio <= 0 {
		return false
	}
	const epsilon = 1e-9
	for _, r := range dreaminaVideoInputRatios {
		if submitRatio > r-epsilon && submitRatio < r+epsilon {
			return true
		}
	}
	return false
}

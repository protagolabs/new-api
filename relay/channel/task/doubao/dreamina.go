package doubao

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// Dreamina Seedance 2.0 is priced per second of output, per resolution tier --
// not per call, which is how it was originally configured here. A flat
// ModelPrice sets PerCallBilling, which short-circuits
// settleTaskBillingOnComplete before any duration or resolution is considered,
// so a 10s video cost us twice a 5s one and sold for the same price (measured in
// production 2026-08-13: 5s/720p and 10s/720p both charged $2.10).

const (
	Dreamina480P  = "480p"
	Dreamina720P  = "720p"
	Dreamina1080P = "1080p"
	Dreamina4K    = "4k"

	// The vendor's own default when no resolution is given. Billing must assume
	// the same tier the vendor will render, or the pre-charge starts out wrong.
	dreaminaDefaultResolution = Dreamina720P
)

// dreaminaResolutionRatios multiply ModelPrice, which is configured as the 720p
// per-second rate. Derived from the vendor's published USD list for a 5s video
// -- $0.35 / $0.76 / $1.87 / $3.89 for 480p / 720p / 1080p / 4K. The per-video
// figures are used rather than the per-second ones ($0.07 / $0.15 / $0.37 /
// $0.78) because the latter are rounded to two decimals.
//
// Do NOT derive these from the CNY list by exchange rate. The vendor prices the
// USD region independently; the CNY list is quoted per million tokens with a
// different shape entirely. We bill against the international endpoint, so the
// USD list is the authoritative one.
var dreaminaResolutionRatios = map[string]float64{
	Dreamina480P:  0.35 / 0.76,
	Dreamina720P:  1.0,
	Dreamina1080P: 1.87 / 0.76,
	Dreamina4K:    3.89 / 0.76,
}

// dreaminaVideoInputRatios apply when the request carries a video input, which
// the vendor charges for on top of the output. Its price then depends on the
// *input* video's duration (2-15s), which the response does not report -- so
// these use the 15s upper bound of the published range ($0.86 / $1.86 / $4.57 /
// $9.33 per video), relative to the same 720p no-video base.
//
// That deliberately overcharges short video inputs rather than undercharging
// long ones. No production traffic uses video input yet; revisit with real
// requests before promoting the feature.
var dreaminaVideoInputRatios = map[string]float64{
	Dreamina480P:  0.86 / 0.76,
	Dreamina720P:  1.86 / 0.76,
	Dreamina1080P: 4.57 / 0.76,
	Dreamina4K:    9.33 / 0.76,
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

// dreaminaTierRatio picks the per-second multiplier for a resolution tier,
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

// dreaminaRatios prices a request as ModelPrice x seconds x resolution.
func dreaminaRatios(seconds int, resolution string, hasVideo bool) map[string]float64 {
	if seconds <= 0 {
		seconds = 5 // the vendor's default output duration
	}
	if seconds > relaycommon.MaxTaskDurationSeconds {
		seconds = relaycommon.MaxTaskDurationSeconds
	}
	return map[string]float64{
		"seconds":    float64(seconds),
		"resolution": dreaminaTierRatio(resolution, hasVideo),
	}
}

// DreaminaSettleQuota re-prices a finished task against what the vendor actually
// delivered: the duration and resolution reported in the response.
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
// unfinished or failed tasks, and whenever the response is unusable.
func DreaminaSettleQuota(task *model.Task, taskResult *relaycommon.TaskInfo) int {
	if task == nil || taskResult == nil || taskResult.Status != model.TaskStatusSuccess {
		return 0
	}
	if !IsDreaminaSeedance2(task.Properties.OriginModelName) &&
		!IsDreaminaSeedance2(task.Properties.UpstreamModelName) {
		return 0
	}
	bc := task.PrivateData.BillingContext
	if bc == nil || bc.ModelPrice <= 0 {
		return 0
	}

	var resp responseTask
	if len(task.Data) == 0 || common.Unmarshal(task.Data, &resp) != nil {
		return 0
	}
	seconds := resp.Duration
	if seconds <= 0 {
		return 0
	}
	if seconds > relaycommon.MaxTaskDurationSeconds {
		seconds = relaycommon.MaxTaskDurationSeconds
	}

	// hasVideo is recoverable from the submit-time ratio: the video-input table
	// has no entry equal to the no-video one, so a resolution ratio that matches
	// a video-input tier means the request carried a video.
	hasVideo := dreaminaHadVideoInput(bc.OtherRatios["resolution"])

	groupRatio := bc.GroupRatio
	if groupRatio <= 0 {
		groupRatio = 1
	}

	total := bc.ModelPrice * float64(seconds) * dreaminaTierRatio(resp.Resolution, hasVideo) * groupRatio
	if total <= 0 {
		return 0
	}
	return int(total * common.QuotaPerUnit)
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

package doubao

import (
	"strconv"
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
// Billing by token (ModelRatio) rather than by second keeps our price aligned
// with the vendor's invoice exactly: 10s/720p is 216.9K tokens, not 2x the 5s
// 108.9K, and only token billing reproduces that.
//
// PRICING PRINCIPLE: we resell at the vendor's *published list price*, not at a
// markup. ModelRatio must equal the vendor's per-token list rate (1 ratio =
// $2/M tokens, so the $7.0/M base tier means ModelRatio = 3.5). Our margin comes
// from the discount the vendor gives us off list, which never appears in these
// tables -- a customer who paid more here than going direct to the vendor would
// have no reason to use us at all.

const (
	Dreamina480P  = "480p"
	Dreamina720P  = "720p"
	Dreamina1080P = "1080p"
	Dreamina4K    = "4k"

	// The vendor's own default when no resolution is given. Billing must assume
	// the same tier the vendor will render, or the pre-charge starts out wrong.
	dreaminaDefaultResolution = Dreamina720P
)

// dreaminaPricing is one model family's per-token unit prices, expressed as
// ratios against that family's own no-video base tier -- so ModelRatio is always
// the family's base list rate and the tables only carry tier differences.
//
// The ratios must NOT be built from the "per-video price" tables in the docs
// ($0.35 / $0.76 / $1.87 / $3.89 for a 5s 2.0 video): usage.completion_tokens
// already encodes resolution, duration and frame rate, so a per-video ratio
// double-counts resolution's effect on the token count -- that mistake
// undercharged 480p by ~54% and overcharged 4K by ~8x.
type dreaminaPricing struct {
	base       float64            // family's no-video base list rate, USD/M tokens
	resolution map[string]float64 // no video input
	videoInput map[string]float64 // with video input
}

// dreaminaPricingByFamily maps a model-name prefix to its published USD list
// rates (docs.byteplus.com, per M tokens). Matched by longest prefix so a new
// point release cannot silently inherit another family's rates.
//
// A family absent here is not treated as Dreamina at all: it falls through to
// the generic per-token path, which bills tokens x ModelRatio with no tier
// adjustment. Correct for a base-tier request, and it overcharges rather than
// undercharges otherwise.
var dreaminaPricingByFamily = map[string]dreaminaPricing{
	// Seedance 2.0: rate varies by output resolution AND video input.
	// no video: $7.0 (480p/720p) / $7.7 (1080p) / $4.0 (4K)
	// video in: $4.3 (480p/720p) / $4.7 (1080p) / $2.4 (4K)
	"dreamina-seedance-2-0": {
		base: 7.0,
		resolution: map[string]float64{
			Dreamina480P: 7.0 / 7.0, Dreamina720P: 7.0 / 7.0,
			Dreamina1080P: 7.7 / 7.0, Dreamina4K: 4.0 / 7.0,
		},
		videoInput: map[string]float64{
			Dreamina480P: 4.3 / 7.0, Dreamina720P: 4.3 / 7.0,
			Dreamina1080P: 4.7 / 7.0, Dreamina4K: 2.4 / 7.0,
		},
	},
	// Seedance 2.5: only 480p/720p exist, and both bill at the same rate, so the
	// only tier axis is video input. no video $10.70 / video in $6.40.
	// Input video may run to 30s here (2.0 caps at 15s), and a minimum token
	// charge applies with video input -- both already reflected in the vendor's
	// completion_tokens, so neither needs handling on our side.
	"dreamina-seedance-2-5": {
		base: 10.70,
		resolution: map[string]float64{
			Dreamina480P: 1.0, Dreamina720P: 1.0,
		},
		videoInput: map[string]float64{
			Dreamina480P: 6.40 / 10.70, Dreamina720P: 6.40 / 10.70,
		},
	},
}

// dreaminaFamilyFor returns the pricing for a model, matching the longest
// configured prefix.
func dreaminaFamilyFor(modelName string) (dreaminaPricing, bool) {
	name := strings.ToLower(strings.TrimSpace(modelName))
	best, bestLen, found := dreaminaPricing{}, -1, false
	for prefix, pricing := range dreaminaPricingByFamily {
		if strings.HasPrefix(name, prefix) && len(prefix) > bestLen {
			best, bestLen, found = pricing, len(prefix), true
		}
	}
	return best, found
}

// IsDreaminaSeedance2 reports whether a model is priced by a known Dreamina USD
// list. An unrecognised dreamina release returns false on purpose: inheriting
// another family's rates would be a silent mispricing, while falling through to
// the generic per-token path is visible and errs toward overcharging.
func IsDreaminaSeedance2(modelName string) bool {
	_, ok := dreaminaFamilyFor(modelName)
	return ok
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
	// Pixel dimensions, e.g. "854x480". The vendor itself only accepts tier
	// names, but callers coming from the OpenAI image API — or from HappyHorse
	// and MiniMax on this same gateway, which both take WxH — reasonably expect
	// this to work. Left unparsed it becomes an empty resolution, the vendor
	// renders its 720p default, and the caller is billed for a tier they did
	// not ask for.
	return dreaminaResolutionFromPixels(s)
}

// dreaminaResolutionFromPixels maps a "WxH" string onto the nearest tier at or
// below the requested size, so a request is never silently upgraded into a more
// expensive tier.
func dreaminaResolutionFromPixels(s string) string {
	parts := strings.SplitN(s, "x", 2)
	if len(parts) != 2 {
		return ""
	}
	w, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	if errW != nil || errH != nil || w <= 0 || h <= 0 {
		return ""
	}
	maxDim := w
	if h > maxDim {
		maxDim = h
	}
	switch {
	case maxDim >= 3840:
		return Dreamina4K
	case maxDim >= 1920:
		return Dreamina1080P
	case maxDim >= 1280:
		return Dreamina720P
	default:
		return Dreamina480P
	}
}

// dreaminaTierRatio picks the per-token multiplier for a model's resolution
// tier, falling back to the vendor's default tier when the caller gave nothing
// usable, and to the family's base rate for a tier the family does not list.
func dreaminaTierRatio(modelName, resolution string, hasVideo bool) float64 {
	pricing, ok := dreaminaFamilyFor(modelName)
	if !ok {
		return 1.0
	}
	res := NormalizeDreaminaResolution(resolution)
	if res == "" {
		res = dreaminaDefaultResolution
	}
	table := pricing.resolution
	if hasVideo {
		table = pricing.videoInput
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
func dreaminaRatios(modelName, resolution string, hasVideo bool) map[string]float64 {
	if r := dreaminaTierRatio(modelName, resolution, hasVideo); r != 1.0 {
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

	// Settle against the model the vendor reports, falling back to what we
	// recorded -- the tier tables are family-specific and must not be crossed.
	modelName := resp.Model
	if !IsDreaminaSeedance2(modelName) {
		modelName = task.Properties.OriginModelName
		if !IsDreaminaSeedance2(modelName) {
			modelName = task.Properties.UpstreamModelName
		}
	}

	// hasVideo is recoverable from the submit-time ratio: within a family the
	// video-input table shares no value with the no-video one, so a resolution
	// ratio matching a video-input tier means the request carried a video.
	hasVideo := dreaminaHadVideoInput(modelName, bc.OtherRatios["resolution"])

	groupRatio := bc.GroupRatio
	if groupRatio <= 0 {
		groupRatio = 1
	}

	// ModelRatio is quota per token, so the result is already in quota -- no
	// QuotaPerUnit scaling, unlike the per-second path it replaces.
	total := float64(tokens) * bc.ModelRatio * dreaminaTierRatio(modelName, resp.Resolution, hasVideo) * groupRatio
	if total <= 0 {
		return 0
	}
	return int(total)
}

// dreaminaHadVideoInput recognises a submit-time resolution ratio as belonging
// to this family's video-input table. Scoped to the family because a ratio that
// means "video input" for one release can be a plain resolution tier in another;
// within a family the two tables share no values, which a test asserts.
func dreaminaHadVideoInput(modelName string, submitRatio float64) bool {
	if submitRatio <= 0 {
		return false
	}
	pricing, ok := dreaminaFamilyFor(modelName)
	if !ok {
		return false
	}
	const epsilon = 1e-9
	for _, r := range pricing.videoInput {
		if submitRatio > r-epsilon && submitRatio < r+epsilon {
			return true
		}
	}
	return false
}

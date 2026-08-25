package ali

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// Wan 3.0 is the "omni reference" generation: text, images, video, audio,
// documents and web links can all be reference inputs. Three properties force
// handling that differs from every other Wan model on this adaptor, all
// confirmed against the live international endpoint:
//
//   - resolution defaults to 1080P upstream -- the most expensive tier. The
//     generic mapping hands unknown Wan models 720P, which would pre-charge
//     half of what a request omitting the field actually costs.
//   - parameters are not validated at submission. An illegal resolution or
//     duration is accepted and queued, failing only asynchronously, so what we
//     billed is never evidence of what was delivered. usage.SR is.
//   - duration accepts -1, "smart duration", where the request carries no
//     length at all and only usage.output_video_duration knows what to bill.
//
// Docs: https://help.aliyun.com/zh/model-studio/wan3-video-generation-api-reference
const (
	Wan30Resolution480P  = "480P"
	Wan30Resolution720P  = "720P"
	Wan30Resolution1080P = "1080P"

	// wan30DefaultResolution mirrors the upstream default so the pre-charge
	// matches what a request that omits the field actually produces.
	wan30DefaultResolution = Wan30Resolution1080P

	// wan30DefaultDuration is the vendor's own default for an absent duration.
	wan30DefaultDuration = 5

	// wan30MaxDuration is the upstream ceiling when no video is referenced.
	// Smart-duration requests are pre-charged at this length: settlement refunds
	// the difference, whereas guessing low would under-charge a 30s render and
	// leave the account short.
	wan30MaxDuration = 30
)

// Ratios multiply ModelPrice, which is configured as each model's 480P
// per-second rate. Figures are the vendor's international (Singapore) list:
//
//	wan3.0-video        $0.05  / $0.10 / $0.20 per second
//	wan3.0-video-prime  $0.068 / $0.14 / $0.28 per second
//
// The standard model's "limited time 30% off" is a purchase discount, not a
// list-price change -- it is margin, and it expires, so billing follows the list
// price and needs no edit when the promotion ends.
//
// Do not collapse the two tables: prime is not a flat uplift of standard
// (2.06x/4.12x vs 2x/4x), so sharing one would misprice its 720P and 1080P.
var (
	wan30ResolutionRatios = map[string]float64{
		Wan30Resolution480P:  1,
		Wan30Resolution720P:  0.10 / 0.05,
		Wan30Resolution1080P: 0.20 / 0.05,
	}
	wan30PrimeResolutionRatios = map[string]float64{
		Wan30Resolution480P:  1,
		Wan30Resolution720P:  0.14 / 0.068,
		Wan30Resolution1080P: 0.28 / 0.068,
	}
)

// IsWan30 reports whether a model belongs to the Wan 3.0 video family, prime
// included. Prefix-matched so a future point release keeps the omni request
// shape instead of silently falling back to the Wan 2.x one.
func IsWan30(modelName string) bool {
	return strings.HasPrefix(normalizedWanModel(modelName), "wan3.0-video")
}

func isWan30Prime(modelName string) bool {
	return strings.HasPrefix(normalizedWanModel(modelName), "wan3.0-video-prime")
}

func normalizedWanModel(modelName string) string {
	return strings.ToLower(strings.TrimSpace(modelName))
}

func wan30BuiltinRatios(modelName string) map[string]float64 {
	if isWan30Prime(modelName) {
		return wan30PrimeResolutionRatios
	}
	return wan30ResolutionRatios
}

// wan30TierRatio prefers the operator-configured video_tier_ratio, so a new
// generation or a repriced tier is a config change rather than a release.
func wan30TierRatio(modelName, resolution string) float64 {
	tier := NormalizeWan30Resolution(resolution)
	if tier == "" {
		tier = wan30DefaultResolution
	}
	if ratio, ok := ratio_setting.GetVideoTierRatio(modelName, tier); ok {
		return ratio
	}
	if ratio, ok := wan30BuiltinRatios(modelName)[tier]; ok {
		return ratio
	}
	return 1
}

// NormalizeWan30Resolution maps the ways a caller can express a tier onto the
// three the vendor accepts, returning "" when nothing usable was given.
func NormalizeWan30Resolution(v string) string {
	s := strings.ToUpper(strings.TrimSpace(v))
	if s == "" {
		return ""
	}
	switch s {
	case "480P", "480":
		return Wan30Resolution480P
	case "720P", "720":
		return Wan30Resolution720P
	case "1080P", "1080":
		return Wan30Resolution1080P
	}
	// A WxH size: the vendor takes tiers only, but callers still send sizes, so
	// translate rather than reject. The short edge names the tier -- keying on
	// height instead would read portrait 720*1280 as 1080P and overcharge it.
	if w, h, ok := parseWidthHeight(s); ok {
		shortEdge := h
		if w < shortEdge {
			shortEdge = w
		}
		switch {
		case shortEdge >= 1080:
			return Wan30Resolution1080P
		case shortEdge >= 720:
			return Wan30Resolution720P
		default:
			return Wan30Resolution480P
		}
	}
	return ""
}

// applyWan30Parameters rewrites the tier knobs onto the field the vendor reads.
// Called after the generic Wan mapping so it overrides whatever that produced,
// including anything metadata set.
func applyWan30Parameters(aliReq *AliVideoRequest, requestedSize string) {
	if aliReq.Parameters == nil {
		aliReq.Parameters = &AliVideoParameters{}
	}
	res := NormalizeWan30Resolution(aliReq.Parameters.Resolution)
	if res == "" {
		res = NormalizeWan30Resolution(requestedSize)
	}
	if res == "" {
		res = NormalizeWan30Resolution(aliReq.Parameters.Size)
	}
	if res == "" {
		res = wan30DefaultResolution
	}
	aliReq.Parameters.Resolution = res
	// The omni API takes resolution only. Leaving size set risks rejection for an
	// unknown field and would misreport the tier we charged for.
	aliReq.Parameters.Size = ""
}

// wan30BillableSeconds resolves the length to pre-charge. Smart duration
// carries no length, so it bills the upstream ceiling and leans on settlement to
// refund the difference.
func wan30BillableSeconds(duration int) int {
	if duration == relaycommon.AutoTaskDuration {
		return wan30MaxDuration
	}
	if duration <= 0 {
		return wan30DefaultDuration
	}
	return duration
}

// wan30TierFromSR names the tier from the vertical resolution the vendor says
// it rendered.
func wan30TierFromSR(sr int) string {
	switch {
	case sr >= 1080:
		return Wan30Resolution1080P
	case sr >= 720:
		return Wan30Resolution720P
	default:
		return Wan30Resolution480P
	}
}

// Wan30SettleQuota re-prices a finished task against what the vendor actually
// produced. Wan 3.0 accepts an out-of-range tier at submission and fails it only
// asynchronously, and smart duration means the request may carry no length at
// all -- in both cases the request is not evidence of the invoice, so usage is
// the authority.
// Returns 0 to leave the pre-charge untouched.
func Wan30SettleQuota(task *model.Task, taskResult *relaycommon.TaskInfo) int {
	if task == nil || taskResult == nil || taskResult.Status != model.TaskStatusSuccess {
		return 0
	}
	modelName := task.Properties.UpstreamModelName
	if !IsWan30(modelName) {
		modelName = task.Properties.OriginModelName
	}
	if !IsWan30(modelName) {
		return 0
	}
	bc := task.PrivateData.BillingContext
	if bc == nil || bc.ModelPrice <= 0 {
		return 0
	}

	var resp AliVideoResponse
	if len(task.Data) == 0 || common.Unmarshal(task.Data, &resp) != nil || resp.Usage == nil {
		return 0
	}

	// output_video_duration is what was rendered; usage.duration echoes the
	// request, which under smart duration says nothing useful.
	seconds := int(resp.Usage.OutputVideoDuration)
	if seconds <= 0 {
		seconds = int(resp.Usage.Duration)
	}
	if seconds <= 0 {
		return 0
	}
	if seconds > relaycommon.MaxTaskDurationSeconds {
		seconds = relaycommon.MaxTaskDurationSeconds
	}

	resRatio := 0.0
	if sr := int(resp.Usage.SR); sr > 0 {
		resRatio = wan30TierRatio(modelName, wan30TierFromSR(sr))
	} else if r := bc.OtherRatios["resolution"]; r > 0 {
		resRatio = r
	}
	if resRatio <= 0 {
		resRatio = 1
	}

	count := int(resp.Usage.VideoCount)
	if count <= 0 {
		count = 1
	}
	groupRatio := bc.GroupRatio
	if groupRatio <= 0 {
		groupRatio = 1
	}

	total := bc.ModelPrice * float64(seconds) * resRatio * float64(count)
	if total <= 0 {
		return 0
	}
	return int(total * common.QuotaPerUnit * groupRatio)
}

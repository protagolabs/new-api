package ali

import (
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// HappyHorse shares the Wan video-synthesis endpoint and request envelope, so
// it rides this adaptor. It differs in two ways that matter, both found by
// running a real request:
//
//   - it honours parameters.resolution and IGNORES parameters.size. The generic
//     path sends size for any t2v model, so a 720P request came back as a 1080P
//     video -- billed at the 720P rate, i.e. 33% under cost.
//   - its rate card is per second and resolution-dependent (720P 0.9 CNY/s,
//     1080P 1.2 CNY/s), and it reports the resolution it actually served in
//     usage.SR.
//
// Docs: https://help.aliyun.com/zh/model-studio/happyhorse-text-to-video-api-reference
const (
	// 480P is absent from the published docs, which list only 720P/1080P, but a
	// live request with resolution=480P returns usage.SR=480 -- the vendor does
	// serve it. Left in because a caller asking for the cheapest tier must not
	// silently receive (and be billed for) the most expensive one.
	HappyHorse480P  = "480P"
	HappyHorse720P  = "720P"
	HappyHorse1080P = "1080P"

	// happyHorseDefaultResolution matches what the vendor produces when the
	// request omits a resolution, so the pre-charge matches the default output.
	happyHorseDefaultResolution = HappyHorse1080P
)

// HappyHorse accepts these aspect ratios. Without a ratio the vendor defaults
// to 16:9, so a caller wanting portrait or square output must be able to say so
// -- the field was missing from AliVideoParameters entirely, making 9:16 and
// 1:1 unreachable.
const (
	HappyHorseRatio16x9 = "16:9"
	HappyHorseRatio9x16 = "9:16"
	HappyHorseRatio1x1  = "1:1"
)

// NormalizeHappyHorseRatio maps common spellings onto the vendor's accepted
// values. Returns "" for anything unrecognised, which leaves the field unset so
// the vendor applies its own default rather than rejecting the request.
func NormalizeHappyHorseRatio(v string) string {
	s := strings.TrimSpace(v)
	s = strings.ReplaceAll(s, "x", ":")
	s = strings.ReplaceAll(s, "X", ":")
	s = strings.ReplaceAll(s, "*", ":")
	switch s {
	case HappyHorseRatio16x9, HappyHorseRatio9x16, HappyHorseRatio1x1:
		return s
	}
	return ""
}

// happyHorseRatioFromSize derives an aspect ratio from a WxH size, snapping to
// the nearest value the vendor accepts. Callers routinely express orientation
// through size alone (e.g. 1080*1920 for portrait).
func happyHorseRatioFromSize(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	r := float64(w) / float64(h)
	switch {
	case r >= 1.5:
		return HappyHorseRatio16x9
	case r <= 1/1.5:
		return HappyHorseRatio9x16
	default:
		return HappyHorseRatio1x1
	}
}

// happyHorseResolutionRatios multiply ModelPrice, which is configured as the
// 720P per-second rate. 1080P is 1.2 CNY/s against 720P's 0.9.
var happyHorseResolutionRatios = map[string]float64{
	// 480P has no published rate. Priced at the 720P rate as a deliberate
	// upper bound: it cannot cost more than 720P, so this never under-charges.
	// Revise once the real figure is confirmed from the billing console.
	HappyHorse480P:  1.0,
	HappyHorse720P:  1.0,
	HappyHorse1080P: 1.2 / 0.9,
}

// IsHappyHorse reports whether a model belongs to the HappyHorse family.
// Prefix-matched so future point releases do not silently fall back to the Wan
// request shape, which this vendor ignores.
func IsHappyHorse(modelName string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(modelName)), "happyhorse")
}

// NormalizeHappyHorseResolution maps the various ways a caller can express a
// tier onto the two the vendor accepts. Returns "" when nothing usable is given.
func NormalizeHappyHorseResolution(v string) string {
	s := strings.ToUpper(strings.TrimSpace(v))
	if s == "" {
		return ""
	}
	switch s {
	case "480P", "480":
		return HappyHorse480P
	case "720P", "720":
		return HappyHorse720P
	case "1080P", "1080":
		return HappyHorse1080P
	}
	// A WxH size, which the vendor does not accept but callers still send:
	// translate rather than reject, using height as the discriminator.
	if _, h, ok := parseWidthHeight(s); ok {
		switch {
		case h >= 1080:
			return HappyHorse1080P
		case h >= 720:
			return HappyHorse720P
		default:
			return HappyHorse480P
		}
	}
	return ""
}

func parseWidthHeight(s string) (int, int, bool) {
	sep := "*"
	if !strings.Contains(s, sep) {
		sep = "X"
	}
	parts := strings.Split(s, sep)
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, err1 := atoiTrim(parts[0])
	h, err2 := atoiTrim(parts[1])
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

// applyHappyHorseParameters rewrites the resolution knobs onto the field the
// vendor reads. Called after the generic Wan mapping so it overrides whatever
// that produced.
func applyHappyHorseParameters(aliReq *AliVideoRequest, requestedSize string) {
	if aliReq.Parameters == nil {
		aliReq.Parameters = &AliVideoParameters{}
	}
	res := NormalizeHappyHorseResolution(aliReq.Parameters.Resolution)
	if res == "" {
		res = NormalizeHappyHorseResolution(requestedSize)
	}
	if res == "" {
		res = NormalizeHappyHorseResolution(aliReq.Parameters.Size)
	}
	if res == "" {
		res = happyHorseDefaultResolution
	}
	// Aspect ratio: an explicit value wins, otherwise infer orientation from the
	// size the caller sent. Resolved before Size is cleared below.
	ratio := NormalizeHappyHorseRatio(aliReq.Parameters.Ratio)
	if ratio == "" {
		ratio = NormalizeHappyHorseRatio(requestedSize)
	}
	if ratio == "" {
		for _, cand := range []string{requestedSize, aliReq.Parameters.Size} {
			if w, h, ok := parseWidthHeight(strings.ToUpper(cand)); ok {
				ratio = happyHorseRatioFromSize(w, h)
				break
			}
		}
	}
	// Left empty when undeterminable so the vendor applies its own default
	// rather than rejecting an unknown value.
	aliReq.Parameters.Ratio = ratio

	aliReq.Parameters.Resolution = res
	// size is not merely ignored -- leaving it set risks the vendor rejecting an
	// unknown field, and it would misreport what we charged for.
	aliReq.Parameters.Size = ""
}

// happyHorseRatios prices a request as ModelPrice x seconds x resolution.
func happyHorseRatios(seconds int, resolution string) map[string]float64 {
	res := NormalizeHappyHorseResolution(resolution)
	if res == "" {
		res = happyHorseDefaultResolution
	}
	ratio, ok := happyHorseResolutionRatios[res]
	if !ok {
		ratio = 1.0
	}
	return map[string]float64{
		"seconds":    float64(seconds),
		"resolution": ratio,
	}
}

// HappyHorseSettleQuota re-prices a finished task against usage.SR and
// usage.duration -- what the vendor actually produced and billed. This is the
// safety net for the vendor ignoring requested parameters, which is exactly how
// the 720P-request/1080P-delivery mismatch was found.
// Returns 0 to leave the pre-charge untouched.
func HappyHorseSettleQuota(task *model.Task, taskResult *relaycommon.TaskInfo) int {
	if task == nil || taskResult == nil || taskResult.Status != model.TaskStatusSuccess {
		return 0
	}
	if !IsHappyHorse(task.Properties.OriginModelName) && !IsHappyHorse(task.Properties.UpstreamModelName) {
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

	seconds := int(resp.Usage.Duration)
	if seconds <= 0 {
		return 0
	}
	if seconds > relaycommon.MaxTaskDurationSeconds {
		seconds = relaycommon.MaxTaskDurationSeconds
	}

	// usage.SR is the vertical resolution actually rendered (720 / 1080).
	resRatio := 1.0
	if sr := int(resp.Usage.SR); sr > 0 {
		var tier string
		switch {
		case sr >= 1080:
			tier = HappyHorse1080P
		case sr >= 720:
			tier = HappyHorse720P
		default:
			tier = HappyHorse480P
		}
		resRatio = happyHorseResolutionRatios[tier]
	} else if r := bc.OtherRatios["resolution"]; r > 0 {
		resRatio = r
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

func atoiTrim(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}

// AdjustBillingOnComplete settles a finished task against what the vendor
// actually produced. Non-HappyHorse models return 0 and keep the pre-charge,
// which is the behaviour they have always had.
func (a *TaskAdaptor) AdjustBillingOnComplete(task *model.Task, taskResult *relaycommon.TaskInfo) int {
	return HappyHorseSettleQuota(task, taskResult)
}

// SettlesPerCallOnComplete opts into completion-time settlement even though
// video models are priced via ModelPrice. Harmless for Wan models: their
// settlement returns 0, leaving the pre-charge untouched.
// Implements service.PerCallSettlementAdaptor.
func (a *TaskAdaptor) SettlesPerCallOnComplete() bool { return true }

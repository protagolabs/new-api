package ali

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// realWan30Body is an actual completed-task body from the international
// endpoint. output_video_duration is what to bill; usage.duration merely echoes
// the request and says nothing under smart duration.
const realWan30Body = `{
  "output": {
    "task_id": "a1609cc2-9c85-4c8f-8022-726e829aa2d1",
    "task_status": "SUCCEEDED",
    "video_url": "https://dashscope-a717.oss-accelerate.aliyuncs.com/1d/6f/202.mp4"
  },
  "request_id": "d674265f-4867-9aed-ac5f-00ffbaac0610",
  "usage": {"duration": 5, "input_video_duration": 0, "output_video_duration": 5,
            "fps": 30, "video_count": 1, "SR": 480, "ratio": "16:9"}
}`

func TestIsWan30(t *testing.T) {
	for _, m := range []string{"wan3.0-video", "wan3.0-video-prime", " WAN3.0-Video ", "wan3.0-video-2609"} {
		if !IsWan30(m) {
			t.Errorf("IsWan30(%q) = false, want true", m)
		}
	}
	for _, m := range []string{"wan2.7-t2v", "happyhorse-1.1-t2v", "wan2.5-i2v-preview", ""} {
		if IsWan30(m) {
			t.Errorf("IsWan30(%q) = true, want false", m)
		}
	}
	// Prime must be distinguishable: its 720P/1080P are 2.06x/4.12x rather than
	// the standard 2x/4x, so conflating them misprices every non-480P render.
	if !isWan30Prime("wan3.0-video-prime") {
		t.Error("prime not recognised")
	}
	if isWan30Prime("wan3.0-video") {
		t.Error("standard model misread as prime")
	}
}

func TestNormalizeWan30Resolution(t *testing.T) {
	cases := map[string]string{
		"480p": Wan30Resolution480P, "480": Wan30Resolution480P,
		"720P": Wan30Resolution720P, "1080": Wan30Resolution1080P,
		"1920*1080": Wan30Resolution1080P,
		"1280*720":  Wan30Resolution720P,
		"832*480":   Wan30Resolution480P,
		// Portrait: the tier follows the short edge. Keying on height would call
		// this 1080P and overcharge it 2x.
		"720*1280":  Wan30Resolution720P,
		"1080*1920": Wan30Resolution1080P,
		"":          "",
		"garbage":   "",
	}
	for in, want := range cases {
		if got := NormalizeWan30Resolution(in); got != want {
			t.Errorf("NormalizeWan30Resolution(%q) = %q, want %q", in, got, want)
		}
	}
}

// The vendor defaults an absent resolution to 1080P -- the most expensive tier.
// The generic Wan mapping hands unknown models 720P, so without this override
// an omitted resolution would be pre-charged at half its real cost.
func TestApplyWan30ParametersDefaultsToVendorDefault(t *testing.T) {
	req := &AliVideoRequest{Model: "wan3.0-video", Parameters: &AliVideoParameters{Resolution: "720P"}}
	applyWan30Parameters(req, "")
	if req.Parameters.Resolution != Wan30Resolution720P {
		t.Errorf("explicit tier overwritten: %q", req.Parameters.Resolution)
	}

	req = &AliVideoRequest{Model: "wan3.0-video", Parameters: &AliVideoParameters{}}
	applyWan30Parameters(req, "")
	if req.Parameters.Resolution != Wan30Resolution1080P {
		t.Errorf("absent tier = %q, want vendor default 1080P", req.Parameters.Resolution)
	}

	// size is translated then cleared: the omni API takes resolution only, and a
	// leftover size would misreport the tier we charged for.
	req = &AliVideoRequest{Model: "wan3.0-video", Parameters: &AliVideoParameters{Size: "1280*720"}}
	applyWan30Parameters(req, "")
	if req.Parameters.Resolution != Wan30Resolution720P || req.Parameters.Size != "" {
		t.Errorf("size handling: resolution=%q size=%q", req.Parameters.Resolution, req.Parameters.Size)
	}
}

func TestWan30TierRatios(t *testing.T) {
	// Standard: $0.05 / $0.10 / $0.20 per second.
	for tier, want := range map[string]float64{
		Wan30Resolution480P: 1, Wan30Resolution720P: 2, Wan30Resolution1080P: 4,
	} {
		if got := wan30TierRatio("wan3.0-video", tier); got != want {
			t.Errorf("standard %s: got %v, want %v", tier, got, want)
		}
	}
	// Prime: $0.068 / $0.14 / $0.28 -- deliberately not a flat multiple of the
	// standard table.
	if got, want := wan30TierRatio("wan3.0-video-prime", Wan30Resolution720P), 0.14/0.068; got != want {
		t.Errorf("prime 720P: got %v, want %v", got, want)
	}
	if got, want := wan30TierRatio("wan3.0-video-prime", Wan30Resolution1080P), 0.28/0.068; got != want {
		t.Errorf("prime 1080P: got %v, want %v", got, want)
	}
	// An unknown tier must not silently become a free 1080P render.
	if got := wan30TierRatio("wan3.0-video", ""); got != 4 {
		t.Errorf("empty tier = %v, want the 1080P default 4", got)
	}
}

// Smart duration carries no length in the request, so the pre-charge takes the
// upstream ceiling; settlement refunds the difference. Billing the 5s default
// would undercharge a 30s render sixfold.
func TestWan30BillableSeconds(t *testing.T) {
	if got := wan30BillableSeconds(relaycommon.AutoTaskDuration); got != wan30MaxDuration {
		t.Errorf("auto duration = %d, want %d", got, wan30MaxDuration)
	}
	if got := wan30BillableSeconds(0); got != wan30DefaultDuration {
		t.Errorf("unset duration = %d, want %d", got, wan30DefaultDuration)
	}
	if got := wan30BillableSeconds(12); got != 12 {
		t.Errorf("explicit duration = %d, want 12", got)
	}
}

func wan30Task(t *testing.T, modelName string, price float64, data string) *model.Task {
	t.Helper()
	return &model.Task{
		Data:       json.RawMessage(data),
		Properties: model.Properties{OriginModelName: modelName},
		PrivateData: model.TaskPrivateData{
			BillingContext: &model.TaskBillingContext{
				ModelPrice: price, GroupRatio: 1,
				OtherRatios: map[string]float64{"seconds": 30, "resolution": 4},
			},
		},
	}
}

// Wan 3.0 accepts an out-of-range tier at submission and fails it only
// asynchronously, so the request is never proof of what was delivered. Settle on
// usage.SR and usage.output_video_duration.
func TestWan30SettleQuota(t *testing.T) {
	a := &TaskAdaptor{}
	ok := &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}

	// Pre-charged as 30s @1080P (smart duration), vendor rendered 5s @480P:
	// 0.05 * 5 * 1 = $0.25, i.e. the over-charge is refunded.
	got := a.AdjustBillingOnComplete(wan30Task(t, "wan3.0-video", 0.05, realWan30Body), ok)
	if want := int(0.05 * 5 * 1 * common.QuotaPerUnit); got != want {
		t.Errorf("480P/5s: got %d, want %d", got, want)
	}

	// 1080P for 8s => 0.05 * 8 * 4 = $1.60
	body := `{"output":{"task_status":"SUCCEEDED"},"usage":{"SR":1080,"output_video_duration":8,"video_count":1}}`
	got = a.AdjustBillingOnComplete(wan30Task(t, "wan3.0-video", 0.05, body), ok)
	if want := int(0.05 * 8 * 4 * common.QuotaPerUnit); got != want {
		t.Errorf("1080P/8s: got %d, want %d", got, want)
	}

	// Prime is priced off its own table: 0.068 * 5 * (0.28/0.068) = $1.40
	primeBody := `{"output":{"task_status":"SUCCEEDED"},"usage":{"SR":1080,"output_video_duration":5,"video_count":1}}`
	got = a.AdjustBillingOnComplete(wan30Task(t, "wan3.0-video-prime", 0.068, primeBody), ok)
	if want := int(0.068 * 5 * (0.28 / 0.068) * common.QuotaPerUnit); got != want {
		t.Errorf("prime 1080P/5s: got %d, want %d", got, want)
	}

	// Falls back to usage.duration when output_video_duration is absent, so an
	// older response shape still settles rather than keeping a 30s pre-charge.
	legacy := `{"output":{"task_status":"SUCCEEDED"},"usage":{"SR":480,"duration":6,"video_count":1}}`
	got = a.AdjustBillingOnComplete(wan30Task(t, "wan3.0-video", 0.05, legacy), ok)
	if want := int(0.05 * 6 * 1 * common.QuotaPerUnit); got != want {
		t.Errorf("legacy duration: got %d, want %d", got, want)
	}

	// A failed task must not be re-priced -- refunds are the caller's job.
	failed := &relaycommon.TaskInfo{Status: model.TaskStatusFailure}
	if got := a.AdjustBillingOnComplete(wan30Task(t, "wan3.0-video", 0.05, realWan30Body), failed); got != 0 {
		t.Errorf("failed task re-priced: %d", got)
	}

	// Other Wan models keep the pre-charge they have always had.
	if got := a.AdjustBillingOnComplete(wan30Task(t, "wan2.7-t2v", 0.05, realWan30Body), ok); got != 0 {
		t.Errorf("non-wan3.0 model settled: %d", got)
	}
}

// -1 must survive request conversion for wan3.0 and must not for models whose
// upstream rejects it; a swallowed sentinel silently becomes a fixed 5 seconds.
func TestWan30AutoDurationSurvivesConversion(t *testing.T) {
	a := &TaskAdaptor{}
	info := testRelayInfo()

	req, err := a.convertToAliRequest(info, relaycommon.TaskSubmitReq{
		Model: "wan3.0-video", Prompt: "a cat", Duration: relaycommon.AutoTaskDuration,
	})
	if err != nil {
		t.Fatalf("convert failed: %v", err)
	}
	if req.Parameters.Duration != relaycommon.AutoTaskDuration {
		t.Errorf("wan3.0 auto duration = %d, want %d", req.Parameters.Duration, relaycommon.AutoTaskDuration)
	}

	req, err = a.convertToAliRequest(info, relaycommon.TaskSubmitReq{
		Model: "wan2.7-t2v", Prompt: "a cat", Duration: relaycommon.AutoTaskDuration,
	})
	if err != nil {
		t.Fatalf("convert failed: %v", err)
	}
	if req.Parameters.Duration != 5 {
		t.Errorf("wan2.7 auto duration = %d, want the 5s default", req.Parameters.Duration)
	}
}

// Audio rides input.media under a different type name per family. Sending
// wan2.7's driving_audio to wan3.0 would be an unknown type.
func TestWan30MediaAudioType(t *testing.T) {
	if got := mediaAudioType("wan3.0-video"); got != "reference_audio" {
		t.Errorf("wan3.0 audio type = %q, want reference_audio", got)
	}
	if got := mediaAudioType("wan2.7-i2v"); got != "driving_audio" {
		t.Errorf("wan2.7 audio type = %q, want driving_audio", got)
	}
	// HappyHorse's audio home is unverified: leave the flat field alone.
	if got := mediaAudioType("happyhorse-1.1-t2v"); got != "" {
		t.Errorf("happyhorse audio type = %q, want empty", got)
	}
}

// Text-to-video is a normal case for wan3.0, so an absent image must not error
// the way it does for wan2.7-i2v.
func TestWan30TextToVideoNeedsNoImage(t *testing.T) {
	req := &AliVideoRequest{Model: "wan3.0-video", Input: AliVideoInput{Prompt: "a cat"}}
	if err := normalizeMediaProtocolInput(req, relaycommon.TaskSubmitReq{}); err != nil {
		t.Fatalf("t2v rejected: %v", err)
	}
	if len(req.Input.Media) != 0 {
		t.Errorf("media invented for t2v: %+v", req.Input.Media)
	}
}

// A caller-supplied image becomes a first_frame entry and the flat field is
// cleared -- sending both is rejected upstream.
func TestWan30ImageMovesToMedia(t *testing.T) {
	req := &AliVideoRequest{Model: "wan3.0-video", Input: AliVideoInput{
		Prompt: "a cat", ImgURL: "https://example.com/a.png",
	}}
	if err := normalizeMediaProtocolInput(req, relaycommon.TaskSubmitReq{}); err != nil {
		t.Fatalf("i2v rejected: %v", err)
	}
	if len(req.Input.Media) != 1 || req.Input.Media[0].Type != "first_frame" {
		t.Fatalf("media = %+v", req.Input.Media)
	}
	if req.Input.ImgURL != "" {
		t.Errorf("flat img_url left set: %q", req.Input.ImgURL)
	}
}

// Explicit omni references pass through untouched: reference_* types are
// mutually exclusive with first_frame/last_frame upstream, so the adaptor must
// not add frames of its own to a request that already carries media.
func TestWan30OmniReferencePassthrough(t *testing.T) {
	req := &AliVideoRequest{Model: "wan3.0-video", Input: AliVideoInput{
		Prompt: "the person in Image 1 walks past Video 1",
		Media: []AliVideoMedia{
			{Type: "reference_image", URL: "https://example.com/a.png"},
			{Type: "reference_video", URL: "https://example.com/b.mp4"},
			{Type: "reference_audio", URL: "https://example.com/c.mp3"},
		},
	}}
	if err := normalizeMediaProtocolInput(req, relaycommon.TaskSubmitReq{
		Image: "https://example.com/should-be-ignored.png",
	}); err != nil {
		t.Fatalf("omni request rejected: %v", err)
	}
	if len(req.Input.Media) != 3 {
		t.Fatalf("media mutated: %+v", req.Input.Media)
	}
	for _, m := range req.Input.Media {
		if m.Type == "first_frame" || m.Type == "last_frame" {
			t.Errorf("frame type injected alongside reference_*: %+v", req.Input.Media)
		}
	}
}

// ProcessAliOtherRatios must price wan3.0 by tier. Falling through to the Wan
// rate table, which has no wan3.0 entry, would leave every tier at 1.0 and
// undercharge 1080P by 4x.
func TestProcessAliOtherRatiosWan30(t *testing.T) {
	ratios, err := ProcessAliOtherRatios(&AliVideoRequest{
		Model: "wan3.0-video", Parameters: &AliVideoParameters{Resolution: "1080P"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := ratios["resolution"]; got != 4 {
		t.Errorf("1080P resolution ratio = %v, want 4", got)
	}

	ratios, err = ProcessAliOtherRatios(&AliVideoRequest{
		Model: "wan3.0-video-prime", Parameters: &AliVideoParameters{Resolution: "720P"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := ratios["resolution"], 0.14/0.068; got != want {
		t.Errorf("prime 720P ratio = %v, want %v", got, want)
	}
}

package ali

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// realHappyHorseBody is an actual completed-task body. Note usage.SR = 1080
// even though the request asked for 720P via size -- the vendor ignores size,
// which is what makes settling on usage rather than the request mandatory.
const realHappyHorseBody = `{
  "output": {
    "task_id": "3d97b861-d81e-4239-ac7c-5666698047f8",
    "task_status": "SUCCEEDED",
    "video_url": "https://dashscope-463f.oss-accelerate.aliyuncs.com/x/video_1080p.mp4"
  },
  "request_id": "0b02567e-92ad-93b1-8fda-256280d1f5b5",
  "usage": {"SR": 1080, "duration": 5, "input_video_duration": 0,
            "output_video_duration": 5, "ratio": "16:9", "video_count": 1}
}`

func TestIsHappyHorse(t *testing.T) {
	for _, m := range []string{"happyhorse-1.1-t2v", "HappyHorse-1.0-i2v", " happyhorse-1.2-r2v "} {
		if !IsHappyHorse(m) {
			t.Errorf("%q should be HappyHorse", m)
		}
	}
	// Wan models must keep the existing behaviour untouched.
	for _, m := range []string{"wan2.7-t2v", "wan2.5-i2v-preview", "", "wanx2.1-i2v-plus"} {
		if IsHappyHorse(m) {
			t.Errorf("%q must not be treated as HappyHorse", m)
		}
	}
}

func TestNormalizeHappyHorseResolution(t *testing.T) {
	for in, want := range map[string]string{
		"480P": HappyHorse480P, "480p": HappyHorse480P, "480": HappyHorse480P,
		"720P": HappyHorse720P, "720p": HappyHorse720P, "720": HappyHorse720P,
		"1080P": HappyHorse1080P, "1080": HappyHorse1080P,
		// Callers still send Wan-style sizes; translate instead of rejecting.
		"1280*720": HappyHorse720P, "1920*1080": HappyHorse1080P, "1080*1920": HappyHorse1080P,
		"854*480": HappyHorse480P, "640*360": HappyHorse480P,
		"": "", "garbage": "",
	} {
		if got := NormalizeHappyHorseResolution(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// The generic Wan path sets parameters.size for any t2v model. HappyHorse
// ignores size, so a 720P request silently produced a 1080P video billed at the
// 720P rate. The rewrite must move the tier onto resolution and clear size.
func TestApplyHappyHorseParameters(t *testing.T) {
	cases := []struct {
		name    string
		params  *AliVideoParameters
		reqSize string
		wantRes string
	}{
		{"wan-style size gets translated", &AliVideoParameters{Size: "1280*720"}, "", HappyHorse720P},
		{"caller asked 1080P", &AliVideoParameters{}, "1080P", HappyHorse1080P},
		{"caller asked 720P", &AliVideoParameters{}, "720P", HappyHorse720P},
		// A 480P request used to fall through to the 1080P default: the caller
		// got the most expensive tier and was billed for it. Live-confirmed the
		// vendor does serve 480P (usage.SR=480) despite the docs omitting it.
		{"caller asked 480P must not be upgraded", &AliVideoParameters{}, "480P", HappyHorse480P},
		{"480P via parameters.resolution", &AliVideoParameters{Resolution: "480P"}, "", HappyHorse480P},
		{"explicit resolution wins", &AliVideoParameters{Resolution: "720P", Size: "1920*1080"}, "", HappyHorse720P},
		// Nothing specified must match what the vendor actually defaults to,
		// or the pre-charge is wrong from the start.
		{"default matches vendor default", &AliVideoParameters{}, "", HappyHorse1080P},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &AliVideoRequest{Model: "happyhorse-1.1-t2v", Parameters: tc.params}
			applyHappyHorseParameters(req, tc.reqSize)
			if req.Parameters.Resolution != tc.wantRes {
				t.Errorf("resolution: got %q, want %q", req.Parameters.Resolution, tc.wantRes)
			}
			if req.Parameters.Size != "" {
				t.Errorf("size must be cleared, got %q", req.Parameters.Size)
			}
		})
	}
}

// ratio was entirely absent from AliVideoParameters, so 9:16 and 1:1 were
// unreachable and every video came back 16:9.
func TestHappyHorseRatio(t *testing.T) {
	for in, want := range map[string]string{
		"16:9": HappyHorseRatio16x9, "9:16": HappyHorseRatio9x16, "1:1": HappyHorseRatio1x1,
		"16x9": HappyHorseRatio16x9, "9X16": HappyHorseRatio9x16,
		// Unrecognised values must yield "" so the field is left unset and the
		// vendor applies its own default, rather than being rejected outright.
		"21:9": "", "adaptive": "", "": "",
	} {
		if got := NormalizeHappyHorseRatio(in); got != want {
			t.Errorf("ratio %q: got %q, want %q", in, got, want)
		}
	}

	// Callers commonly express orientation through size alone.
	for in, want := range map[string]string{
		"1920*1080": HappyHorseRatio16x9,
		"1080*1920": HappyHorseRatio9x16,
		"1024*1024": HappyHorseRatio1x1,
		"1280*720":  HappyHorseRatio16x9,
	} {
		w, h, ok := parseWidthHeight(in)
		if !ok {
			t.Fatalf("parse %q failed", in)
		}
		if got := happyHorseRatioFromSize(w, h); got != want {
			t.Errorf("size %q -> ratio: got %q, want %q", in, got, want)
		}
	}

	// End to end through the rewrite: explicit ratio wins, size infers, and an
	// undeterminable one stays empty.
	cases := []struct {
		name    string
		params  *AliVideoParameters
		reqSize string
		want    string
	}{
		{"explicit 9:16", &AliVideoParameters{Ratio: "9:16"}, "", HappyHorseRatio9x16},
		{"inferred from portrait size", &AliVideoParameters{}, "1080*1920", HappyHorseRatio9x16},
		{"inferred from square size", &AliVideoParameters{}, "1024*1024", HappyHorseRatio1x1},
		{"explicit beats size", &AliVideoParameters{Ratio: "1:1"}, "1920*1080", HappyHorseRatio1x1},
		{"nothing to go on -> unset", &AliVideoParameters{}, "720P", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &AliVideoRequest{Model: "happyhorse-1.1-t2v", Parameters: tc.params}
			applyHappyHorseParameters(req, tc.reqSize)
			if req.Parameters.Ratio != tc.want {
				t.Errorf("ratio: got %q, want %q", req.Parameters.Ratio, tc.want)
			}
		})
	}
}

// ModelPrice is the 720P per-second rate; 1080P is 1.2 CNY/s against 0.9.
func TestHappyHorseRatios(t *testing.T) {
	r := happyHorseRatios(5, HappyHorse720P)
	if r["seconds"] != 5 || r["resolution"] != 1.0 {
		t.Errorf("720P: %+v", r)
	}
	r = happyHorseRatios(10, HappyHorse1080P)
	if math.Abs(r["resolution"]-1.2/0.9) > 1e-9 {
		t.Errorf("1080P ratio: %+v", r)
	}
}

// HappyHorse must not fall through to the Wan rate table, which has no entry
// for it and would therefore apply no resolution multiplier at all.
func TestProcessAliOtherRatiosHappyHorse(t *testing.T) {
	req := &AliVideoRequest{Model: "happyhorse-1.1-t2v",
		Parameters: &AliVideoParameters{Resolution: "1080P"}}
	got, err := ProcessAliOtherRatios(req)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got["resolution"]-1.2/0.9) > 1e-9 {
		t.Errorf("1080P: %+v", got)
	}

	req.Parameters.Resolution = "720P"
	got, _ = ProcessAliOtherRatios(req)
	if got["resolution"] != 1.0 {
		t.Errorf("720P: %+v", got)
	}

	// A Wan model must still get its own table entry, untouched.
	wan := &AliVideoRequest{Model: "wan2.2-i2v-flash",
		Parameters: &AliVideoParameters{Resolution: "720P"}}
	got, _ = ProcessAliOtherRatios(wan)
	if got["resolution-720P"] != 2 {
		t.Errorf("wan ratios changed: %+v", got)
	}
}

func hhTask(t *testing.T, modelName string, price float64, data string) *model.Task {
	t.Helper()
	return &model.Task{
		Data:       json.RawMessage(data),
		Properties: model.Properties{OriginModelName: modelName},
		PrivateData: model.TaskPrivateData{
			BillingContext: &model.TaskBillingContext{
				ModelPrice: price, GroupRatio: 1,
				OtherRatios: map[string]float64{"seconds": 5, "resolution": 1},
			},
		},
	}
}

// The vendor reports what it rendered in usage.SR / usage.duration. Settling on
// that is the safety net for it ignoring the requested parameters -- exactly the
// case that produced a 1080P video for a 720P charge.
func TestHappyHorseSettleQuota(t *testing.T) {
	a := &TaskAdaptor{}
	ok := &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}

	// Charged at 720P (ratio 1), vendor delivered 1080P for 5s:
	// 0.125 * 5 * (1.2/0.9) = $0.8333
	got := a.AdjustBillingOnComplete(hhTask(t, "happyhorse-1.1-t2v", 0.125, realHappyHorseBody), ok)
	want := int(0.125 * 5 * (1.2 / 0.9) * common.QuotaPerUnit)
	if got != want {
		t.Errorf("1080P delivery: got %d, want %d", got, want)
	}

	// Vendor delivered 720P for 8s => 0.125 * 8 * 1 = $1.00
	body720 := `{"output":{"task_status":"SUCCEEDED"},"usage":{"SR":720,"duration":8,"video_count":1}}`
	got = a.AdjustBillingOnComplete(hhTask(t, "happyhorse-1.1-t2v", 0.125, body720), ok)
	if want := int(1.00 * common.QuotaPerUnit); got != want {
		t.Errorf("720P delivery: got %d, want %d", got, want)
	}

	// Multiple videos are billed per video.
	body2 := `{"output":{"task_status":"SUCCEEDED"},"usage":{"SR":720,"duration":5,"video_count":2}}`
	got = a.AdjustBillingOnComplete(hhTask(t, "happyhorse-1.1-t2v", 0.125, body2), ok)
	if want := int(0.125 * 5 * 2 * common.QuotaPerUnit); got != want {
		t.Errorf("video_count=2: got %d, want %d", got, want)
	}

	// A 480P delivery must settle at the 480P tier, not be lumped into 720P
	// or (worse) 1080P.
	body480 := `{"output":{"task_status":"SUCCEEDED"},"usage":{"SR":480,"duration":5,"video_count":1}}`
	got = a.AdjustBillingOnComplete(hhTask(t, "happyhorse-1.1-t2v", 0.125, body480), ok)
	if want := int(0.125 * 5 * happyHorseResolutionRatios[HappyHorse480P] * common.QuotaPerUnit); got != want {
		t.Errorf("480P delivery: got %d, want %d", got, want)
	}

	// Wan models must be left completely alone.
	if got := a.AdjustBillingOnComplete(hhTask(t, "wan2.7-t2v", 0.125, realHappyHorseBody), ok); got != 0 {
		t.Errorf("wan model must not be re-priced, got %d", got)
	}

	// No-ops that must not panic or charge.
	for name, task := range map[string]*model.Task{
		"no usage":   hhTask(t, "happyhorse-1.1-t2v", 0.125, `{"output":{"task_status":"SUCCEEDED"}}`),
		"zero secs":  hhTask(t, "happyhorse-1.1-t2v", 0.125, `{"usage":{"SR":720,"duration":0}}`),
		"empty data": hhTask(t, "happyhorse-1.1-t2v", 0.125, `{}`),
	} {
		if got := a.AdjustBillingOnComplete(task, ok); got != 0 {
			t.Errorf("%s should be a no-op, got %d", name, got)
		}
	}
	if got := a.AdjustBillingOnComplete(nil, ok); got != 0 {
		t.Errorf("nil task: %d", got)
	}
	if got := a.AdjustBillingOnComplete(hhTask(t, "happyhorse-1.1-t2v", 0.125, realHappyHorseBody),
		&relaycommon.TaskInfo{Status: model.TaskStatusFailure}); got != 0 {
		t.Errorf("failure goes through refund, not re-pricing: %d", got)
	}
	noBC := hhTask(t, "happyhorse-1.1-t2v", 0.125, realHappyHorseBody)
	noBC.PrivateData.BillingContext = nil
	if got := a.AdjustBillingOnComplete(noBC, ok); got != 0 {
		t.Errorf("missing billing context: %d", got)
	}

	// A hostile duration must not become an unbounded billing multiplier.
	huge := `{"usage":{"SR":720,"duration":99999,"video_count":1}}`
	got = a.AdjustBillingOnComplete(hhTask(t, "happyhorse-1.1-t2v", 0.125, huge), ok)
	capped := int(0.125 * float64(relaycommon.MaxTaskDurationSeconds) * common.QuotaPerUnit)
	if got != capped {
		t.Errorf("duration must be capped: got %d, want %d", got, capped)
	}
}

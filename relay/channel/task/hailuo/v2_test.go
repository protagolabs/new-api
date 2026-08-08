package hailuo

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// realQueryBody is an actual response captured from the vendor. Every parsing
// assumption below is anchored to it rather than to the docs, which describe a
// flat object and a file_id round trip that do not exist in practice.
const realQueryBody = `{
  "items": [
    {
      "id": "428463094829280",
      "model": "MiniMax-H3",
      "status": "succeeded",
      "created_at": 1786158604,
      "updated_at": 1786158716,
      "content": {"url": "https://video-product.cdn.minimax.io/inference_output/x/output.mp4"},
      "resolution": "768P",
      "duration": 5,
      "usage": {"total_seconds": 5, "input_seconds": 0, "output_seconds": 5},
      "ratio": "16:9",
      "task_type": "generation"
    }
  ],
  "total": 1
}`

func TestIsV2Model(t *testing.T) {
	for _, m := range []string{"MiniMax-H3", "minimax-h3", " MiniMax-H3-Regeneration ", "MiniMax-H3-20260731"} {
		if !IsV2Model(m) {
			t.Errorf("%q should route to v2", m)
		}
	}
	// v1 models must keep the old protocol, or they break.
	for _, m := range []string{"MiniMax-Hailuo-2.3", "MiniMax-Hailuo-02", "T2V-01", "", "abab6.5-chat"} {
		if IsV2Model(m) {
			t.Errorf("%q should stay on v1", m)
		}
	}
}

// The query endpoint answers with recent tasks and ignores the task_id
// parameter, so reading items[0] would report someone else's status. Observed
// live: a freshly submitted task's query returned only the previous task,
// already succeeded -- which would have been read as instant success.
func TestParseV2ResultPicksOurTask(t *testing.T) {
	twoTasks := `{"items":[
		{"id":"111","status":"processing"},
		{"id":"222","status":"succeeded","content":{"url":"https://x/y.mp4"},"duration":5}
	],"total":2}`

	cases := []struct {
		name       string
		body       string
		wantID     string
		wantStatus string
		wantURL    string
	}{
		{"real body, our task", realQueryBody, "428463094829280", model.TaskStatusSuccess,
			"https://video-product.cdn.minimax.io/inference_output/x/output.mp4"},
		{"our task absent -> keep polling", realQueryBody, "999999", model.TaskStatusInProgress, ""},
		{"pick ours out of several", twoTasks, "222", model.TaskStatusSuccess, "https://x/y.mp4"},
		{"the other one is still running", twoTasks, "111", model.TaskStatusInProgress, ""},
		{"failed", `{"items":[{"id":"1","status":"failed","status_msg":"blocked"}],"total":1}`, "1",
			model.TaskStatusFailure, ""},
		{"unknown status keeps polling", `{"items":[{"id":"1","status":"brand_new"}],"total":1}`, "1",
			model.TaskStatusInProgress, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := parseV2Result([]byte(tc.body), tc.wantID)
			if err != nil {
				t.Fatal(err)
			}
			if info.Status != tc.wantStatus {
				t.Errorf("status: got %v, want %v", info.Status, tc.wantStatus)
			}
			// Url drives the polling loop's result choice; RemoteUrl is read by
			// the proxy. Both must carry the vendor URL or the caller gets a
			// locally built proxy address instead.
			if info.Url != tc.wantURL {
				t.Errorf("Url: got %q, want %q", info.Url, tc.wantURL)
			}
			if tc.wantURL != "" && info.RemoteUrl != tc.wantURL {
				t.Errorf("RemoteUrl: got %q, want %q", info.RemoteUrl, tc.wantURL)
			}
		})
	}

	// A vendor-level error must surface rather than be read as "not found yet".
	info, err := parseV2Result([]byte(`{"base_resp":{"status_code":1008,"status_msg":"insufficient balance"}}`), "1")
	if err != nil {
		t.Fatal(err)
	}
	if info.Status != model.TaskStatusFailure || info.Reason != "insufficient balance" {
		t.Errorf("vendor error not surfaced: %+v", info)
	}
}

func TestV2RequestBuilding(t *testing.T) {
	// Text-to-video: ratio is required and content[] carries a single text item.
	req := relaycommon.TaskSubmitReq{Prompt: "a boat", Duration: 5}
	got, err := buildV2Request(&req, "MiniMax-H3")
	if err != nil {
		t.Fatal(err)
	}
	if got.Ratio != "16:9" {
		t.Errorf("t2v must carry a ratio, got %q", got.Ratio)
	}
	if len(got.Content) != 1 || got.Content[0].Type != "text" {
		t.Errorf("content: %+v", got.Content)
	}
	if got.Resolution != V2Resolution768P {
		t.Errorf("resolution: got %q", got.Resolution)
	}

	// Image-to-video: the image goes into content[] before the text, and ratio
	// is omitted so it cannot conflict with the input image.
	req = relaycommon.TaskSubmitReq{Prompt: "make her turn", Image: "https://i/a.jpg", Duration: 6}
	got, _ = buildV2Request(&req, "MiniMax-H3")
	if got.Ratio != "" {
		t.Errorf("i2v must not send a ratio, got %q", got.Ratio)
	}
	if len(got.Content) != 2 || got.Content[0].Type != "image_url" || got.Content[1].Type != "text" {
		t.Fatalf("content order wrong: %+v", got.Content)
	}
	if got.Content[0].ImageURL == nil || got.Content[0].ImageURL.URL != "https://i/a.jpg" {
		t.Errorf("image url not carried: %+v", got.Content[0])
	}

	// A prompt is mandatory upstream; failing here beats a 400 later.
	if _, err := buildV2Request(&relaycommon.TaskSubmitReq{Duration: 5}, "MiniMax-H3"); err == nil {
		t.Error("empty prompt should be rejected")
	}

	// Duration is clamped: out of range is a hard upstream rejection, and an
	// unbounded value would also be an unbounded billing multiplier.
	for in, want := range map[int]int{0: 5, 1: 4, 4: 4, 15: 15, 99: 15} {
		r := relaycommon.TaskSubmitReq{Prompt: "x", Duration: in}
		if got := v2ResolveDuration(&r); got != want {
			t.Errorf("duration %d: got %d, want %d", in, got, want)
		}
	}
}

func TestV2ResolutionAndRatio(t *testing.T) {
	for in, want := range map[string]string{
		"2K": V2Resolution2K, "1440p": V2Resolution2K, "768P": V2Resolution768P,
		"1920x1080": V2Resolution2K, "1280x720": V2Resolution768P, "": V2Resolution768P,
	} {
		if got := v2ResolveResolution("", in); got != want {
			t.Errorf("resolution %q: got %q, want %q", in, got, want)
		}
	}
	// Explicit metadata wins over an inferred size.
	if got := v2ResolveResolution("2K", "1280x720"); got != V2Resolution2K {
		t.Errorf("explicit resolution should win, got %q", got)
	}
	for in, want := range map[string]string{
		"1920x1080": "16:9", "1080x1920": "9:16", "1024x1024": "1:1", "garbage": "16:9",
	} {
		if got := v2ResolveRatio(in); got != want {
			t.Errorf("ratio %q: got %q, want %q", in, got, want)
		}
	}
}

// ModelPrice is the 768P per-second rate; 2K is $0.13/s against $0.08/s.
func TestV2BillingRatios(t *testing.T) {
	r := V2BillingRatios(5, V2Resolution768P, 0)
	if r["seconds"] != 5 || r["resolution"] != 1.0 {
		t.Errorf("768P 5s: %+v", r)
	}
	r = V2BillingRatios(10, V2Resolution2K, 0)
	if r["seconds"] != 10 || math.Abs(r["resolution"]-1.625) > 1e-9 {
		t.Errorf("2K 10s: %+v", r)
	}
	// Reference footage is billed at the output tier rate, so it adds seconds
	// rather than a separate multiplier.
	r = V2BillingRatios(5, V2Resolution768P, 3)
	if r["seconds"] != 8 {
		t.Errorf("input video should add seconds: %+v", r)
	}
}

func v2Task(t *testing.T, modelName string, price float64, data string) *model.Task {
	t.Helper()
	tk := &model.Task{
		Data:       json.RawMessage(data),
		Properties: model.Properties{OriginModelName: modelName},
		PrivateData: model.TaskPrivateData{
			BillingContext: &model.TaskBillingContext{
				ModelPrice: price, GroupRatio: 1,
				OtherRatios: map[string]float64{"seconds": 15, "resolution": 1},
			},
		},
	}
	tk.PrivateData.UpstreamTaskID = "428463094829280"
	return tk
}

// The vendor reports what it actually billed; settling on that is what makes an
// over-estimated pre-charge refundable.
func TestV2AdjustBillingOnComplete(t *testing.T) {
	a := &TaskAdaptor{}
	ok := &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}

	// Charged for 15s, vendor billed 5s at 768P => 5 * $0.08 = $0.40.
	got := a.AdjustBillingOnComplete(v2Task(t, "MiniMax-H3", 0.08, realQueryBody), ok)
	if want := int(0.40 * common.QuotaPerUnit); got != want {
		t.Errorf("refund case: got %d, want %d", got, want)
	}

	// The served resolution wins over the requested one: 5s at 2K =>
	// 5 * 0.08 * 1.625 = $0.65.
	body2K := `{"items":[{"id":"428463094829280","status":"succeeded","resolution":"2K",
		"usage":{"total_seconds":5,"output_seconds":5}}],"total":1}`
	got = a.AdjustBillingOnComplete(v2Task(t, "MiniMax-H3", 0.08, body2K), ok)
	if want := int(0.65 * common.QuotaPerUnit); got != want {
		t.Errorf("2K case: got %d, want %d", got, want)
	}

	// Reference footage is inside total_seconds, so it is billed too.
	bodyInput := `{"items":[{"id":"428463094829280","status":"succeeded","resolution":"768P",
		"usage":{"total_seconds":8,"input_seconds":3,"output_seconds":5}}],"total":1}`
	got = a.AdjustBillingOnComplete(v2Task(t, "MiniMax-H3", 0.08, bodyInput), ok)
	if want := int(0.64 * common.QuotaPerUnit); got != want {
		t.Errorf("input seconds case: got %d, want %d", got, want)
	}

	// v1 models must be left completely alone -- they never had per-second
	// settlement and their pre-charge is the whole price.
	if got := a.AdjustBillingOnComplete(v2Task(t, "MiniMax-Hailuo-2.3", 0.08, realQueryBody), ok); got != 0 {
		t.Errorf("v1 model must not be re-priced, got %d", got)
	}

	// No-ops that must not panic or charge.
	if got := a.AdjustBillingOnComplete(nil, ok); got != 0 {
		t.Errorf("nil task: %d", got)
	}
	if got := a.AdjustBillingOnComplete(v2Task(t, "MiniMax-H3", 0.08, `{"items":[]}`), ok); got != 0 {
		t.Errorf("task absent from list: %d", got)
	}
	if got := a.AdjustBillingOnComplete(v2Task(t, "MiniMax-H3", 0.08, realQueryBody),
		&relaycommon.TaskInfo{Status: model.TaskStatusFailure}); got != 0 {
		t.Errorf("failure goes through refund, not re-pricing: %d", got)
	}
	noCtx := v2Task(t, "MiniMax-H3", 0.08, realQueryBody)
	noCtx.PrivateData.BillingContext = nil
	if got := a.AdjustBillingOnComplete(noCtx, ok); got != 0 {
		t.Errorf("missing billing context: %d", got)
	}
}

// GET /v1/videos/{id} is served through a runtime type assertion; the v2 branch
// has to find our entry in the list the same way the poller does.
func TestConvertV2ToOpenAIVideo(t *testing.T) {
	task := v2Task(t, "MiniMax-H3", 0.08, realQueryBody)
	task.Status = model.TaskStatusSuccess
	task.Progress = "100%"

	out, err := convertV2ToOpenAIVideo(task)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	if v["status"] != "completed" {
		t.Errorf("status: got %v", v["status"])
	}
	if v["seconds"] != "5" {
		t.Errorf("seconds: got %v", v["seconds"])
	}
	md, _ := v["metadata"].(map[string]any)
	if md == nil || md["url"] != "https://video-product.cdn.minimax.io/inference_output/x/output.mp4" {
		t.Errorf("metadata.url missing: %v", v["metadata"])
	}

	// A task with no poll data yet must still render.
	empty := &model.Task{Status: model.TaskStatusQueued,
		Properties: model.Properties{OriginModelName: "MiniMax-H3"}}
	if _, err := convertV2ToOpenAIVideo(empty); err != nil {
		t.Fatalf("unpolled task must render: %v", err)
	}
}

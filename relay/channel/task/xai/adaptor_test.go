package xai

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

func TestParseTaskResultStatusMapping(t *testing.T) {
	a := &TaskAdaptor{}
	cases := []struct {
		name string
		body string
		// relaycommon.TaskInfo.Status is a plain string; model.TaskStatus* are
		// untyped constants.
		wantStatus string
		wantURL    string
		wantReason string
	}{
		{
			name:       "done",
			body:       `{"status":"done","video":{"url":"https://vidgen.x.ai/a.mp4","duration":8}}`,
			wantStatus: model.TaskStatusSuccess,
			wantURL:    "https://vidgen.x.ai/a.mp4",
		},
		{
			name:       "processing keeps polling",
			body:       `{"status":"processing"}`,
			wantStatus: model.TaskStatusInProgress,
		},
		{
			name:       "failed with error object",
			body:       `{"status":"failed","error":{"message":"moderation blocked"}}`,
			wantStatus: model.TaskStatusFailure,
			wantReason: "moderation blocked",
		},
		{
			name:       "expired is terminal",
			body:       `{"status":"expired"}`,
			wantStatus: model.TaskStatusFailure,
			wantReason: "task expired",
		},
		{
			// An unrecognised status must keep polling, never silently fail a
			// task the user already paid for.
			name:       "unknown status keeps polling",
			body:       `{"status":"some_new_state"}`,
			wantStatus: model.TaskStatusInProgress,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ti, err := a.ParseTaskResult([]byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if ti.Status != tc.wantStatus {
				t.Errorf("status: got %v, want %v", ti.Status, tc.wantStatus)
			}
			// Url drives the generic polling loop's result-URL choice; RemoteUrl
			// is only read by the Gemini proxy. Both must be set, otherwise the
			// caller gets a locally built proxy URL instead of the real video.
			if tc.wantURL != "" {
				if ti.Url != tc.wantURL {
					t.Errorf("Url: got %q, want %q", ti.Url, tc.wantURL)
				}
				if ti.RemoteUrl != tc.wantURL {
					t.Errorf("RemoteUrl: got %q, want %q", ti.RemoteUrl, tc.wantURL)
				}
			}
			if tc.wantReason != "" && ti.Reason != tc.wantReason {
				t.Errorf("reason: got %q, want %q", ti.Reason, tc.wantReason)
			}
		})
	}
}

// buildTask assembles a task as the submit path would have left it.
func buildTask(t *testing.T, modelPrice float64, ratios map[string]float64, pollBody string) *model.Task {
	t.Helper()
	return &model.Task{
		TaskID: "t1",
		Data:   json.RawMessage(pollBody),
		PrivateData: model.TaskPrivateData{
			BillingContext: &model.TaskBillingContext{
				ModelPrice:  modelPrice,
				GroupRatio:  1,
				OtherRatios: ratios,
			},
		},
	}
}

func TestAdjustBillingOnCompleteSettlesActualDuration(t *testing.T) {
	a := &TaskAdaptor{}
	success := &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}

	// Charged for 15s @720p ($0.05 * 1.4 = $0.07/s => $1.05), delivered 8s => $0.56.
	task := buildTask(t, 0.05,
		map[string]float64{"seconds": 15, "resolution": 1.4, "media": 1},
		`{"status":"done","video":{"url":"u","duration":8}}`)
	got := a.AdjustBillingOnComplete(task, success)
	want := int(0.56 * common.QuotaPerUnit)
	if got != want {
		t.Errorf("refund case: got %d, want %d", got, want)
	}

	// Input media must survive the re-price: 5s @480p + $0.03 input video.
	// media ratio = 1 + 0.03/(0.05*5*1) = 1.12 ; delivered 3s => 0.15 + 0.03 = $0.18
	task = buildTask(t, 0.05,
		map[string]float64{"seconds": 5, "resolution": 1, "media": 1.12},
		`{"status":"done","video":{"url":"u","duration":3}}`)
	got = a.AdjustBillingOnComplete(task, success)
	want = int(0.18 * common.QuotaPerUnit)
	if got != want {
		t.Errorf("input-media case: got %d, want %d", got, want)
	}
}

// xAI reports its own charge via usage.cost_in_usd_ticks. That is authoritative
// and must win over the local duration-based recompute. Shape and scale
// verified against a live 480p/1s generation: 500000000 ticks == $0.05.
func TestAdjustBillingOnCompletePrefersUpstreamCost(t *testing.T) {
	a := &TaskAdaptor{}
	success := &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}

	// Local math would say 15s * $0.05 * 1.4 = $1.05; upstream says $0.05.
	task := buildTask(t, 0.05,
		map[string]float64{"seconds": 15, "resolution": 1.4, "media": 1},
		`{"status":"done","video":{"duration":1},"usage":{"cost_in_usd_ticks":500000000}}`)
	got := a.AdjustBillingOnComplete(task, success)
	want := int(0.05 * common.QuotaPerUnit)
	if got != want {
		t.Errorf("upstream cost should win: got %d, want %d", got, want)
	}

	// An absurd cost must be rejected rather than charged, falling back to the
	// local recompute (5s estimated, 3s delivered => $0.15).
	task = buildTask(t, 0.05,
		map[string]float64{"seconds": 5, "resolution": 1, "media": 1},
		`{"status":"done","video":{"duration":3},"usage":{"cost_in_usd_ticks":999999999999999}}`)
	got = a.AdjustBillingOnComplete(task, success)
	want = int(0.15 * common.QuotaPerUnit)
	if got != want {
		t.Errorf("implausible upstream cost should fall back: got %d, want %d", got, want)
	}
}

func TestUpstreamCostConversion(t *testing.T) {
	if got := UpstreamCostUSD(500000000); math.Abs(got-0.05) > 1e-9 {
		t.Errorf("live-verified value: got %v, want 0.05", got)
	}
	if got := UpstreamCostUSD(0); got != 0 {
		t.Errorf("absent cost: got %v, want 0", got)
	}
	if got := UpstreamCostUSD(-5); got != 0 {
		t.Errorf("negative cost: got %v, want 0", got)
	}
	if got := UpstreamCostUSD(int64(MaxUpstreamCostUSD*CostTicksPerUSD) + 1); got != 0 {
		t.Errorf("over cap should be rejected: got %v, want 0", got)
	}
}

func TestAdjustBillingOnCompleteNoOpCases(t *testing.T) {
	a := &TaskAdaptor{}
	success := &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}
	ratios := map[string]float64{"seconds": 8, "resolution": 1, "media": 1}

	// Actual matches the estimate — nothing to settle.
	if got := a.AdjustBillingOnComplete(
		buildTask(t, 0.05, ratios, `{"status":"done","video":{"duration":8}}`), success); got != 0 {
		t.Errorf("matching duration should be a no-op, got %d", got)
	}
	// Upstream reported no video block.
	if got := a.AdjustBillingOnComplete(
		buildTask(t, 0.05, ratios, `{"status":"done"}`), success); got != 0 {
		t.Errorf("missing video should be a no-op, got %d", got)
	}
	// Upstream reported a zero/absent duration.
	if got := a.AdjustBillingOnComplete(
		buildTask(t, 0.05, ratios, `{"status":"done","video":{"duration":0}}`), success); got != 0 {
		t.Errorf("zero duration should be a no-op, got %d", got)
	}
	// Failed tasks go through the refund path, not re-pricing.
	if got := a.AdjustBillingOnComplete(
		buildTask(t, 0.05, ratios, `{"status":"failed"}`),
		&relaycommon.TaskInfo{Status: model.TaskStatusFailure}); got != 0 {
		t.Errorf("failure should be a no-op, got %d", got)
	}
	// No billing context (legacy task) — must not panic or charge.
	if got := a.AdjustBillingOnComplete(&model.Task{Data: json.RawMessage(`{}`)}, success); got != 0 {
		t.Errorf("missing billing context should be a no-op, got %d", got)
	}
	if got := a.AdjustBillingOnComplete(nil, success); got != 0 {
		t.Errorf("nil task should be a no-op, got %d", got)
	}
}

// A hostile upstream duration must not become an unbounded billing multiplier.
func TestAdjustBillingOnCompleteClampsUpstreamDuration(t *testing.T) {
	a := &TaskAdaptor{}
	task := buildTask(t, 0.05,
		map[string]float64{"seconds": 5, "resolution": 1, "media": 1},
		`{"status":"done","video":{"duration":999999999}}`)
	got := a.AdjustBillingOnComplete(task, &relaycommon.TaskInfo{Status: model.TaskStatusSuccess})
	max := int(0.05 * float64(relaycommonMaxDuration()) * common.QuotaPerUnit)
	if got != max {
		t.Errorf("clamped quota: got %d, want %d", got, max)
	}
}

func relaycommonMaxDuration() int { return relaycommon.MaxTaskDurationSeconds }

// GET /v1/videos/{id} (the OpenAI-shaped fetch route) is served via a runtime
// type assertion to channel.OpenAIVideoConverter; without this method it
// answers "not_implemented:48" with HTTP 501 while the generic
// GET /v1/video/generations/{id} route keeps working — so the gap only bites
// clients using the OpenAI video shape.
func TestConvertToOpenAIVideo(t *testing.T) {
	a := &TaskAdaptor{}

	task := &model.Task{
		TaskID:     "task_abc",
		Status:     model.TaskStatusSuccess,
		Progress:   "100%",
		CreatedAt:  1700000000,
		FinishTime: 1700000060,
		Properties: model.Properties{OriginModelName: "grok-imagine-video"},
		Data:       json.RawMessage(`{"status":"done","video":{"url":"https://vidgen.x.ai/a.mp4","duration":3}}`),
	}
	out, err := a.ConvertToOpenAIVideo(task)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	if v["status"] != "completed" {
		t.Errorf("status: got %v, want completed", v["status"])
	}
	if v["model"] != "grok-imagine-video" {
		t.Errorf("model: got %v", v["model"])
	}
	if v["seconds"] != "3" {
		t.Errorf("seconds: got %v, want \"3\"", v["seconds"])
	}
	md, _ := v["metadata"].(map[string]any)
	if md == nil || md["url"] != "https://vidgen.x.ai/a.mp4" {
		t.Errorf("metadata.url missing: %v", v["metadata"])
	}

	// Failure carries the upstream message through.
	task = &model.Task{
		TaskID: "task_bad", Status: model.TaskStatusFailure, Progress: "100%",
		Data: json.RawMessage(`{"status":"failed","error":{"message":"moderation blocked","code":"blocked"}}`),
	}
	out, _ = a.ConvertToOpenAIVideo(task)
	_ = json.Unmarshal(out, &v)
	if v["status"] != "failed" {
		t.Errorf("status: got %v, want failed", v["status"])
	}
	if e, _ := v["error"].(map[string]any); e == nil || e["message"] != "moderation blocked" {
		t.Errorf("error not propagated: %v", v["error"])
	}

	// A task with no poll data yet (still queued) must render, not error.
	task = &model.Task{TaskID: "task_q", Status: model.TaskStatusQueued, Progress: "0%"}
	out, err = a.ConvertToOpenAIVideo(task)
	if err != nil {
		t.Fatalf("queued task must render: %v", err)
	}
	_ = json.Unmarshal(out, &v)
	if v["status"] != "queued" {
		t.Errorf("status: got %v, want queued", v["status"])
	}

	// Failure with no upstream error object falls back to the recorded reason.
	task = &model.Task{TaskID: "task_e", Status: model.TaskStatusFailure,
		FailReason: "task expired", Data: json.RawMessage(`{"status":"expired"}`)}
	out, _ = a.ConvertToOpenAIVideo(task)
	_ = json.Unmarshal(out, &v)
	if e, _ := v["error"].(map[string]any); e == nil || e["message"] != "task expired" {
		t.Errorf("fail reason fallback missing: %v", v["error"])
	}

	if _, err := a.ConvertToOpenAIVideo(nil); err == nil {
		t.Error("nil task should error")
	}
}

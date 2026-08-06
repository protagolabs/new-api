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

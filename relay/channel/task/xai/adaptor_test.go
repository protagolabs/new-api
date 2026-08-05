package xai

import (
	"encoding/json"
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
			if tc.wantURL != "" && ti.RemoteUrl != tc.wantURL {
				t.Errorf("url: got %q, want %q", ti.RemoteUrl, tc.wantURL)
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

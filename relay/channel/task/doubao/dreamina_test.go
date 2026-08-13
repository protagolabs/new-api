package doubao

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

const dreaminaModel = "dreamina-seedance-2-0-260128"

// ModelRatio is quota per token, chosen so a 5s/720p video -- 108900 tokens,
// the exact value captured from production on 2026-08-13 -- still costs $2.10.
// 1050000 / 108900 = 9.641873...
const testModelRatio = 9.6419

func dreaminaTask(t *testing.T, resolution string, tokens int, bc *model.TaskBillingContext) (*model.Task, *relaycommon.TaskInfo) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":      dreaminaModel,
		"status":     "succeeded",
		"resolution": resolution,
		"usage":      map[string]int{"completion_tokens": tokens, "total_tokens": tokens},
	})
	if err != nil {
		t.Fatal(err)
	}
	task := &model.Task{Data: body}
	task.Properties.OriginModelName = dreaminaModel
	task.PrivateData.BillingContext = bc
	return task, &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}
}

func usd(quota int) float64 { return float64(quota) / 500000.0 }

// The whole reason for the switch: billing has to track the vendor's tokens, not
// a flat per-call price. A 5s/720p video is 108900 tokens and still lands at
// $2.10 so existing customers are untouched.
func TestDreaminaBillsByTokens(t *testing.T) {
	bc := &model.TaskBillingContext{ModelRatio: testModelRatio, GroupRatio: 1}
	task, res := dreaminaTask(t, Dreamina720P, 108900, bc)
	got := usd(DreaminaSettleQuota(task, res))
	if got < 2.095 || got > 2.105 {
		t.Errorf("5s/720p (108900 tokens) should be $2.10, got $%.4f", got)
	}
}

// 10s/720p is 216900 tokens -- NOT exactly 2x the 5s 108900 (it is 1.9917x).
// Billing per second would charge exactly 2x; billing per token tracks the
// vendor. This is the drift that made per-second pricing diverge from the
// invoice.
func TestDreaminaTracksVendorTokensNotSeconds(t *testing.T) {
	bc := &model.TaskBillingContext{ModelRatio: testModelRatio, GroupRatio: 1}
	task5, res5 := dreaminaTask(t, Dreamina720P, 108900, bc)
	task10, res10 := dreaminaTask(t, Dreamina720P, 216900, bc)
	got5, got10 := DreaminaSettleQuota(task5, res5), DreaminaSettleQuota(task10, res10)

	ratio := float64(got10) / float64(got5)
	want := 216900.0 / 108900.0 // 1.9917
	if ratio < want-0.001 || ratio > want+0.001 {
		t.Errorf("10s/5s quota ratio should be %.4f (token ratio), got %.4f", want, ratio)
	}
}

// A per-second price (ModelPrice, so ModelRatio is zero) must leave the
// pre-charge untouched -- this model was per-second before the switch, and other
// doubao models still are.
func TestDreaminaLeavesPerSecondPricingAlone(t *testing.T) {
	bc := &model.TaskBillingContext{ModelPrice: 0.42, GroupRatio: 1}
	task, res := dreaminaTask(t, Dreamina720P, 108900, bc)
	if got := DreaminaSettleQuota(task, res); got != 0 {
		t.Errorf("per-second pricing must not settle, got %d", got)
	}
}

// Asking for 4K and receiving 720p must bill 720p. Trusting the request would
// charge the 4k tier (3.89/0.76 = 5.1x) on a 720p token count.
func TestDreaminaBillsDeliveredResolutionNotRequested(t *testing.T) {
	// Submit-time ratio records the 4K request, but the response says 720p.
	requested4K := &model.TaskBillingContext{
		ModelRatio:  testModelRatio,
		GroupRatio:  1,
		OtherRatios: map[string]float64{"resolution": dreaminaResolutionRatios[Dreamina4K]},
	}
	task, res := dreaminaTask(t, Dreamina720P, 108900, requested4K)
	got := usd(DreaminaSettleQuota(task, res))
	if got < 2.095 || got > 2.105 {
		t.Errorf("delivered 720p must bill $2.10, got $%.4f", got)
	}
}

// A genuine 4K delivery picks up the 4k tier: 3.89/0.76 = 5.118x the 720p base.
func TestDreaminaHonoursDelivered4k(t *testing.T) {
	bc := &model.TaskBillingContext{ModelRatio: testModelRatio, GroupRatio: 1}
	task4k, res := dreaminaTask(t, Dreamina4K, 108900, bc)
	task720, res2 := dreaminaTask(t, Dreamina720P, 108900, bc)
	got4k, got720 := DreaminaSettleQuota(task4k, res), DreaminaSettleQuota(task720, res2)

	want := dreaminaResolutionRatios[Dreamina4K] // 3.89 / 0.76
	got := float64(got4k) / float64(got720)
	if got < want-0.001 || got > want+0.001 {
		t.Errorf("4k tier: got %.4f, want %.4f", got, want)
	}
}

// A discount reaches this path through GroupRatio.
func TestDreaminaAppliesGroupRatio(t *testing.T) {
	task, res := dreaminaTask(t, Dreamina720P, 108900,
		&model.TaskBillingContext{ModelRatio: testModelRatio, GroupRatio: 0.95})
	got := usd(DreaminaSettleQuota(task, res))
	if got < 1.990 || got > 2.000 {
		t.Errorf("0.95 discount: got $%.4f, want ~$1.995", got)
	}
}

// A video input is a fact about the request the vendor cannot revise, so it is
// carried from submit time while the resolution still comes from the response.
func TestDreaminaKeepsVideoInputTierFromSubmit(t *testing.T) {
	bc := &model.TaskBillingContext{
		ModelRatio:  testModelRatio,
		GroupRatio:  1,
		OtherRatios: map[string]float64{"resolution": dreaminaVideoInputRatios[Dreamina720P]},
	}
	task, res := dreaminaTask(t, Dreamina720P, 108900, bc)
	got := DreaminaSettleQuota(task, res)
	want := 108900.0 * testModelRatio * dreaminaVideoInputRatios[Dreamina720P]
	if float64(got) < want-2 || float64(got) > want+2 {
		t.Errorf("video input 720p: got %d, want ~%d", got, int(want))
	}
	// The two tables must never collide, or this recovery is ambiguous.
	for tier, noVideo := range dreaminaResolutionRatios {
		for _, withVideo := range dreaminaVideoInputRatios {
			if noVideo == withVideo {
				t.Errorf("tier %s: no-video ratio %v collides with a video-input ratio", tier, noVideo)
			}
		}
	}
}

// `size` must reach the vendor, and billing must read the same value it sends.
func TestDreaminaHonoursSizeField(t *testing.T) {
	for _, tc := range []struct {
		name    string
		req     relaycommon.TaskSubmitReq
		wantRes string
	}{
		{"size only", relaycommon.TaskSubmitReq{Size: "1080p"}, Dreamina1080P},
		{"metadata wins over size", relaycommon.TaskSubmitReq{
			Size:     "480p",
			Metadata: map[string]any{"resolution": "4k"},
		}, Dreamina4K},
		{"neither", relaycommon.TaskSubmitReq{}, ""},
		{"unrecognised", relaycommon.TaskSubmitReq{Size: "1920x1080"}, ""},
	} {
		if got := dreaminaRequestedResolution(&tc.req); got != tc.wantRes {
			t.Errorf("%s: resolved %q, want %q", tc.name, got, tc.wantRes)
		}
		out := &requestPayload{}
		applyDreaminaResolution(&tc.req, out)
		if out.Resolution != tc.wantRes {
			t.Errorf("%s: sent %q upstream, want %q", tc.name, out.Resolution, tc.wantRes)
		}
	}

	out := &requestPayload{Resolution: "720p"}
	applyDreaminaResolution(&relaycommon.TaskSubmitReq{Size: "4k"}, out)
	if out.Resolution != "720p" {
		t.Errorf("existing resolution overwritten with %q", out.Resolution)
	}
}

func TestDreaminaSettleNoOpCases(t *testing.T) {
	bc := &model.TaskBillingContext{ModelRatio: testModelRatio, GroupRatio: 1}

	if got := DreaminaSettleQuota(nil, &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}); got != 0 {
		t.Errorf("nil task: got %d", got)
	}
	task, _ := dreaminaTask(t, Dreamina720P, 108900, bc)
	if got := DreaminaSettleQuota(task, nil); got != 0 {
		t.Errorf("nil result: got %d", got)
	}
	if got := DreaminaSettleQuota(task, &relaycommon.TaskInfo{Status: model.TaskStatusInProgress}); got != 0 {
		t.Errorf("in-progress: got %d", got)
	}
	if got := DreaminaSettleQuota(task, &relaycommon.TaskInfo{Status: model.TaskStatusFailure}); got != 0 {
		t.Errorf("failed: got %d", got)
	}
	// Vendor reported no usage.
	noUsage, res := dreaminaTask(t, Dreamina720P, 0, bc)
	if got := DreaminaSettleQuota(noUsage, res); got != 0 {
		t.Errorf("zero tokens: got %d", got)
	}
	// No billing context.
	bare := &model.Task{Data: json.RawMessage(`{"status":"succeeded","usage":{"total_tokens":1000}}`)}
	bare.Properties.OriginModelName = dreaminaModel
	if got := DreaminaSettleQuota(bare, &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}); got != 0 {
		t.Errorf("no billing context: got %d", got)
	}
	// Another model on the same adaptor.
	other, res2 := dreaminaTask(t, Dreamina720P, 108900, bc)
	other.Properties.OriginModelName = "doubao-seedance-1-0-pro-250528"
	other.Properties.UpstreamModelName = "doubao-seedance-1-0-pro-250528"
	if got := DreaminaSettleQuota(other, res2); got != 0 {
		t.Errorf("non-dreamina model must not settle, got %d", got)
	}
}

func TestIsDreaminaSeedance2(t *testing.T) {
	for _, m := range []string{
		"dreamina-seedance-2-0-260128",
		"Dreamina-Seedance-2-0-260128",
		"dreamina-seedance-2-0-fast-260128",
	} {
		if !IsDreaminaSeedance2(m) {
			t.Errorf("%s should match", m)
		}
	}
	for _, m := range []string{
		"doubao-seedance-2-0-260128",
		"doubao-seedance-1-0-pro-250528",
		"dreamina-seedance-1-0",
		"",
	} {
		if IsDreaminaSeedance2(m) {
			t.Errorf("%s should not match", m)
		}
	}
}

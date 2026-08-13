package doubao

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

const dreaminaModel = "dreamina-seedance-2-0-260128"

// ModelPrice is configured as the 720p per-second rate. $0.42 keeps a 5s/720p
// video at the $2.10 it has always cost while letting everything else scale.
const testModelPrice = 0.42

func dreaminaTask(t *testing.T, resolution string, duration int, bc *model.TaskBillingContext) (*model.Task, *relaycommon.TaskInfo) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":      dreaminaModel,
		"status":     "succeeded",
		"resolution": resolution,
		"duration":   duration,
	})
	if err != nil {
		t.Fatal(err)
	}
	task := &model.Task{Data: body}
	task.Properties.OriginModelName = dreaminaModel
	task.PrivateData.BillingContext = bc
	return task, &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}
}

func usd(quota int) float64 { return float64(quota) / common.QuotaPerUnit }

// The vendor's published USD list for a 5s video, reproduced end to end. These
// are the numbers on the invoice we are marking up, so a drift here is a pricing
// incident.
func TestDreaminaSettlesAgainstPublishedList(t *testing.T) {
	bc := &model.TaskBillingContext{ModelPrice: testModelPrice, GroupRatio: 1}
	// Markup is uniform across tiers, so each tier lands at list x (2.10/0.76).
	const markup = 2.10 / 0.76
	for _, tc := range []struct {
		res  string
		list float64
	}{
		{Dreamina480P, 0.35},
		{Dreamina720P, 0.76},
		{Dreamina1080P, 1.87},
		{Dreamina4K, 3.89},
	} {
		task, res := dreaminaTask(t, tc.res, 5, bc)
		got := usd(DreaminaSettleQuota(task, res))
		want := tc.list * markup
		if got < want-0.005 || got > want+0.005 {
			t.Errorf("%s 5s: got $%.4f, want $%.4f", tc.res, got, want)
		}
	}
}

// The whole reason for the change: duration has to move the bill.
func TestDreaminaScalesWithDuration(t *testing.T) {
	bc := &model.TaskBillingContext{ModelPrice: testModelPrice, GroupRatio: 1}

	task5s, res := dreaminaTask(t, Dreamina720P, 5, bc)
	got5s := usd(DreaminaSettleQuota(task5s, res))
	task10s, res := dreaminaTask(t, Dreamina720P, 10, bc)
	got10s := usd(DreaminaSettleQuota(task10s, res))

	if got5s < 2.095 || got5s > 2.105 {
		t.Errorf("5s/720p should stay at $2.10, got $%.4f", got5s)
	}
	if got10s < 4.195 || got10s > 4.205 {
		t.Errorf("10s/720p should be $4.20, got $%.4f", got10s)
	}
}

// Asking for 4K and receiving 720p must bill 720p. Trusting the request would
// charge 5.1x -- and this vendor is especially untrustworthy here, having
// ignored `size` outright before applyDreaminaResolution.
func TestDreaminaBillsDeliveredResolutionNotRequested(t *testing.T) {
	requested4K := &model.TaskBillingContext{
		ModelPrice:  testModelPrice,
		GroupRatio:  1,
		OtherRatios: map[string]float64{"resolution": dreaminaResolutionRatios[Dreamina4K], "seconds": 5},
	}
	task, res := dreaminaTask(t, Dreamina720P, 5, requested4K) // delivered 720p
	got := usd(DreaminaSettleQuota(task, res))
	if got < 2.095 || got > 2.105 {
		t.Errorf("delivered 720p must bill $2.10, got $%.4f", got)
	}
}

// A discount reaches this path through GroupRatio.
func TestDreaminaAppliesGroupRatio(t *testing.T) {
	task, res := dreaminaTask(t, Dreamina720P, 5,
		&model.TaskBillingContext{ModelPrice: testModelPrice, GroupRatio: 0.95})
	got := usd(DreaminaSettleQuota(task, res))
	if got < 1.990 || got > 2.000 {
		t.Errorf("0.95 discount: got $%.4f, want ~$1.995", got)
	}
}

// A video input is a fact about the request the vendor cannot revise, so it is
// carried from submit time while the resolution still comes from the response.
func TestDreaminaKeepsVideoInputTierFromSubmit(t *testing.T) {
	bc := &model.TaskBillingContext{
		ModelPrice:  testModelPrice,
		GroupRatio:  1,
		OtherRatios: map[string]float64{"resolution": dreaminaVideoInputRatios[Dreamina720P], "seconds": 5},
	}
	task, res := dreaminaTask(t, Dreamina720P, 5, bc)
	got := usd(DreaminaSettleQuota(task, res))
	want := testModelPrice * 5 * dreaminaVideoInputRatios[Dreamina720P]
	if got < want-0.005 || got > want+0.005 {
		t.Errorf("video input 720p: got $%.4f, want $%.4f", got, want)
	}
	// And the two tables must never collide, or this recovery is ambiguous.
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

	// An explicit upstream value already set must not be overwritten.
	out := &requestPayload{Resolution: "720p"}
	applyDreaminaResolution(&relaycommon.TaskSubmitReq{Size: "4k"}, out)
	if out.Resolution != "720p" {
		t.Errorf("existing resolution overwritten with %q", out.Resolution)
	}
}

// No resolution given must bill the tier the vendor will actually render, or the
// pre-charge is wrong from the start.
func TestDreaminaDefaultsToVendorDefaultTier(t *testing.T) {
	if got := dreaminaTierRatio("", false); got != 1.0 {
		t.Errorf("empty resolution should bill the 720p base, got %v", got)
	}
	ratios := dreaminaRatios(0, "", false)
	if ratios["seconds"] != 5 {
		t.Errorf("missing duration should assume the vendor default 5s, got %v", ratios["seconds"])
	}
}

func TestDreaminaSettleNoOpCases(t *testing.T) {
	bc := &model.TaskBillingContext{ModelPrice: testModelPrice, GroupRatio: 1}

	if got := DreaminaSettleQuota(nil, &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}); got != 0 {
		t.Errorf("nil task: got %d", got)
	}
	task, _ := dreaminaTask(t, Dreamina720P, 5, bc)
	if got := DreaminaSettleQuota(task, nil); got != 0 {
		t.Errorf("nil result: got %d", got)
	}
	if got := DreaminaSettleQuota(task, &relaycommon.TaskInfo{Status: model.TaskStatusInProgress}); got != 0 {
		t.Errorf("in-progress: got %d", got)
	}
	// Failed tasks are refunded elsewhere; settling here would charge for nothing.
	if got := DreaminaSettleQuota(task, &relaycommon.TaskInfo{Status: model.TaskStatusFailure}); got != 0 {
		t.Errorf("failed: got %d", got)
	}
	// No duration reported.
	noDur, res := dreaminaTask(t, Dreamina720P, 0, bc)
	if got := DreaminaSettleQuota(noDur, res); got != 0 {
		t.Errorf("zero duration: got %d", got)
	}
	// Another model on the same adaptor must be left alone.
	other, res2 := dreaminaTask(t, Dreamina720P, 5, bc)
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

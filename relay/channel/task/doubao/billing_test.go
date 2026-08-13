package doubao

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// The real response captured from dreamina-seedance-2-0-260128 in production on
// 2026-08-13. Two data points confirmed tokens scale linearly with duration:
// 5s/720p = 108900 tokens, 10s/720p = 216900.
func successTask(t *testing.T, modelName, resolution string, tokens int, bc *model.TaskBillingContext) (*model.Task, *relaycommon.TaskInfo) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":      modelName,
		"status":     "succeeded",
		"resolution": resolution,
		"duration":   5,
		"usage":      map[string]int{"completion_tokens": tokens, "total_tokens": tokens},
	})
	if err != nil {
		t.Fatal(err)
	}
	task := &model.Task{Data: body}
	task.PrivateData.BillingContext = bc
	return task, &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}
}

// A ratio-priced model settles at tokens x ModelRatio x groupRatio. This is the
// whole point of the change: a 10s video costs twice a 5s one instead of the
// same flat per-call price.
func TestSettleQuotaScalesWithTokens(t *testing.T) {
	bc := &model.TaskBillingContext{ModelRatio: 9.6419, GroupRatio: 1}

	task5s, res := successTask(t, "dreamina-seedance-2-0-260128", "720p", 108900, bc)
	got5s := SettleQuota(task5s, res)

	task10s, res := successTask(t, "dreamina-seedance-2-0-260128", "720p", 216900, bc)
	got10s := SettleQuota(task10s, res)

	if got5s <= 0 || got10s <= 0 {
		t.Fatalf("expected positive quotas, got %d and %d", got5s, got10s)
	}
	// 216900/108900 = 1.9917
	ratio := float64(got10s) / float64(got5s)
	if ratio < 1.98 || ratio > 2.0 {
		t.Errorf("10s should cost ~1.99x the 5s video, got %.4f", ratio)
	}
}

// A per-call price (ModelPrice, so ModelRatio is zero) must keep its pre-charge.
// Returning anything else here would double-bill every model that has always
// been priced per call.
func TestSettleQuotaLeavesPerCallPricingAlone(t *testing.T) {
	bc := &model.TaskBillingContext{ModelPrice: 2.1, GroupRatio: 1}
	task, res := successTask(t, "dreamina-seedance-2-0-260128", "720p", 108900, bc)
	if got := SettleQuota(task, res); got != 0 {
		t.Errorf("per-call pricing must not settle, got %d", got)
	}
}

// The discount configured for a user reaches this path through GroupRatio, so a
// 0.95 rule has to show up as 5% off the settled amount.
func TestSettleQuotaAppliesGroupRatio(t *testing.T) {
	full, res := successTask(t, "dreamina-seedance-2-0-260128", "720p", 108900,
		&model.TaskBillingContext{ModelRatio: 9.6419, GroupRatio: 1})
	discounted, res2 := successTask(t, "dreamina-seedance-2-0-260128", "720p", 108900,
		&model.TaskBillingContext{ModelRatio: 9.6419, GroupRatio: 0.95})

	gotFull, gotDisc := SettleQuota(full, res), SettleQuota(discounted, res2)
	want := int(float64(gotFull) * 0.95)
	if gotDisc < want-2 || gotDisc > want+2 {
		t.Errorf("group ratio 0.95: got %d, want ~%d", gotDisc, want)
	}
}

// The resolution tier comes from the response, never the request. Asking for 4k
// and receiving 720p must bill the 720p tier -- taking the 4k tier (a discount
// against the base) while receiving 720p token counts undercharges by ~43%.
func TestSettleQuotaUsesDeliveredResolution(t *testing.T) {
	// doubao-seedance-2-0-260128 is the model with a populated price table:
	// base(720p) 46.0, 4k 26.0 CNY per million tokens.
	requested4k := &model.TaskBillingContext{
		ModelRatio:  9.6419,
		GroupRatio:  1,
		OtherRatios: map[string]float64{RatioKeyResolution: 26.0 / 46.0},
	}
	// Vendor delivered 720p despite the 4k request.
	task, res := successTask(t, "doubao-seedance-2-0-260128", "720p", 108900, requested4k)
	delivered := SettleQuota(task, res)

	baseline, res2 := successTask(t, "doubao-seedance-2-0-260128", "720p", 108900,
		&model.TaskBillingContext{ModelRatio: 9.6419, GroupRatio: 1})
	want := SettleQuota(baseline, res2)

	if delivered != want {
		t.Errorf("delivered 720p must bill the 720p tier: got %d, want %d", delivered, want)
	}
}

// Conversely, a genuine 4k delivery picks up the 4k tier.
func TestSettleQuotaHonoursDelivered4k(t *testing.T) {
	bc := &model.TaskBillingContext{ModelRatio: 9.6419, GroupRatio: 1}
	task4k, res := successTask(t, "doubao-seedance-2-0-260128", "4k", 108900, bc)
	got4k := SettleQuota(task4k, res)

	task720, res2 := successTask(t, "doubao-seedance-2-0-260128", "720p", 108900, bc)
	got720 := SettleQuota(task720, res2)

	wantRatio := 26.0 / 46.0
	gotRatio := float64(got4k) / float64(got720)
	if gotRatio < wantRatio-0.001 || gotRatio > wantRatio+0.001 {
		t.Errorf("4k tier: got ratio %.4f, want %.4f", gotRatio, wantRatio)
	}
}

// A model with no price table entry must still bill by tokens, just without a
// tier adjustment -- failing closed to 1.0 rather than dropping the charge.
func TestSettleQuotaUnknownModelStillBillsTokens(t *testing.T) {
	bc := &model.TaskBillingContext{ModelRatio: 2, GroupRatio: 1}
	task, res := successTask(t, "some-unlisted-video-model", "1080p", 100000, bc)
	if got := SettleQuota(task, res); got != 200000 {
		t.Errorf("got %d, want 200000 (tokens x ratio, tier 1.0)", got)
	}
}

func TestSettleQuotaNoOpCases(t *testing.T) {
	bc := &model.TaskBillingContext{ModelRatio: 9.6419, GroupRatio: 1}

	if got := SettleQuota(nil, &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}); got != 0 {
		t.Errorf("nil task: got %d", got)
	}
	task, _ := successTask(t, "dreamina-seedance-2-0-260128", "720p", 108900, bc)
	if got := SettleQuota(task, nil); got != 0 {
		t.Errorf("nil result: got %d", got)
	}
	// Not yet finished.
	if got := SettleQuota(task, &relaycommon.TaskInfo{Status: model.TaskStatusInProgress}); got != 0 {
		t.Errorf("in-progress: got %d", got)
	}
	// Vendor reported no usage.
	noUsage, res := successTask(t, "dreamina-seedance-2-0-260128", "720p", 0, bc)
	if got := SettleQuota(noUsage, res); got != 0 {
		t.Errorf("zero tokens: got %d", got)
	}
	// No billing context at all.
	bare := &model.Task{Data: json.RawMessage(`{"status":"succeeded","usage":{"total_tokens":1000}}`)}
	if got := SettleQuota(bare, &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}); got != 0 {
		t.Errorf("no billing context: got %d", got)
	}
}

// Splitting the submit-time ratio into two dimensions must not change the
// pre-charge: their product has to equal the original combined ratio.
func TestSplitRatiosMultiplyToCombined(t *testing.T) {
	const m = "doubao-seedance-2-0-260128"
	for _, tc := range []struct {
		res      string
		hasVideo bool
	}{
		{"720p", false}, {"720p", true},
		{"1080p", false}, {"1080p", true},
		{"4k", false}, {"4k", true},
		{"", false},
	} {
		combined, ok := GetVideoInputRatio(m, tc.res, tc.hasVideo)
		if !ok {
			t.Fatalf("res=%s hasVideo=%v: no price table", tc.res, tc.hasVideo)
		}
		vi, ok1 := GetVideoInputOnlyRatio(m, tc.hasVideo)
		rr, ok2 := GetResolutionRatio(m, tc.res, tc.hasVideo)
		if !ok1 || !ok2 {
			t.Fatalf("res=%s hasVideo=%v: split lookup failed", tc.res, tc.hasVideo)
		}
		if got := vi * rr; got < combined-1e-9 || got > combined+1e-9 {
			t.Errorf("res=%s hasVideo=%v: split product %.6f != combined %.6f",
				tc.res, tc.hasVideo, got, combined)
		}
	}
}

package doubao

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

const dreaminaModel = "dreamina-seedance-2-0-260128"

// ModelRatio is the vendor's list rate as quota per token: the $7.0/M base tier
// at the platform's 1-ratio-equals-$2/M convention. We resell at list, so a
// 5s/720p video (108900 tokens, captured from production 2026-08-13) must come
// out at the published $0.76.
const testModelRatio = 3.5

// Seedance 2.5's base list rate is $10.70/M tokens.
const testModelRatio25 = 5.35

const dreaminaModel25 = "dreamina-seedance-2-5-260628"

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
	if got < 0.760 || got > 0.765 {
		t.Errorf("5s/720p (108900 tokens) should be the list $0.7623, got $%.4f", got)
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

// Asking for 4K and receiving 720p must bill the 720p unit price. Trusting the
// request would charge the 4k unit-price tier ($4.0/M vs $7.0/M) on a 720p
// token count -- undercharging, since 4K's unit price is lower but its token
// count higher.
func TestDreaminaBillsDeliveredResolutionNotRequested(t *testing.T) {
	// Submit-time ratio records the 4K request, but the response says 720p.
	requested4K := &model.TaskBillingContext{
		ModelRatio:  testModelRatio,
		GroupRatio:  1,
		OtherRatios: map[string]float64{"resolution": dreaminaPricingByFamily["dreamina-seedance-2-0"].resolution[Dreamina4K]},
	}
	task, res := dreaminaTask(t, Dreamina720P, 108900, requested4K)
	got := usd(DreaminaSettleQuota(task, res))
	if got < 0.760 || got > 0.765 {
		t.Errorf("delivered 720p must bill the list $0.7623, got $%.4f", got)
	}
}

// A genuine 4K delivery picks up the 4k unit-price tier: $4.0/M vs $7.0/M =
// 0.571x. The unit price is lower but the token count far higher, so absolute
// 4K pricing still exceeds 720p.
func TestDreaminaHonoursDelivered4k(t *testing.T) {
	bc := &model.TaskBillingContext{ModelRatio: testModelRatio, GroupRatio: 1}
	task4k, res := dreaminaTask(t, Dreamina4K, 108900, bc)
	task720, res2 := dreaminaTask(t, Dreamina720P, 108900, bc)
	got4k, got720 := DreaminaSettleQuota(task4k, res), DreaminaSettleQuota(task720, res2)

	want := dreaminaPricingByFamily["dreamina-seedance-2-0"].resolution[Dreamina4K] // 3.89 / 0.76
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
	want := 0.7623 * 0.95
	if got < want-0.005 || got > want+0.005 {
		t.Errorf("0.95 discount: got $%.4f, want ~$%.4f", got, want)
	}
}

// A video input is a fact about the request the vendor cannot revise, so it is
// carried from submit time while the resolution still comes from the response.
func TestDreaminaKeepsVideoInputTierFromSubmit(t *testing.T) {
	bc := &model.TaskBillingContext{
		ModelRatio:  testModelRatio,
		GroupRatio:  1,
		OtherRatios: map[string]float64{"resolution": dreaminaPricingByFamily["dreamina-seedance-2-0"].videoInput[Dreamina720P]},
	}
	task, res := dreaminaTask(t, Dreamina720P, 108900, bc)
	got := DreaminaSettleQuota(task, res)
	want := 108900.0 * testModelRatio * dreaminaPricingByFamily["dreamina-seedance-2-0"].videoInput[Dreamina720P]
	if float64(got) < want-2 || float64(got) > want+2 {
		t.Errorf("video input 720p: got %d, want ~%d", got, int(want))
	}
	// The two tables must never collide, or this recovery is ambiguous.
	p20 := dreaminaPricingByFamily["dreamina-seedance-2-0"]
	for tier, noVideo := range p20.resolution {
		for _, withVideo := range p20.videoInput {
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
		// Pixel dimensions used to be dropped, which let the vendor fall back
		// to 720p and bill for it; they now map onto a tier. See
		// size_parsing_test.go for the full matrix.
		{"pixels", relaycommon.TaskSubmitReq{Size: "1920x1080"}, Dreamina1080P},
		{"unrecognised", relaycommon.TaskSubmitReq{Size: "not-a-size"}, ""},
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

// The ratio tables must reproduce the vendor's published unit prices per token
// exactly (USD/M tokens, from docs.byteplus.com). A drift here is a pricing
// incident: get it wrong and 480p undercharges or 4K overcharges by multiples.
func TestDreaminaRatioTableMatchesVendorUnitPrices(t *testing.T) {
	cases := []struct {
		res      string
		hasVideo bool
		want     float64
	}{
		{Dreamina480P, false, 7.0 / 7.0},
		{Dreamina720P, false, 7.0 / 7.0},
		{Dreamina1080P, false, 7.7 / 7.0},
		{Dreamina4K, false, 4.0 / 7.0},
		{Dreamina480P, true, 4.3 / 7.0},
		{Dreamina720P, true, 4.3 / 7.0},
		{Dreamina1080P, true, 4.7 / 7.0},
		{Dreamina4K, true, 2.4 / 7.0},
	}
	for _, tc := range cases {
		got := dreaminaTierRatio(dreaminaModel, tc.res, tc.hasVideo)
		if got < tc.want-1e-9 || got > tc.want+1e-9 {
			t.Errorf("res=%s hasVideo=%v: got ratio %v, want %v (vendor unit price)", tc.res, tc.hasVideo, got, tc.want)
		}
	}
}

// dreamina25Task builds a finished 2.5 task. Kept separate because the model
// name is what selects the pricing family.
func dreamina25Task(t *testing.T, resolution string, tokens int, bc *model.TaskBillingContext) (*model.Task, *relaycommon.TaskInfo) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"model":      dreaminaModel25,
		"status":     "succeeded",
		"resolution": resolution,
		"usage":      map[string]int{"completion_tokens": tokens, "total_tokens": tokens},
	})
	if err != nil {
		t.Fatal(err)
	}
	task := &model.Task{Data: body}
	task.Properties.OriginModelName = dreaminaModel25
	task.PrivateData.BillingContext = bc
	return task, &relaycommon.TaskInfo{Status: model.TaskStatusSuccess}
}

// 2.5's published price examples, reproduced end to end. Token counts are the
// vendor's own examples divided by its $10.70/M base rate.
func TestDreamina25MatchesPublishedPrices(t *testing.T) {
	bc := &model.TaskBillingContext{ModelRatio: testModelRatio25, GroupRatio: 1}
	for _, tc := range []struct {
		res  string
		list float64
	}{
		{Dreamina480P, 0.514},
		{Dreamina720P, 1.156},
	} {
		tokens := int(tc.list / 10.70 * 1e6)
		task, res := dreamina25Task(t, tc.res, tokens, bc)
		got := usd(DreaminaSettleQuota(task, res))
		if got < tc.list-0.005 || got > tc.list+0.005 {
			t.Errorf("2.5 %s 5s: got $%.4f, want the published $%.3f", tc.res, got, tc.list)
		}
	}
}

// 2.5 charges the same rate for 480p and 720p -- unlike 2.0, its only tier axis
// is video input. Billing 2.5 with 2.0's tiers would be a silent mispricing.
func TestDreamina25HasNoResolutionTier(t *testing.T) {
	if got := dreaminaTierRatio(dreaminaModel25, Dreamina480P, false); got != 1.0 {
		t.Errorf("2.5 480p should be the base rate, got %v", got)
	}
	if got := dreaminaTierRatio(dreaminaModel25, Dreamina720P, false); got != 1.0 {
		t.Errorf("2.5 720p should be the base rate, got %v", got)
	}
	// $6.40 / $10.70 -- and distinct from 2.0's $4.3/$7.0, which is the whole
	// point of splitting the tables by family.
	want := 6.40 / 10.70
	if got := dreaminaTierRatio(dreaminaModel25, Dreamina720P, true); got < want-1e-9 || got > want+1e-9 {
		t.Errorf("2.5 video input: got %v, want %v", got, want)
	}
	if same := dreaminaTierRatio(dreaminaModel, Dreamina720P, true); same == want {
		t.Error("2.0 and 2.5 video-input ratios must differ, or the families are interchangeable")
	}
}

// 2.5 does not offer 1080p/4K. A caller who asks anyway must not get another
// family's tier -- the family's base rate is the safe answer, and upstream
// rejects the request regardless.
// 2.5 has no 4K tier, so 4K must fall back to the base rate rather than borrow
// 2.0's much cheaper 4K figure. 1080p used to belong here too, but the model
// gained 1080p support and it is now a priced tier of its own -- see
// TestDreamina25HasA1080pTier.
func TestDreamina25UnsupportedTiersFallBackToBase(t *testing.T) {
	if got := dreaminaTierRatio(dreaminaModel25, Dreamina4K, false); got != 1.0 {
		t.Errorf("2.5 4K should fall back to the base rate, got %v", got)
	}
}

// Each family must resolve to its own table, matched by longest prefix.
func TestDreaminaFamilyRouting(t *testing.T) {
	for _, tc := range []struct {
		model    string
		wantBase float64
	}{
		{"dreamina-seedance-2-0-260128", 7.0},
		{"dreamina-seedance-2-0-fast-260128", 7.0},
		{"dreamina-seedance-2-5-260628", 10.70},
		{"Dreamina-Seedance-2-5-260628", 10.70},
	} {
		p, ok := dreaminaFamilyFor(tc.model)
		if !ok {
			t.Errorf("%s: no family matched", tc.model)
			continue
		}
		if p.base != tc.wantBase {
			t.Errorf("%s: matched base $%.2f/M, want $%.2f/M", tc.model, p.base, tc.wantBase)
		}
	}
	// An unknown release must NOT inherit a neighbouring family's rates.
	if _, ok := dreaminaFamilyFor("dreamina-seedance-3-0-270101"); ok {
		t.Error("an unconfigured family must not match, or its price is silently wrong")
	}
}

// Within every family the two tier tables must share no value, otherwise
// recovering hasVideo from the submit-time ratio is ambiguous.
func TestDreaminaTierTablesDoNotCollide(t *testing.T) {
	for family, p := range dreaminaPricingByFamily {
		for tier, noVideo := range p.resolution {
			for _, withVideo := range p.videoInput {
				if noVideo == withVideo {
					t.Errorf("%s tier %s: no-video ratio %v collides with a video-input ratio",
						family, tier, noVideo)
				}
			}
		}
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

// Both spellings of the duration must reach the vendor. Dropping `duration`
// silently let the vendor apply its own default: a 5s request came back as 10s
// and cost twice as much (observed in production 2026-08-14, 96475 tokens for
// what should have been ~48000). Every other task adaptor reads both fields.
func TestDreaminaAcceptsBothDurationSpellings(t *testing.T) {
	a := &TaskAdaptor{}
	for _, tc := range []struct {
		name string
		req  relaycommon.TaskSubmitReq
		want int
	}{
		{"seconds only", relaycommon.TaskSubmitReq{Model: dreaminaModel25, Seconds: "5"}, 5},
		{"duration only", relaycommon.TaskSubmitReq{Model: dreaminaModel25, Duration: 5}, 5},
		{"seconds wins", relaycommon.TaskSubmitReq{Model: dreaminaModel25, Seconds: "7", Duration: 5}, 7},
		{"neither", relaycommon.TaskSubmitReq{Model: dreaminaModel25}, 0},
	} {
		out, err := a.convertToRequestPayload(&tc.req)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got := 0
		if out.Duration != nil {
			got = int(*out.Duration)
		}
		if got != tc.want {
			t.Errorf("%s: sent duration=%d upstream, want %d", tc.name, got, tc.want)
		}
	}
}

// Seedance 2.5 requires duration = -1 for edit and extend tasks, where the
// output length comes from the source video. The sentinel must reach the vendor
// untouched -- and must NOT be accepted for models whose upstream has no notion
// of it, where silently substituting a default would hide the mismatch.
func TestDreaminaAutoDurationSentinel(t *testing.T) {
	a := &TaskAdaptor{}
	for _, tc := range []struct {
		name  string
		model string
		want  int // 0 means "no duration sent upstream"
	}{
		{"2.5 accepts -1", dreaminaModel25, relaycommon.AutoTaskDuration},
		{"2.0 does not", dreaminaModel, 0},
		{"other doubao does not", "doubao-seedance-1-0-pro-250528", 0},
	} {
		req := relaycommon.TaskSubmitReq{Model: tc.model, Duration: relaycommon.AutoTaskDuration}
		out, err := a.convertToRequestPayload(&req)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got := 0
		if out.Duration != nil {
			got = int(*out.Duration)
		}
		if got != tc.want {
			t.Errorf("%s: sent duration=%d, want %d", tc.name, got, tc.want)
		}
	}
}

// omni_reference_task_type is 2.5's way of stating the task type up front. It
// has to survive the metadata round-trip; before this field existed it was
// dropped silently and the vendor fell back to inferring the type.
func TestDreaminaOmniReferenceTaskTypePassesThrough(t *testing.T) {
	a := &TaskAdaptor{}
	req := relaycommon.TaskSubmitReq{
		Model:    dreaminaModel25,
		Prompt:   "extend this clip",
		Metadata: map[string]any{"omni_reference_task_type": "extend"},
	}
	out, err := a.convertToRequestPayload(&req)
	if err != nil {
		t.Fatal(err)
	}
	if out.OmniReferenceTaskType != "extend" {
		t.Errorf("sent %q upstream, want %q", out.OmniReferenceTaskType, "extend")
	}

	// Absent from the request means absent from the payload -- 2.0 rejects it.
	plain := relaycommon.TaskSubmitReq{Model: dreaminaModel, Prompt: "a cat"}
	out2, err := a.convertToRequestPayload(&plain)
	if err != nil {
		t.Fatal(err)
	}
	if out2.OmniReferenceTaskType != "" {
		t.Errorf("unset field leaked as %q", out2.OmniReferenceTaskType)
	}
}

// A discount scoped to one resolution must apply to that resolution and leave
// the others at full price. The vendor discounted only 1080p, so passing it on
// wholesale would give away the tiers they never discounted.
func TestSettleAppliesTierScopedDiscount(t *testing.T) {
	ratio_setting.SetGroupModelRatioForTest(map[string]map[string]float64{
		"user:530": {"dreamina-seedance-2-5*@1080p": 0.85},
	})
	defer ratio_setting.SetGroupModelRatioForTest(nil)

	const tokens = 100000

	// 1080p: base x 1080p tier (11.70/10.70) x 0.85 discount.
	got1080 := DreaminaSettleQuota(userTask25(t, 530, "default", Dreamina1080P, tokens))
	want1080 := float64(tokens) * 5.35 * (11.70 / 10.70) * 0.85
	if diff := float64(got1080) - want1080; diff > 1 || diff < -1 {
		t.Errorf("1080p = %d, want ~%.0f (tier discount applied)", got1080, want1080)
	}

	// 720p: no discount at all.
	got720 := DreaminaSettleQuota(userTask25(t, 530, "default", Dreamina720P, tokens))
	want720 := float64(tokens) * 5.35 * 1.0
	if diff := float64(got720) - want720; diff > 1 || diff < -1 {
		t.Errorf("720p = %d, want ~%.0f (must stay undiscounted)", got720, want720)
	}
}

// The tier is taken from what the vendor delivered, not what was requested --
// the vendor ignores the requested resolution often enough that pricing against
// the request would hand out a 1080p discount on a 720p video.
func TestSettleTierDiscountFollowsDeliveredResolution(t *testing.T) {
	ratio_setting.SetGroupModelRatioForTest(map[string]map[string]float64{
		"user:530": {"dreamina-seedance-2-5*@1080p": 0.85},
	})
	defer ratio_setting.SetGroupModelRatioForTest(nil)

	const tokens = 100000
	// Response says 720p, so no discount regardless of what was asked for.
	got := DreaminaSettleQuota(userTask25(t, 530, "default", Dreamina720P, tokens))
	want := float64(tokens) * 5.35
	if diff := float64(got) - want; diff > 1 || diff < -1 {
		t.Errorf("delivered 720p = %d, want ~%.0f (undiscounted)", got, want)
	}
}

// Another user must pay full price.
func TestSettleTierDiscountIsUserScoped(t *testing.T) {
	ratio_setting.SetGroupModelRatioForTest(map[string]map[string]float64{
		"user:530": {"dreamina-seedance-2-5*@1080p": 0.85},
	})
	defer ratio_setting.SetGroupModelRatioForTest(nil)

	const tokens = 100000
	got := DreaminaSettleQuota(userTask25(t, 999, "default", Dreamina1080P, tokens))
	want := float64(tokens) * 5.35 * (11.70 / 10.70)
	if diff := float64(got) - want; diff > 1 || diff < -1 {
		t.Errorf("other user = %d, want ~%.0f (full price)", got, want)
	}
}

// userTask25 is dreamina25Task with an owner, so tier-scoped discounts (which
// are looked up per user) can be exercised.
func userTask25(t *testing.T, userID int, group, resolution string, tokens int) (*model.Task, *relaycommon.TaskInfo) {
	t.Helper()
	task, info := dreamina25Task(t, resolution, tokens,
		&model.TaskBillingContext{ModelRatio: testModelRatio25, GroupRatio: 1})
	task.UserId = userID
	task.Group = group
	return task, info
}

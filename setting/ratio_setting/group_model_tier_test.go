package ratio_setting

import "testing"

func seedRules(t *testing.T, data map[string]map[string]float64) {
	t.Helper()
	groupModelRatioMap.Clear()
	for k, v := range data {
		groupModelRatioMap.Set(k, v)
	}
	t.Cleanup(groupModelRatioMap.Clear)
}

const seedance25 = "dreamina-seedance-2-5-260628"

// A vendor promotion on one resolution should reach only that resolution.
func TestTierRuleAppliesOnlyToItsTier(t *testing.T) {
	seedRules(t, map[string]map[string]float64{
		"user:530": {seedance25 + "@1080p": 0.85},
	})

	if r, ok := GetModelTierRatioForUser(530, "default", seedance25, "1080p"); !ok || r != 0.85 {
		t.Errorf("1080p = %v/%v, want 0.85/true", r, ok)
	}
	for _, tier := range []string{"480p", "720p", "4k"} {
		if r, ok := GetModelTierRatioForUser(530, "default", seedance25, tier); ok {
			t.Errorf("%s picked up the 1080p discount (%v); other tiers must be undiscounted", tier, r)
		}
	}
}

// The rule must not leak to other users.
func TestTierRuleIsScopedToTheUser(t *testing.T) {
	seedRules(t, map[string]map[string]float64{
		"user:530": {seedance25 + "@1080p": 0.85},
	})
	if r, ok := GetModelTierRatioForUser(999, "default", seedance25, "1080p"); ok {
		t.Errorf("another user got the discount: %v", r)
	}
}

// Prefix form, so a new point release of the same family keeps the discount
// without a config edit.
func TestTierRulePrefixPattern(t *testing.T) {
	seedRules(t, map[string]map[string]float64{
		"user:530": {"dreamina-seedance-2-5*@1080p": 0.85},
	})
	for _, model := range []string{seedance25, "dreamina-seedance-2-5-270101"} {
		if r, ok := GetModelTierRatioForUser(530, "default", model, "1080p"); !ok || r != 0.85 {
			t.Errorf("%s 1080p = %v/%v, want 0.85/true", model, r, ok)
		}
	}
	// A different family must not match.
	if _, ok := GetModelTierRatioForUser(530, "default", "dreamina-seedance-2-0-260128", "1080p"); ok {
		t.Error("2.0 matched a 2.5-scoped rule")
	}
}

// This is the distinction the whole design rests on: a model-level pattern is
// also a textual prefix of "model@tier", so it must not be allowed to answer
// tier queries — otherwise every model-wide discount would silently become a
// per-tier one and the two rule kinds would be indistinguishable.
func TestModelRuleDoesNotAnswerTierQueries(t *testing.T) {
	seedRules(t, map[string]map[string]float64{
		"user:530": {"dreamina-seedance-2-5*": 0.90},
	})
	// It still applies, but as the model-level fallback (0.90 for every tier),
	// not as a tier-specific rule.
	for _, tier := range []string{"480p", "720p", "1080p"} {
		r, ok := GetModelTierRatioForUser(530, "default", seedance25, tier)
		if !ok || r != 0.90 {
			t.Errorf("%s = %v/%v, want the model-level 0.90 for every tier", tier, r, ok)
		}
	}
}

// When both exist, the tier rule wins outright — it is the more specific one.
func TestTierRuleBeatsModelRule(t *testing.T) {
	seedRules(t, map[string]map[string]float64{
		"user:530": {
			"dreamina-seedance-2-5*":       0.90,
			"dreamina-seedance-2-5*@1080p": 0.85,
		},
	})
	if r, _ := GetModelTierRatioForUser(530, "default", seedance25, "1080p"); r != 0.85 {
		t.Errorf("1080p = %v, want 0.85 (tier rule replaces, not multiplies, the model rule)", r)
	}
	if r, _ := GetModelTierRatioForUser(530, "default", seedance25, "720p"); r != 0.90 {
		t.Errorf("720p = %v, want the model-level 0.90", r)
	}
}

// A user-scoped rule outranks a group-scoped one, matching GetModelRatioForUser.
func TestTierRuleUserScopeBeatsGroup(t *testing.T) {
	seedRules(t, map[string]map[string]float64{
		"user:530": {seedance25 + "@1080p": 0.85},
		"default":  {seedance25 + "@1080p": 0.95},
	})
	if r, _ := GetModelTierRatioForUser(530, "default", seedance25, "1080p"); r != 0.85 {
		t.Errorf("got %v, want the user-scoped 0.85", r)
	}
	// A different user still gets the group rule.
	if r, _ := GetModelTierRatioForUser(999, "default", seedance25, "1080p"); r != 0.95 {
		t.Errorf("got %v, want the group-scoped 0.95", r)
	}
}

// No rules at all must report "no opinion" so the caller keeps its group ratio;
// returning 0 would zero out the charge.
func TestNoRuleReportsNotFound(t *testing.T) {
	seedRules(t, nil)
	if r, ok := GetModelTierRatioForUser(530, "default", seedance25, "1080p"); ok {
		t.Errorf("got %v/true, want not-found", r)
	}
}

// An empty tier degrades to the plain model lookup rather than matching oddly.
func TestEmptyTierFallsBackToModelLookup(t *testing.T) {
	seedRules(t, map[string]map[string]float64{
		"user:530": {seedance25: 0.9, seedance25 + "@1080p": 0.85},
	})
	if r, ok := GetModelTierRatioForUser(530, "default", seedance25, ""); !ok || r != 0.9 {
		t.Errorf("empty tier = %v/%v, want the model rule 0.9", r, ok)
	}
}

// TierScopedRules is the diagnostic for rules written against models whose
// settlement never consults a tier — they would silently never fire.
func TestTierScopedRulesListsThem(t *testing.T) {
	seedRules(t, map[string]map[string]float64{
		"user:530": {seedance25 + "@1080p": 0.85, "MiniMax-H3": 0.95},
	})
	got := TierScopedRules()
	if len(got["user:530"]) != 1 || got["user:530"][0] != seedance25+"@1080p" {
		t.Errorf("TierScopedRules = %v, want just the @1080p rule", got)
	}
}

package ratio_setting

import "testing"

// setRules installs rules for one test and restores the previous contents
// afterwards. RWMap has no per-key delete, so the whole map is snapshotted.
func setRules(t *testing.T, rules map[string]map[string]float64) {
	t.Helper()
	prev := groupModelRatioMap.ReadAll()
	groupModelRatioMap.AddAll(rules)
	t.Cleanup(func() {
		groupModelRatioMap.Clear()
		groupModelRatioMap.AddAll(prev)
	})
}

// The real configuration this was built for: one customer, four discount tiers
// across seven models, reachable with a single API key and the original model
// names.
func TestGetGroupModelRatioRealConfig(t *testing.T) {
	setRules(t, map[string]map[string]float64{
		"vip-0806": {
			"MiniMax-H3":                   0.95,
			"happyhorse-1.1-*":             0.55,
			"grok-imagine-video*":          0.65,
			"dreamina-seedance-2-0-260128": 0.95,
		},
	})

	cases := map[string]float64{
		"MiniMax-H3":                   0.95,
		"happyhorse-1.1-t2v":           0.55,
		"happyhorse-1.1-i2v":           0.55,
		"happyhorse-1.1-r2v":           0.55,
		"grok-imagine-video":           0.65,
		"grok-imagine-video-1.5":       0.65,
		"dreamina-seedance-2-0-260128": 0.95,
	}
	for m, want := range cases {
		got, ok := GetGroupModelRatio("vip-0806", m)
		if !ok {
			t.Errorf("%s: expected a rule to match", m)
			continue
		}
		if got != want {
			t.Errorf("%s: got %v, want %v", m, got, want)
		}
	}

	// Models outside the rules must fall through, NOT be treated as free or as
	// some default discount.
	for _, m := range []string{"gpt-4.1", "claude-sonnet-4-6", "happyhorse-1.0-t2v", "grok-4.5"} {
		if _, ok := GetGroupModelRatio("vip-0806", m); ok {
			t.Errorf("%s: must not match any rule", m)
		}
	}

	// Other groups are unaffected.
	if _, ok := GetGroupModelRatio("default", "MiniMax-H3"); ok {
		t.Error("group without rules must not match")
	}
}

// An exact name must beat a pattern, and a longer prefix must beat a shorter
// one, so a single model can be carved out of a family-wide rule regardless of
// map iteration order.
func TestGetGroupModelRatioPrecedence(t *testing.T) {
	setRules(t, map[string]map[string]float64{
		"g": {
			"*":                  0.9,  // group-wide default
			"happyhorse-*":       0.7,  // family
			"happyhorse-1.1-*":   0.55, // narrower family
			"happyhorse-1.1-r2v": 0.4,  // exact carve-out
		},
	})
	cases := map[string]float64{
		"happyhorse-1.1-r2v": 0.4,  // exact wins
		"happyhorse-1.1-t2v": 0.55, // longest prefix wins
		"happyhorse-1.0-t2v": 0.7,  // shorter family prefix
		"gpt-4.1":            0.9,  // bare * as weakest fallback
	}
	for m, want := range cases {
		got, ok := GetGroupModelRatio("g", m)
		if !ok || got != want {
			t.Errorf("%s: got %v (ok=%v), want %v", m, got, ok, want)
		}
	}
}

func TestGetGroupModelRatioEdgeCases(t *testing.T) {
	setRules(t, map[string]map[string]float64{
		"g":     {"m": 0.5},
		"empty": {},
	})
	for _, tc := range []struct{ group, model string }{
		{"", "m"},        // no group
		{"g", ""},        // no model
		{"empty", "m"},   // group present but no rules
		{"missing", "m"}, // unknown group
	} {
		if _, ok := GetGroupModelRatio(tc.group, tc.model); ok {
			t.Errorf("group=%q model=%q should not match", tc.group, tc.model)
		}
	}

	// A zero ratio is a legitimate value (free for this group) and must be
	// reported as a match rather than confused with "no rule".
	setRules(t, map[string]map[string]float64{"free": {"m": 0}})
	if got, ok := GetGroupModelRatio("free", "m"); !ok || got != 0 {
		t.Errorf("zero ratio must match: got %v ok=%v", got, ok)
	}
}

func TestContainsGroupModelRules(t *testing.T) {
	setRules(t, map[string]map[string]float64{
		"has":  {"m": 0.5},
		"none": {},
	})
	if !ContainsGroupModelRules("has") {
		t.Error("group with rules should report true")
	}
	for _, g := range []string{"none", "missing", ""} {
		if ContainsGroupModelRules(g) {
			t.Errorf("group %q should report false", g)
		}
	}
}

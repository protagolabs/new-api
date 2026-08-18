package ratio_setting

import "testing"

func seedVideoTiers(t *testing.T, data map[string]map[string]float64) {
	t.Helper()
	videoTierRatioMap.Clear()
	for k, v := range data {
		videoTierRatioMap.Set(k, v)
	}
	t.Cleanup(videoTierRatioMap.Clear)
}

func TestGetVideoTierRatioExactMatch(t *testing.T) {
	seedVideoTiers(t, map[string]map[string]float64{
		"veo-3.1-fast-generate-preview": {"1080p": 1.2, "4k": 3.0},
	})

	if r, ok := GetVideoTierRatio("veo-3.1-fast-generate-preview", "1080p"); !ok || r != 1.2 {
		t.Errorf("exact match = %v/%v, want 1.2/true", r, ok)
	}
	// A tier the model has no entry for must report "no opinion", not 0.
	if r, ok := GetVideoTierRatio("veo-3.1-fast-generate-preview", "8k"); ok {
		t.Errorf("unknown tier returned %v/true, want not-found", r)
	}
}

// The point of the whole change: pricing a future generation without a release.
func TestGetVideoTierRatioPrefixPattern(t *testing.T) {
	seedVideoTiers(t, map[string]map[string]float64{
		"veo-4.0-*": {"1080p": 1.4, "4k": 3.2},
	})

	if r, ok := GetVideoTierRatio("veo-4.0-fast-generate-preview", "1080p"); !ok || r != 1.4 {
		t.Errorf("prefix match = %v/%v, want 1.4/true", r, ok)
	}
	if _, ok := GetVideoTierRatio("veo-3.1-fast-generate-preview", "1080p"); ok {
		t.Error("veo-4.0-* must not match a 3.1 model")
	}
}

// A narrower rule must win over a family-wide one whatever the map order.
func TestGetVideoTierRatioLongestPrefixWins(t *testing.T) {
	seedVideoTiers(t, map[string]map[string]float64{
		"veo-*":      {"1080p": 1.1},
		"veo-4.0-*":  {"1080p": 1.4},
		"veo-4.0-fa": {"1080p": 9.9}, // exact-name rule, not a pattern
	})

	for i := 0; i < 20; i++ { // repeat: map iteration order varies per run
		if r, _ := GetVideoTierRatio("veo-4.0-fast-generate-preview", "1080p"); r != 1.4 {
			t.Fatalf("longest prefix = %v, want 1.4", r)
		}
	}
	if r, _ := GetVideoTierRatio("veo-3.9-generate", "1080p"); r != 1.1 {
		t.Errorf("family-wide fallback = %v, want 1.1", r)
	}
}

// Config must be able to correct a built-in value, not merely fill gaps --
// otherwise a vendor price change still needs a release.
func TestGetVideoTierRatioOverridesAreExpressible(t *testing.T) {
	seedVideoTiers(t, map[string]map[string]float64{
		"veo-3.1-fast-generate-preview": {"1080p": 1.5},
	})
	if r, ok := GetVideoTierRatio("veo-3.1-fast-generate-preview", "1080p"); !ok || r != 1.5 {
		t.Errorf("override = %v/%v, want 1.5/true (config must beat the built-in 1.2)", r, ok)
	}
}

func TestHasVideoTierConfig(t *testing.T) {
	seedVideoTiers(t, map[string]map[string]float64{
		"veo-3.1-fast-generate-preview": {"1080p": 1.2},
		"veo-4.0-*":                     {"1080p": 1.4},
	})

	for _, tc := range []struct {
		model string
		want  bool
	}{
		{"veo-3.1-fast-generate-preview", true},
		{"veo-4.0-anything", true},
		{"veo-3.1-lite-generate-preview", false},
		{"", false},
	} {
		if got := HasVideoTierConfig(tc.model); got != tc.want {
			t.Errorf("HasVideoTierConfig(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestGetVideoTierRatioEmptyInputs(t *testing.T) {
	seedVideoTiers(t, map[string]map[string]float64{"veo-*": {"1080p": 1.1}})
	if _, ok := GetVideoTierRatio("", "1080p"); ok {
		t.Error("empty model must not match")
	}
	if _, ok := GetVideoTierRatio("veo-3.1", ""); ok {
		t.Error("empty tier must not match")
	}
}

package gemini

import (
	"testing"

	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

func withVideoTierConfig(t *testing.T, data map[string]map[string]float64) {
	t.Helper()
	s := ratio_setting.GetVideoTierRatioSetting()
	s.VideoTierRatio.Clear()
	for k, v := range data {
		s.VideoTierRatio.Set(k, v)
	}
	t.Cleanup(s.VideoTierRatio.Clear)
}

// The failure this change exists to prevent: a new Veo generation used to
// collapse every tier to 1.0 and undercharge in silence. It must now be
// priceable from configuration, with no code change.
func TestFutureVeoGenerationIsPriceableFromConfig(t *testing.T) {
	withVideoTierConfig(t, map[string]map[string]float64{
		"veo-4.0-fast-*": {"720p": 1.0, "1080p": 1.4, "4k": 3.2},
	})

	for _, tc := range []struct {
		resolution string
		want       float64
	}{
		{"720p", 1.0},
		{"1080p", 1.4},
		{"4k", 3.2},
	} {
		if got := VeoResolutionRatio("veo-4.0-fast-generate-preview", tc.resolution); got != tc.want {
			t.Errorf("veo-4.0 %s = %v, want %v", tc.resolution, got, tc.want)
		}
	}
}

// With nothing configured, a fresh install must still bill Veo 3.1 correctly.
func TestBuiltInTableStillAppliesWithoutConfig(t *testing.T) {
	withVideoTierConfig(t, nil)

	for _, tc := range []struct {
		model      string
		resolution string
		want       float64
	}{
		{"veo-3.1-fast-generate-preview", "1080p", 1.2},
		{"veo-3.1-lite-generate-preview", "1080p", 1.6},
		{"veo-3.1-generate-preview", "4k", 1.5},
		{"veo-3.1-fast-generate-preview", "4k", 3.0},
	} {
		if got := VeoResolutionRatio(tc.model, tc.resolution); got != tc.want {
			t.Errorf("%s %s = %v, want built-in %v", tc.model, tc.resolution, got, tc.want)
		}
	}
}

// A vendor price change must be fixable by editing the option, so config has to
// beat the built-in value rather than only fill gaps.
func TestConfigOverridesBuiltIn(t *testing.T) {
	withVideoTierConfig(t, map[string]map[string]float64{
		"veo-3.1-fast-generate-preview": {"1080p": 1.35},
	})

	if got := VeoResolutionRatio("veo-3.1-fast-generate-preview", "1080p"); got != 1.35 {
		t.Errorf("1080p = %v, want the configured 1.35 to beat the built-in 1.2", got)
	}
	// Tiers the override does not mention must keep falling back.
	if got := VeoResolutionRatio("veo-3.1-fast-generate-preview", "4k"); got != 3.0 {
		t.Errorf("4k = %v, want built-in 3.0 to still apply", got)
	}
}

// 720p is the rate ModelPrice is quoted at, so an unconfigured 720p is correct
// at 1.0 and must not be treated as a pricing gap.
func TestBaseResolutionNeedsNoConfig(t *testing.T) {
	withVideoTierConfig(t, nil)
	if got := VeoResolutionRatio("veo-9.9-unknown-generate", "720p"); got != 1.0 {
		t.Errorf("720p = %v, want 1.0", got)
	}
}

// An unpriced non-base tier still bills 1.0 (we cannot invent a number), but
// the point is that it no longer does so quietly -- see WarnUnpricedVideoTier.
func TestUnpricedTierFallsBackToNeutral(t *testing.T) {
	withVideoTierConfig(t, nil)
	if got := VeoResolutionRatio("veo-9.9-unknown-generate", "1080p"); got != 1.0 {
		t.Errorf("unpriced 1080p = %v, want neutral 1.0 fallback", got)
	}
}

package gemini

import "testing"

// ModelPrice holds each family's 720p per-second rate, so 720p must be exactly
// 1.0 or every clip is mispriced.
func TestVeoResolutionRatioBaselineIs720p(t *testing.T) {
	for _, model := range []string{
		"veo-3.1-generate-preview",
		"veo-3.1-fast-generate-preview",
		"veo-3.1-lite-generate-preview",
	} {
		if got := VeoResolutionRatio(model, "720p"); got != 1.0 {
			t.Errorf("%s 720p ratio = %v, want 1.0", model, got)
		}
	}
}

// 1080p used to bill at the 720p rate. It is a genuinely different price for
// fast and lite, and the same price only for standard.
func TestVeoResolutionRatio1080p(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  float64
		why   string
	}{
		{"veo-3.1-generate-preview", 1.0, "$0.40 = $0.40"},
		{"veo-3.1-fast-generate-preview", 1.2, "$0.12 / $0.10"},
		{"veo-3.1-lite-generate-preview", 1.6, "$0.08 / $0.05"},
	} {
		if got := VeoResolutionRatio(tc.model, "1080p"); got != tc.want {
			t.Errorf("%s 1080p ratio = %v, want %v (%s)", tc.model, got, tc.want, tc.why)
		}
	}
}

// The 4k fast multiplier was 2.333, computed from Vertex AI's price list while
// we bill against the Gemini API. That undercharged by 22%.
func TestVeoResolutionRatio4k(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  float64
		why   string
	}{
		{"veo-3.1-generate-preview", 1.5, "$0.60 / $0.40"},
		{"veo-3.1-fast-generate-preview", 3.0, "$0.30 / $0.10, Gemini API not Vertex"},
	} {
		if got := VeoResolutionRatio(tc.model, "4k"); got != tc.want {
			t.Errorf("%s 4k ratio = %v, want %v (%s)", tc.model, got, tc.want, tc.why)
		}
	}
	// Lite has no 4k tier; upstream rejects it, so a neutral multiplier is the
	// right answer rather than borrowing another family's.
	if got := VeoResolutionRatio("veo-3.1-lite-generate-preview", "4k"); got != 1.0 {
		t.Errorf("lite 4k ratio = %v, want 1.0 (unsupported upstream)", got)
	}
}

// Family detection must not be fooled by the shared "generate" substring: the
// old implementation matched lite against the standard branch.
func TestVeoFamilyDetection(t *testing.T) {
	for _, tc := range []struct{ model, want string }{
		{"veo-3.1-generate-preview", "standard"},
		{"veo-3.1-fast-generate-preview", "fast"},
		{"veo-3.1-lite-generate-preview", "lite"},
	} {
		if got := veoFamily(tc.model); got != tc.want {
			t.Errorf("veoFamily(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
}

// Shut-down generations (Veo 2.0 / 3.0, off since 2026-06-30) and unknown names
// must keep the neutral multiplier rather than inherit 3.1's tiers.
func TestVeoResolutionRatioIgnoresOldGenerations(t *testing.T) {
	for _, model := range []string{
		"veo-3.0-generate-001",
		"veo-3.0-fast-generate-001",
		"veo-2.0-generate-001",
		"something-else",
	} {
		for _, res := range []string{"720p", "1080p", "4k"} {
			if got := VeoResolutionRatio(model, res); got != 1.0 {
				t.Errorf("%s %s ratio = %v, want 1.0", model, res, got)
			}
		}
	}
}

// An unrecognised resolution label must not fall through to a priced tier.
func TestVeoResolutionRatioUnknownResolution(t *testing.T) {
	if got := VeoResolutionRatio("veo-3.1-fast-generate-preview", "8k"); got != 1.0 {
		t.Errorf("unknown resolution ratio = %v, want 1.0", got)
	}
}

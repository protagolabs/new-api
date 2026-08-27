package sora

import "testing"

// The exact failure that made grok-imagine-video-1.5 unusable: OpenRouter
// answered `Invalid size format: "720p". Expected "WIDTHxHEIGHT"`.
func TestNormalizeVideoSizeExpandsTiers(t *testing.T) {
	cases := map[string]string{
		"480p":    "854x480",
		"720p":    "1280x720",
		"1080p":   "1920x1080",
		"720P":    "1280x720",
		" 1080p ": "1920x1080",
	}
	for in, want := range cases {
		if got := normalizeVideoSize(in); got != want {
			t.Errorf("normalizeVideoSize(%q) = %q, want %q", in, got, want)
		}
	}
}

// Explicit pixel sizes must survive untouched -- that is how a caller asks for
// portrait, and Sora's own sizes come through this same path.
func TestNormalizeVideoSizeLeavesPixelSizesAlone(t *testing.T) {
	for _, raw := range []string{
		"720x1280", "1280x720", "1792x1024", "1024x1792", "1920x1080",
		"", "auto", "4k",
	} {
		if got := normalizeVideoSize(raw); got != raw {
			t.Errorf("normalizeVideoSize(%q) altered it to %q", raw, got)
		}
	}
}

// Expansion has to make the tier visible to billing as well: videoTierFromSize
// splits on "x", so before this a tier name resolved to no tier at all and the
// multiplier silently fell back to the 480p base rate.
func TestNormalizedTierIsBillable(t *testing.T) {
	cases := map[string]string{
		"480p":  "480p",
		"720p":  "720p",
		"1080p": "1080p",
	}
	for in, wantTier := range cases {
		if got := videoTierFromSize(in); got != "" {
			t.Errorf("videoTierFromSize(%q) = %q; a raw tier name should not parse", in, got)
		}
		if got := videoTierFromSize(normalizeVideoSize(in)); got != wantTier {
			t.Errorf("videoTierFromSize(normalize(%q)) = %q, want %q", in, got, wantTier)
		}
	}
}

// Portrait and landscape of one tier must price alike, which is how xAI bills
// them -- so expansion must not change the tier a caller would have got by
// sending pixels directly.
func TestTierAgnosticToOrientation(t *testing.T) {
	if a, b := videoTierFromSize("1280x720"), videoTierFromSize("720x1280"); a != b {
		t.Errorf("orientation changed tier: %q vs %q", a, b)
	}
	if got := videoTierFromSize(normalizeVideoSize("720p")); got != videoTierFromSize("720x1280") {
		t.Errorf("expanded 720p tier %q differs from portrait 720p", got)
	}
}

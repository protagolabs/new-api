package doubao

import "testing"

// Tier labels are what the vendor accepts and what our docs prescribe; these
// must keep working unchanged.
func TestNormalizeDreaminaResolutionTierLabels(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"480p", Dreamina480P},
		{"720p", Dreamina720P},
		{"1080p", Dreamina1080P},
		{"4k", Dreamina4K},
		{"1080P", Dreamina1080P},
		{"UHD", Dreamina4K},
		{"480", Dreamina480P},
	} {
		if got := NormalizeDreaminaResolution(tc.in); got != tc.want {
			t.Errorf("NormalizeDreaminaResolution(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Pixel dimensions now resolve too. Before this, "854x480" produced an empty
// resolution, the vendor rendered its 720p default, and the caller paid for
// 720p — 2.25x the tokens of the 480p they asked for. Measured in production:
// 87,300 tokens instead of 38,830.
func TestNormalizeDreaminaResolutionPixels(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"854x480", Dreamina480P},
		{"1280x720", Dreamina720P},
		{"1920x1080", Dreamina1080P},
		{"3840x2160", Dreamina4K},
		{"480x854", Dreamina480P},    // portrait
		{"1080x1920", Dreamina1080P}, // portrait
		{" 1280 x 720 ", Dreamina720P},
	} {
		if got := NormalizeDreaminaResolution(tc.in); got != tc.want {
			t.Errorf("NormalizeDreaminaResolution(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A size between tiers must round *down*, never up: silently upgrading the
// caller into a pricier tier is the one failure mode that costs them money.
func TestNormalizeDreaminaResolutionRoundsDown(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"1024x576", Dreamina480P},   // below 1280 -> 480p, not 720p
		{"1600x900", Dreamina720P},   // below 1920 -> 720p, not 1080p
		{"2560x1440", Dreamina1080P}, // below 3840 -> 1080p, not 4k
	} {
		if got := NormalizeDreaminaResolution(tc.in); got != tc.want {
			t.Errorf("NormalizeDreaminaResolution(%q) = %q, want %q (must round down)", tc.in, got, tc.want)
		}
	}
}

// Garbage must stay empty so callers fall back to the vendor default rather
// than being assigned an arbitrary tier.
func TestNormalizeDreaminaResolutionRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "nonsense", "x", "0x0", "-100x200", "axb", "1280x"} {
		if got := NormalizeDreaminaResolution(in); got != "" {
			t.Errorf("NormalizeDreaminaResolution(%q) = %q, want empty", in, got)
		}
	}
}

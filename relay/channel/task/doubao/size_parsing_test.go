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

// Seedance 2.5 gained 1080p support (10-bit, H.265). It is a dearer tier than
// 480p/720p, which share one rate — pricing it at the shared rate would
// undercharge roughly 9% on every 1080p clip.
func TestDreamina25HasA1080pTier(t *testing.T) {
	const model = "dreamina-seedance-2-5-260628"

	// 480p and 720p are the same rate, so their multiplier is exactly 1.
	for _, res := range []string{Dreamina480P, Dreamina720P} {
		if got := dreaminaTierRatio(model, res, false); got != 1.0 {
			t.Errorf("%s %s = %v, want 1.0", model, res, got)
		}
	}

	// 1080p: $11.70 against the $10.70 base.
	want := 11.70 / 10.70
	if got := dreaminaTierRatio(model, Dreamina1080P, false); got != want {
		t.Errorf("1080p = %v, want %v ($11.70 / $10.70)", got, want)
	}

	// With a video input the whole scale shifts down, and 1080p keeps its
	// premium: $7.00 against the same $10.70 base.
	wantVideo := 7.00 / 10.70
	if got := dreaminaTierRatio(model, Dreamina1080P, true); got != wantVideo {
		t.Errorf("1080p with video = %v, want %v ($7.00 / $10.70)", got, wantVideo)
	}
	wantVideoBase := 6.40 / 10.70
	if got := dreaminaTierRatio(model, Dreamina720P, true); got != wantVideoBase {
		t.Errorf("720p with video = %v, want %v ($6.40 / $10.70)", got, wantVideoBase)
	}

	// 2.5 has no 4K tier; an unlisted tier must fall back to the base rate
	// rather than borrow 2.0's much cheaper 4K figure.
	if got := dreaminaTierRatio(model, Dreamina4K, false); got != 1.0 {
		t.Errorf("4K = %v, want the 1.0 base fallback (2.5 has no 4K tier)", got)
	}
}

// Seedance 2.0's tiers are unchanged upstream; pin them so a future edit to the
// 2.5 table cannot silently disturb them.
func TestDreamina20TiersUnchanged(t *testing.T) {
	const model = "dreamina-seedance-2-0-260128"
	for _, tc := range []struct {
		res      string
		hasVideo bool
		want     float64
	}{
		{Dreamina480P, false, 1.0},
		{Dreamina1080P, false, 7.7 / 7.0},
		{Dreamina4K, false, 4.0 / 7.0},
		{Dreamina480P, true, 4.3 / 7.0},
		{Dreamina1080P, true, 4.7 / 7.0},
		{Dreamina4K, true, 2.4 / 7.0},
	} {
		if got := dreaminaTierRatio(model, tc.res, tc.hasVideo); got != tc.want {
			t.Errorf("2.0 %s video=%v = %v, want %v", tc.res, tc.hasVideo, got, tc.want)
		}
	}
}

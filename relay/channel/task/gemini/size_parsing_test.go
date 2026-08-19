package gemini

import "testing"

// Our published docs tell Veo callers to send "720p" / "1080p" / "4k". Those
// labels used to fall through to the 720p default because the parser only
// understood WxH, so a caller following the documentation silently received a
// lower resolution — and a bill that matched it, making the loss invisible.
func TestSizeToVeoResolutionAcceptsTierLabels(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"720p", "720p"},
		{"1080p", "1080p"},
		{"4k", "4k"},
		{"1080P", "1080p"}, // case must not matter
		{"  4K  ", "4k"},   // nor surrounding space
		{"2160p", "4k"},    // common alias
		{"fhd", "1080p"},
		{"uhd", "4k"},
	} {
		if got := SizeToVeoResolution(tc.in); got != tc.want {
			t.Errorf("SizeToVeoResolution(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The WxH form has to keep working exactly as before.
func TestSizeToVeoResolutionStillAcceptsPixels(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"1280x720", "720p"},
		{"1920x1080", "1080p"},
		{"3840x2160", "4k"},
		{"1080x1920", "1080p"}, // portrait: the larger side decides
		{"854x480", "720p"},    // below the 1080p threshold
	} {
		if got := SizeToVeoResolution(tc.in); got != tc.want {
			t.Errorf("SizeToVeoResolution(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Anything unparseable must land on the documented default rather than error.
func TestSizeToVeoResolutionFallback(t *testing.T) {
	for _, in := range []string{"", "nonsense", "x", "axb"} {
		if got := SizeToVeoResolution(in); got != "720p" {
			t.Errorf("SizeToVeoResolution(%q) = %q, want the 720p default", in, got)
		}
	}
}

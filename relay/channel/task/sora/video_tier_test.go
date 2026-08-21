package sora

import "testing"

func TestVideoTierFromSize(t *testing.T) {
	cases := map[string]string{
		"854x480":   "480p",
		"480x854":   "480p", // portrait of the same tier prices alike
		"1280x720":  "720p",
		"720x1280":  "720p",
		"1920x1080": "1080p",
		"1080x1920": "1080p",
		"3840x2160": "1080p", // above the top tier still maps to it
		"1792x1024": "720p",  // Sora's own size; the config lookup is what keeps it out
		"":          "",
		"720p":      "", // a tier label, not WxH — no "x" separator
		"0x0":       "",
	}
	for size, want := range cases {
		if got := videoTierFromSize(size); got != want {
			t.Errorf("videoTierFromSize(%q) = %q, want %q", size, got, want)
		}
	}
}

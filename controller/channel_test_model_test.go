package controller

import "testing"

// Testing a Veo model over chat/completions yields a 404 that reads like a dead
// channel. Those models must be refused before the request is built.
func TestIsNonChatTestModel(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  bool
	}{
		{"veo-3.1-generate-preview", true},
		{"veo-3.1-fast-generate-preview", true},
		{"veo-3.1-lite-generate-preview", true},
		{"veo-3.0-generate-001", true},
		{"imagen-4.0-generate-001", true},
		{"VEO-3.1-GENERATE-PREVIEW", true}, // case must not matter
		{"  veo-3.1-generate-preview  ", true},

		// Chat models on the same channel type must stay testable, or the fix
		// would blind us to real Gemini outages.
		{"gemini-3.7-flash", false},
		{"gemini-3.1-pro-preview", false},
		{"gpt-4o-mini", false},
		{"claude-sonnet-4-6", false},
		{"", false},
	} {
		if got := isNonChatTestModel(tc.model); got != tc.want {
			t.Errorf("isNonChatTestModel(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

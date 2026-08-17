package middleware

import "testing"

// The case this was built for: a customer sent "MinMax-H3" for two days and got
// 503s that read as an outage on our side. The name is one character from a
// real model, so the answer should say so.
func TestLevenshteinCatchesTheRealTypo(t *testing.T) {
	if d := levenshtein("minmax-h3", "minimax-h3"); d != 1 {
		t.Errorf("MinMax-H3 vs MiniMax-H3: distance %d, want 1", d)
	}
}

// The suggestion budget has to be wide enough for a one-character slip in a
// short name, and narrow enough that unrelated models are not proposed.
func TestSuggestionBudget(t *testing.T) {
	budget := func(name string) int { return len(name)/4 + 1 }

	for _, tc := range []struct {
		typo, target string
		wantSuggest  bool
	}{
		{"minmax-h3", "minimax-h3", true}, // 1 edit, budget 3
		{"happyhorse-1.1-t2b", "happyhorse-1.1-t2v", true},
		{"gpt-4.1", "gpt-4.1", true},            // identical
		{"claude-sonnet-4-6", "gpt-4.1", false}, // unrelated
		{"veo-3.0-generate-001", "MiniMax-H3", false},
	} {
		d := levenshtein(tc.typo, tc.target)
		got := d <= budget(tc.typo)
		if got != tc.wantSuggest {
			t.Errorf("%q vs %q: distance %d, budget %d, suggest=%v want %v",
				tc.typo, tc.target, d, budget(tc.typo), got, tc.wantSuggest)
		}
	}
}

func TestLevenshteinBasics(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"", "abc", 3},
		{"abc", "", 3},
		{"abc", "abc", 0},
		{"kitten", "sitting", 3},
		{"a", "b", 1},
	} {
		if got := levenshtein(tc.a, tc.b); got != tc.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// An empty or blank model name must not be treated as known, or a malformed
// request would fall through to the "temporarily unavailable" branch.
func TestIsKnownModelRejectsBlank(t *testing.T) {
	for _, name := range []string{"", "   "} {
		if isKnownModel(name) {
			t.Errorf("blank model name %q must not be known", name)
		}
	}
	if suggestModel("") != "" {
		t.Error("blank model name must not produce a suggestion")
	}
}

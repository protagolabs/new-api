package dto

import "testing"

// The two fields carry the same quantity from different upstream shapes, and when
// they disagree the total is the authoritative one. Taking the larger value used
// to charge callers for a system prompt one upstream prepends on its own: it
// counted that prefix in the TTL buckets (which feed CacheWriteTokens) without
// updating the total, producing a fixed 1000-token gap on ~13% of cache writes.
func TestCacheCreationTokensTotalPrefersTheAuthoritativeTotal(t *testing.T) {
	cases := []struct {
		name   string
		cached int
		write  int
		want   int
		why    string
	}{
		{
			name:   "buckets inflated by an injected prefix",
			cached: 1731, write: 2731, want: 1731,
			why: "the caller sent 1731; the extra 1000 is the upstream's own prompt",
		},
		{
			name:   "fields agree",
			cached: 1450, write: 1450, want: 1450,
			why: "the common case must be untouched",
		},
		{
			name:   "OpenAI-native shape reports only cache_write_tokens",
			cached: 0, write: 900, want: 900,
			why: "no total available, so the buckets are all we have",
		},
		{
			name:   "neither reported",
			cached: 0, write: 0, want: 0,
			why: "nothing to bill",
		},
		{
			name:   "negative total clamps rather than crediting",
			cached: -5, write: 0, want: 0,
			why: "a bad upstream value must never lower a charge",
		},
		{
			name:   "negative total falls through to the buckets",
			cached: -5, write: 700, want: 700,
			why: "<=0 means unreported, not zero",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := InputTokenDetails{CachedCreationTokens: c.cached, CacheWriteTokens: c.write}
			if got := d.CacheCreationTokensTotal(); got != c.want {
				t.Errorf("cached=%d write=%d -> %d, want %d (%s)", c.cached, c.write, got, c.want, c.why)
			}
		})
	}
}

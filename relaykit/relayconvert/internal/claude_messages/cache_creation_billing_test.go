package claudemessages

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// This function picks the number that gets billed, so a wrong choice here is a
// wrong invoice. The total is authoritative and the per-TTL buckets are its
// breakdown; when they disagree it is because an upstream counted its own
// injected prefix in the buckets without updating the total. Billing the buckets
// there charges the caller for a prefix they never sent, while the response they
// receive shows the smaller total -- invoice and usage block contradict.
func TestCacheCreationTokensForOpenAIUsage(t *testing.T) {
	usage := func(total, b5m, b1h int) *dto.Usage {
		u := &dto.Usage{}
		u.PromptTokensDetails.CachedCreationTokens = total
		u.ClaudeCacheCreation5mTokens = b5m
		u.ClaudeCacheCreation1hTokens = b1h
		return u
	}

	cases := []struct {
		name string
		in   *dto.Usage
		want int
		why  string
	}{
		{
			name: "buckets inflated by an injected prefix",
			in:   usage(1971, 2971, 0),
			want: 1971,
			why:  "the caller sent 1971; the extra 1000 belongs to the upstream",
		},
		{
			name: "fields agree",
			in:   usage(1521, 1521, 0),
			want: 1521,
			why:  "the common case must be untouched",
		},
		{
			name: "no total reported, buckets are all we have",
			in:   usage(0, 900, 100),
			want: 1000,
			why:  "some responses carry only the breakdown",
		},
		{
			name: "total larger than buckets",
			in:   usage(2000, 1200, 0),
			want: 2000,
			why:  "the total was already preferred in this direction",
		},
		{
			name: "nothing reported",
			in:   usage(0, 0, 0),
			want: 0,
			why:  "nothing to bill",
		},
		{
			name: "nil usage",
			in:   nil,
			want: 0,
			why:  "must not panic on a missing usage block",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cacheCreationTokensForOpenAIUsage(c.in); got != c.want {
				t.Errorf("got %d, want %d (%s)", got, c.want, c.why)
			}
		})
	}
}

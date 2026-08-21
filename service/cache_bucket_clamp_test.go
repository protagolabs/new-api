package service

import "testing"

// The quota math multiplies the per-TTL buckets by their ratios, so a bucket
// larger than the authoritative total bills tokens the caller never sent. One
// upstream prepends its own system prompt and counts it in the buckets without
// updating the total, which is exactly that shape.
func TestClampCacheBucketsToTotal(t *testing.T) {
	cases := []struct {
		name           string
		total          int
		in5m, in1h     int
		want5m, want1h int
		why            string
	}{
		{
			name:  "5m inflated past the total by an injected prefix",
			total: 1811, in5m: 2811, in1h: 0,
			want5m: 1811, want1h: 0,
			why: "only the caller's 1811 is billable",
		},
		{
			name:  "buckets already sum to the total",
			total: 1361, in5m: 1361, in1h: 0,
			want5m: 1361, want1h: 0,
			why: "the common case must pass through untouched",
		},
		{
			name:  "legitimate mixed TTL within the total",
			total: 2000, in5m: 1200, in1h: 800,
			want5m: 1200, want1h: 800,
			why: "sums to the total, nothing to clamp",
		},
		{
			name:  "mixed TTL over the total keeps the pricier 1h tier",
			total: 1500, in5m: 1200, in1h: 800,
			want5m: 700, want1h: 800,
			why: "shrinking 1h first would under-charge",
		},
		{
			name:  "1h alone exceeds the total",
			total: 500, in5m: 0, in1h: 900,
			want5m: 0, want1h: 500,
			why: "cap at the total rather than going negative on 5m",
		},
		{
			name:  "no total reported leaves buckets alone",
			total: 0, in5m: 900, in1h: 0,
			want5m: 900, want1h: 0,
			why: "nothing authoritative to clamp against",
		},
		{
			name:  "buckets under the total are not padded",
			total: 2000, in5m: 500, in1h: 0,
			want5m: 500, want1h: 0,
			why: "this function only clamps down, never up",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got5m, got1h := clampCacheBucketsToTotal(c.total, c.in5m, c.in1h)
			if got5m != c.want5m || got1h != c.want1h {
				t.Errorf("total=%d in=(%d,%d) -> (%d,%d), want (%d,%d) — %s",
					c.total, c.in5m, c.in1h, got5m, got1h, c.want5m, c.want1h, c.why)
			}
		})
	}
}

// The billed figure that lands in the usage log must never exceed the total once
// the buckets are clamped — that equality is what makes the invoice match the
// usage block the client received.
func TestCacheWriteTokensTotalMatchesClampedBuckets(t *testing.T) {
	total := 1811
	b5m, b1h := clampCacheBucketsToTotal(total, 2811, 0)
	s := textQuotaSummary{
		CacheCreationTokens:   total,
		CacheCreationTokens5m: b5m,
		CacheCreationTokens1h: b1h,
	}
	if got := cacheWriteTokensTotal(s); got != total {
		t.Errorf("billed %d, want %d — invoice would disagree with the response", got, total)
	}
}

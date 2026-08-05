package xai

import (
	"math"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

func approx(t *testing.T, got, want float64, what string) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s: got %v, want %v", what, got, want)
	}
}

// The whole point of the composite ratio is that
// modelPrice * seconds * resolution * media == the exact xAI bill.
func TestBillingRatiosMatchOfficialPricing(t *testing.T) {
	cases := []struct {
		name              string
		model             string
		modelPrice        float64 // configured 480p per-second price
		seconds           int
		resolution        string
		images            int
		inputVideoSeconds int
		wantUSD           float64
	}{
		// grok-imagine-video: 480p $0.05/s, 720p $0.07/s
		{"video 480p 6s", "grok-imagine-video", 0.05, 6, "480p", 0, 0, 0.30},
		{"video 720p 10s", "grok-imagine-video", 0.05, 10, "720p", 0, 0, 0.70},
		{"video 720p 15s max", "grok-imagine-video", 0.05, 15, "720p", 0, 0, 1.05},
		// + input media: image $0.002, input video $0.01/s
		{"video 480p 5s +2img", "grok-imagine-video", 0.05, 5, "480p", 2, 0, 0.25 + 0.004},
		{"video 480p 5s +3s vid", "grok-imagine-video", 0.05, 5, "480p", 0, 3, 0.25 + 0.03},
		// grok-imagine-video-1.5: 480p $0.08/s, 720p $0.14/s, 1080p $0.25/s
		{"v1.5 480p 4s", "grok-imagine-video-1.5", 0.08, 4, "480p", 0, 0, 0.32},
		{"v1.5 720p 4s", "grok-imagine-video-1.5", 0.08, 4, "720p", 0, 0, 0.56},
		{"v1.5 1080p 15s max", "grok-imagine-video-1.5", 0.08, 15, "1080p", 0, 0, 3.75},
		// v1.5 input image is $0.01; 7 reference images is the documented max
		{"v1.5 720p 5s +7img", "grok-imagine-video-1.5", 0.08, 5, "720p", 7, 0, 0.70 + 0.07},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := BillingRatios(tc.model, tc.modelPrice, tc.seconds, tc.resolution,
				tc.images, tc.inputVideoSeconds)
			total := tc.modelPrice * r["seconds"] * r["resolution"] * r["media"]
			approx(t, total, tc.wantUSD, "total USD")
		})
	}
}

// An unknown model or resolution must never inflate the bill.
func TestRatiosFallBackToOne(t *testing.T) {
	if got := ResolutionRatio("grok-imagine-video", "4k"); got != 1.0 {
		t.Errorf("unknown resolution: got %v, want 1.0", got)
	}
	if got := ResolutionRatio("some-unknown-model", "720p"); got != 1.0 {
		t.Errorf("unknown model: got %v, want 1.0", got)
	}
	if got := InputMediaCost("some-unknown-model", 5, 5); got != 0 {
		t.Errorf("unknown model input cost: got %v, want 0", got)
	}
	// A zero base price would make the media ratio meaningless.
	if got := MediaRatio(0, 5, 1, 0.05); got != 1.0 {
		t.Errorf("zero model price: got %v, want 1.0", got)
	}
}

// Vendor-prefixed / uppercase names must still resolve to a pricing entry,
// otherwise a model_mapping rename would silently drop resolution pricing.
func TestNormalizeModelName(t *testing.T) {
	for _, name := range []string{"grok-imagine-video-1.5", "xai/grok-imagine-video-1.5", "GROK-IMAGINE-VIDEO-1.5"} {
		if got := ResolutionRatio(name, "1080p"); math.Abs(got-3.125) > 1e-9 {
			t.Errorf("%s: got %v, want 3.125", name, got)
		}
	}
}

func TestResolveDuration(t *testing.T) {
	cases := []struct {
		name     string
		metadata map[string]any
		duration int
		seconds  string
		want     int
	}{
		{"default when unset", nil, 0, "", DefaultDurationSeconds},
		{"std duration", nil, 10, "", 10},
		{"seconds string", nil, 0, "7", 7},
		{"metadata wins", map[string]any{"duration": float64(12)}, 5, "", 12},
		{"metadata int", map[string]any{"duration": 9}, 0, "", 9},
		// Duration is a direct billing multiplier and the metadata path skips
		// request validation, so it must be capped.
		{"capped at max", map[string]any{"duration": float64(9999)}, 0, "", MaxDurationSeconds},
		{"negative ignored", nil, -3, "", DefaultDurationSeconds},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveDuration(tc.metadata, tc.duration, tc.seconds); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestResolveResolutionAndSize(t *testing.T) {
	if got := ResolveResolution(nil, ""); got != DefaultResolution {
		t.Errorf("default: got %q, want %q", got, DefaultResolution)
	}
	if got := ResolveResolution(map[string]any{"resolution": "1080P"}, "1280x720"); got != "1080p" {
		t.Errorf("metadata should win and lowercase: got %q", got)
	}
	for size, want := range map[string]string{
		"1920x1080": "1080p", "1280x720": "720p", "854x480": "480p", "garbage": DefaultResolution,
	} {
		if got := SizeToResolution(size); got != want {
			t.Errorf("SizeToResolution(%q): got %q, want %q", size, got, want)
		}
	}
	if got := SizeToAspectRatio("720x1280"); got != "9:16" {
		t.Errorf("portrait: got %q, want 9:16", got)
	}
	if got := SizeToAspectRatio("1920x1080"); got != "16:9" {
		t.Errorf("landscape: got %q, want 16:9", got)
	}
}

// A duration echoed back by the upstream is untrusted input used as a billing
// multiplier, so it must be clamped.
func TestCapDuration(t *testing.T) {
	if got := CapDuration(-1); got != 0 {
		t.Errorf("negative: got %d, want 0", got)
	}
	if got := CapDuration(8); got != 8 {
		t.Errorf("normal: got %d, want 8", got)
	}
	if got := CapDuration(relaycommon.MaxTaskDurationSeconds + 1000); got != relaycommon.MaxTaskDurationSeconds {
		t.Errorf("over cap: got %d, want %d", got, relaycommon.MaxTaskDurationSeconds)
	}
}

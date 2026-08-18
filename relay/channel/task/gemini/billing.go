package gemini

import (
	"strconv"
	"strings"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// ParseVeoDurationSeconds extracts durationSeconds from metadata.
// Returns 8 (Veo default) when not specified or invalid.
func ParseVeoDurationSeconds(metadata map[string]any) int {
	if metadata == nil {
		return 8
	}
	v, ok := metadata["durationSeconds"]
	if !ok {
		return 8
	}
	switch n := v.(type) {
	case float64:
		if int(n) > 0 {
			return int(n)
		}
	case int:
		if n > 0 {
			return n
		}
	}
	return 8
}

// ParseVeoResolution extracts resolution from metadata.
// Returns "720p" when not specified.
func ParseVeoResolution(metadata map[string]any) string {
	if metadata == nil {
		return "720p"
	}
	v, ok := metadata["resolution"]
	if !ok {
		return "720p"
	}
	if s, ok := v.(string); ok && s != "" {
		return strings.ToLower(s)
	}
	return "720p"
}

// ResolveVeoDuration returns the effective duration in seconds.
// Priority: metadata["durationSeconds"] > stdDuration > stdSeconds > default (8).
// The result is capped because it is used as a billing multiplier and the
// metadata path bypasses standard request validation.
func ResolveVeoDuration(metadata map[string]any, stdDuration int, stdSeconds string) int {
	if metadata != nil {
		if _, exists := metadata["durationSeconds"]; exists {
			if d := ParseVeoDurationSeconds(metadata); d > 0 {
				return min(d, relaycommon.MaxTaskDurationSeconds)
			}
		}
	}
	if stdDuration > 0 {
		return min(stdDuration, relaycommon.MaxTaskDurationSeconds)
	}
	if s, err := strconv.Atoi(stdSeconds); err == nil && s > 0 {
		return min(s, relaycommon.MaxTaskDurationSeconds)
	}
	return 8
}

// ResolveVeoResolution returns the effective resolution string (lowercase).
// Priority: metadata["resolution"] > SizeToVeoResolution(stdSize) > default ("720p").
func ResolveVeoResolution(metadata map[string]any, stdSize string) string {
	if metadata != nil {
		if _, exists := metadata["resolution"]; exists {
			if r := ParseVeoResolution(metadata); r != "" {
				return r
			}
		}
	}
	if stdSize != "" {
		return SizeToVeoResolution(stdSize)
	}
	return "720p"
}

// SizeToVeoResolution converts a "WxH" size string to a Veo resolution label.
func SizeToVeoResolution(size string) string {
	parts := strings.SplitN(strings.ToLower(size), "x", 2)
	if len(parts) != 2 {
		return "720p"
	}
	w, _ := strconv.Atoi(parts[0])
	h, _ := strconv.Atoi(parts[1])
	maxDim := w
	if h > maxDim {
		maxDim = h
	}
	if maxDim >= 3840 {
		return "4k"
	}
	if maxDim >= 1920 {
		return "1080p"
	}
	return "720p"
}

// SizeToVeoAspectRatio converts a "WxH" size string to a Veo aspect ratio.
func SizeToVeoAspectRatio(size string) string {
	parts := strings.SplitN(strings.ToLower(size), "x", 2)
	if len(parts) != 2 {
		return "16:9"
	}
	w, _ := strconv.Atoi(parts[0])
	h, _ := strconv.Atoi(parts[1])
	if w <= 0 || h <= 0 {
		return "16:9"
	}
	if h > w {
		return "9:16"
	}
	return "16:9"
}

// veoBaseResolution is the tier ModelPrice itself is quoted at, so a missing
// entry for it is not a pricing gap.
const veoBaseResolution = "720p"

// veoResolutionRatios maps a Veo 3.1 family to its per-resolution multiplier,
// relative to that family's own 720p rate (which is what ModelPrice holds).
//
// Derived from the Gemini API standard tier, ai.google.dev/gemini-api/docs/pricing
// as of 2026-08-17 (per second of video):
//
//	              720p     1080p    4k
//	standard      $0.40    $0.40    $0.60
//	fast          $0.10    $0.12    $0.30
//	lite          $0.05    $0.08    unsupported
//
// Prices must come from the Gemini API page, not Vertex AI: Vertex keeps a
// separate without-audio tier at roughly half price while the Gemini API has a
// single audio-inclusive rate, so the two are not interchangeable. The previous
// 4k fast multiplier (2.333) was computed from Vertex's $0.35/$0.15 and
// undercharged by 22% against the $0.30/$0.10 we actually pay.
var veoResolutionRatios = map[string]map[string]float64{
	"standard": {"720p": 1.0, "1080p": 1.0, "4k": 1.5},
	"fast":     {"720p": 1.0, "1080p": 1.2, "4k": 3.0},
	// Lite has no 4k tier; a 4k request is rejected upstream before billing.
	"lite": {"720p": 1.0, "1080p": 1.6},
}

// veoFamily classifies a Veo model name. Order matters: every name contains
// "generate", and the lite/fast names are the more specific ones.
func veoFamily(modelName string) string {
	switch {
	case strings.Contains(modelName, "lite"):
		return "lite"
	case strings.Contains(modelName, "fast"):
		return "fast"
	default:
		return "standard"
	}
}

// VeoResolutionRatio returns the pricing multiplier for the given resolution,
// relative to the model's 720p rate.
//
// 1080p is a genuinely different price for the fast and lite families, so
// treating it as 720p undercharges by 17% and 37% respectively. Only the
// standard family prices 720p and 1080p the same.
//
// Resolution order: the video_tier_ratio option first, then the built-in table
// above. Config first means a new Veo generation is a price-list edit rather
// than a release; the built-in table means a fresh install still bills 3.1
// correctly with nothing configured.
func VeoResolutionRatio(modelName, resolution string) float64 {
	if ratio, ok := ratio_setting.GetVideoTierRatio(modelName, resolution); ok {
		return ratio
	}
	// The built-in table describes Veo 3.1 only. Earlier generations are shut
	// down upstream (2026-06-30).
	if strings.Contains(modelName, "3.1") {
		if ratio, ok := veoResolutionRatios[veoFamily(modelName)][resolution]; ok {
			return ratio
		}
	}
	// Neither source priced this tier. 720p is the base rate ModelPrice already
	// holds, so 1.0 is right there and not worth a warning; anything else means
	// a surcharge we are about to miss -- say so rather than undercharge in
	// silence, which is exactly how the 3.1 tiers went unnoticed.
	if resolution != veoBaseResolution && strings.HasPrefix(modelName, "veo-") {
		ratio_setting.WarnUnpricedVideoTier(modelName, resolution)
	}
	return 1.0
}

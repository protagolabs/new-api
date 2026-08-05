package xai

import (
	"strconv"
	"strings"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// xAI prices video generation per second of output, scaled by resolution, and
// charges separately for input media. newapi's OtherRatios are multiplicative,
// so ModelPrice is configured as the 480p per-second output price and the
// additive input-media cost is folded into a `media` multiplier.
//
// Official xAI pricing (per docs.x.ai, verified 2026-08-05):
//
//	grok-imagine-video      480p $0.05/s  720p $0.07/s
//	                        input: video $0.01/s, image $0.002/img
//	grok-imagine-video-1.5  480p $0.08/s  720p $0.14/s  1080p $0.25/s
//	                        input: image $0.01/img
const (
	// DefaultDurationSeconds mirrors xAI's default when the client omits duration.
	DefaultDurationSeconds = 6
	// MaxDurationSeconds is xAI's documented ceiling for a single generation.
	MaxDurationSeconds = 15
	// DefaultResolution is xAI's default output resolution.
	DefaultResolution = "480p"
)

// resolutionRatios maps a model to its resolution multipliers relative to the
// 480p base price configured as ModelPrice.
var resolutionRatios = map[string]map[string]float64{
	"grok-imagine-video": {
		"480p": 1.0,
		"720p": 1.4, // $0.07 / $0.05
	},
	"grok-imagine-video-1.5": {
		"480p":  1.0,
		"720p":  1.75,  // $0.14 / $0.08
		"1080p": 3.125, // $0.25 / $0.08
	},
}

// inputMediaPrices holds the per-unit input-media cost in USD for each model.
type inputMediaPrice struct {
	perImage        float64
	perVideoSeconds float64
}

var inputMediaPrices = map[string]inputMediaPrice{
	"grok-imagine-video":     {perImage: 0.002, perVideoSeconds: 0.01},
	"grok-imagine-video-1.5": {perImage: 0.01},
}

// normalizeModel strips any vendor prefix so channel-level model_mapping and
// "xai/" style names still resolve to a pricing entry.
func normalizeModel(modelName string) string {
	m := strings.ToLower(strings.TrimSpace(modelName))
	if idx := strings.LastIndex(m, "/"); idx >= 0 {
		m = m[idx+1:]
	}
	return m
}

// ResolveDuration returns the effective output duration in seconds.
// Priority: metadata["duration"] > req.Duration > req.Seconds > default.
// The result is capped: it is used directly as a billing multiplier and the
// metadata path bypasses standard request validation.
func ResolveDuration(metadata map[string]any, stdDuration int, stdSeconds string) int {
	pick := func(n int) int {
		if n <= 0 {
			return 0
		}
		if n > MaxDurationSeconds {
			return MaxDurationSeconds
		}
		return n
	}
	if metadata != nil {
		if v, ok := metadata["duration"]; ok {
			switch n := v.(type) {
			case float64:
				if d := pick(int(n)); d > 0 {
					return d
				}
			case int:
				if d := pick(n); d > 0 {
					return d
				}
			}
		}
	}
	if d := pick(stdDuration); d > 0 {
		return d
	}
	if n, err := strconv.Atoi(stdSeconds); err == nil {
		if d := pick(n); d > 0 {
			return d
		}
	}
	return DefaultDurationSeconds
}

// ResolveResolution returns the effective resolution label (lowercase).
// Priority: metadata["resolution"] > SizeToResolution(req.Size) > default.
func ResolveResolution(metadata map[string]any, stdSize string) string {
	if metadata != nil {
		if v, ok := metadata["resolution"]; ok {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.ToLower(strings.TrimSpace(s))
			}
		}
	}
	if stdSize != "" {
		return SizeToResolution(stdSize)
	}
	return DefaultResolution
}

// SizeToResolution converts a "WxH" size string to an xAI resolution label.
func SizeToResolution(size string) string {
	parts := strings.SplitN(strings.ToLower(size), "x", 2)
	if len(parts) != 2 {
		return DefaultResolution
	}
	w, _ := strconv.Atoi(parts[0])
	h, _ := strconv.Atoi(parts[1])
	maxDim := w
	if h > maxDim {
		maxDim = h
	}
	switch {
	case maxDim >= 1920:
		return "1080p"
	case maxDim >= 1280:
		return "720p"
	default:
		return DefaultResolution
	}
}

// SizeToAspectRatio converts a "WxH" size string to an aspect ratio label.
func SizeToAspectRatio(size string) string {
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

// ResolutionRatio returns the price multiplier for a resolution relative to the
// model's 480p base price. Unknown model/resolution falls back to 1.0 so an
// unrecognised value can never silently inflate the bill.
func ResolutionRatio(modelName, resolution string) float64 {
	table, ok := resolutionRatios[normalizeModel(modelName)]
	if !ok {
		return 1.0
	}
	if r, ok := table[strings.ToLower(resolution)]; ok {
		return r
	}
	return 1.0
}

// InputMediaCost returns the input-media charge in USD for the given counts.
func InputMediaCost(modelName string, imageCount int, inputVideoSeconds int) float64 {
	p, ok := inputMediaPrices[normalizeModel(modelName)]
	if !ok {
		return 0
	}
	cost := 0.0
	if imageCount > 0 {
		cost += float64(imageCount) * p.perImage
	}
	if inputVideoSeconds > 0 {
		cost += float64(inputVideoSeconds) * p.perVideoSeconds
	}
	return cost
}

// MediaRatio folds the additive input-media cost into a multiplier over the
// output charge, so the multiplicative OtherRatios pipeline still yields the
// exact total:
//
//	total = modelPrice * seconds * resRatio * mediaRatio
//	      = outputCost + inputMediaCost
//
// Returns 1.0 when there is no input media or when the output charge is zero
// (a zero base price would make the ratio meaningless).
func MediaRatio(modelPrice float64, seconds int, resRatio float64, inputCost float64) float64 {
	if inputCost <= 0 {
		return 1.0
	}
	outputCost := modelPrice * float64(seconds) * resRatio
	if outputCost <= 0 {
		return 1.0
	}
	return 1.0 + inputCost/outputCost
}

// BillingRatios builds the OtherRatios map handed to the pricing pipeline.
func BillingRatios(modelName string, modelPrice float64, seconds int, resolution string,
	imageCount int, inputVideoSeconds int) map[string]float64 {
	resRatio := ResolutionRatio(modelName, resolution)
	inputCost := InputMediaCost(modelName, imageCount, inputVideoSeconds)
	return map[string]float64{
		"seconds":    float64(seconds),
		"resolution": resRatio,
		"media":      MediaRatio(modelPrice, seconds, resRatio, inputCost),
	}
}

// CapDuration clamps a duration coming back from the upstream. Shared with the
// completion-time settlement path, which must never trust a remote value as an
// unbounded billing multiplier.
func CapDuration(seconds int) int {
	if seconds <= 0 {
		return 0
	}
	if seconds > relaycommon.MaxTaskDurationSeconds {
		return relaycommon.MaxTaskDurationSeconds
	}
	return seconds
}

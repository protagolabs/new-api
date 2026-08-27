package sora

import "strings"

// Resolution-tier names get expanded into explicit pixel dimensions before the
// request leaves us.
//
// Not every model on this adaptor is Sora. Nested sub-sites and OpenRouter
// expose grok video here, and xAI documents its tiers as 480p / 720p / 1080p --
// so callers naturally send `"size": "720p"`. OpenRouter rejects that outright
// with `Invalid size format: "720p". Expected "WIDTHxHEIGHT"`, which left
// grok-imagine-video-1.5 completely unusable: the artemis channel 404s the
// model and OpenRouter 400s the size, so both routes failed and the caller
// retried into the same wall.
//
// It was also undercharging. videoTierFromSize splits on "x" to find the tier,
// so a tier NAME parsed to nothing, the tier multiplier never applied, and a
// 720p render billed at the 480p base rate.
//
// Dimensions follow the ordinary meaning of the tier names -- "720p" is
// 1280x720, landscape 16:9 -- rather than this adaptor's Sora-era portrait
// default. A caller who wants portrait already has a way to say so: pass the
// pixel form (`"720x1280"`), which is left untouched.
var videoTierSizes = map[string]string{
	"480p":  "854x480",
	"720p":  "1280x720",
	"1080p": "1920x1080",
}

// normalizeVideoSize expands a tier name into "WIDTHxHEIGHT". Anything else --
// an explicit pixel size, an empty string, an unrecognised value -- is returned
// unchanged so the upstream applies its own validation and defaults.
func normalizeVideoSize(size string) string {
	if expanded, ok := videoTierSizes[strings.ToLower(strings.TrimSpace(size))]; ok {
		return expanded
	}
	return size
}

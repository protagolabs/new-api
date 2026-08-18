package ratio_setting

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/types"
)

// Per-model, per-tier multipliers for video generation.
//
// Video vendors price by output tier -- resolution, and sometimes whether the
// request carried a video input -- on top of a per-second or per-token base
// rate held in ModelPrice. Those multipliers used to live in Go maps keyed by
// the exact model generation, e.g.
//
//	if strings.Contains(modelName, "3.1") { ... }   // veo
//	"dreamina-seedance-2-0": {...}                  // seedance
//
// which fails in the worst possible direction. When the vendor ships the next
// generation, the name stops matching, every tier collapses to the neutral 1.0,
// and we undercharge silently: the request succeeds, the video is fine, and the
// invoice is quietly 17%-67% short. Veo 3.1 -> 3.2 would have done exactly that.
//
// Shape, stored under the `video_tier_ratio` option:
//
//	{
//	  "veo-3.1-fast-generate-preview": {"720p": 1.0, "1080p": 1.2, "4k": 3.0},
//	  "veo-3.2-*":                     {"720p": 1.0, "1080p": 1.2, "4k": 3.0},
//	  "dreamina-seedance-2-5*":        {"480P": 1.0, "480P+video": 0.598}
//	}
//
// A trailing `*` matches by prefix, exact names win over patterns, and among
// patterns the longest prefix wins -- same rules as group_model_ratio, so there
// is one matching semantics to learn rather than two.
//
// Tier keys are whatever the adaptor asks for; the compound "480P+video" form
// is how a second dimension is encoded without inventing nested maps.
var videoTierRatioMap = types.NewRWMap[string, map[string]float64]()

type VideoTierRatioSetting struct {
	VideoTierRatio *types.RWMap[string, map[string]float64] `json:"video_tier_ratio"`
}

var videoTierRatioSetting VideoTierRatioSetting

func init() {
	videoTierRatioSetting = VideoTierRatioSetting{VideoTierRatio: videoTierRatioMap}
	config.GlobalConfig.Register("video_tier_ratio_setting", &videoTierRatioSetting)
}

func GetVideoTierRatioSetting() *VideoTierRatioSetting {
	if videoTierRatioSetting.VideoTierRatio == nil {
		videoTierRatioSetting.VideoTierRatio = types.NewRWMap[string, map[string]float64]()
	}
	return &videoTierRatioSetting
}

// GetVideoTierRatio resolves the multiplier for a (model, tier) pair.
//
// Returns false when nothing matches, which means "I have no opinion" -- the
// caller falls back to its built-in table. It must never be read as 1.0 by the
// caller without that fallback, or a typo'd config key would silently zero out
// a resolution surcharge.
func GetVideoTierRatio(modelName, tier string) (float64, bool) {
	if modelName == "" || tier == "" {
		return 0, false
	}
	tiers, ok := videoTierRatioMap.Get(modelName)
	if ok {
		if r, found := tiers[tier]; found {
			return r, true
		}
	}

	// Longest matching prefix, so a specific override beats a family-wide rule
	// regardless of map iteration order.
	best, bestLen, found := 0.0, -1, false
	for pattern, rules := range videoTierRatioMap.ReadAll() {
		if !strings.HasSuffix(pattern, "*") {
			continue
		}
		r, has := rules[tier]
		if !has {
			continue
		}
		prefix := strings.TrimSuffix(pattern, "*")
		if strings.HasPrefix(modelName, prefix) && len(prefix) > bestLen {
			best, bestLen, found = r, len(prefix), true
		}
	}
	return best, found
}

// HasVideoTierConfig reports whether any rule exists for a model, letting a
// caller tell "configured as 1.0" apart from "not configured".
func HasVideoTierConfig(modelName string) bool {
	if modelName == "" {
		return false
	}
	if tiers, ok := videoTierRatioMap.Get(modelName); ok && len(tiers) > 0 {
		return true
	}
	for pattern, rules := range videoTierRatioMap.ReadAll() {
		if !strings.HasSuffix(pattern, "*") || len(rules) == 0 {
			continue
		}
		if strings.HasPrefix(modelName, strings.TrimSuffix(pattern, "*")) {
			return true
		}
	}
	return false
}

// VideoTierRatioCopy returns a snapshot for the admin API / UI.
func VideoTierRatioCopy() map[string]map[string]float64 {
	return videoTierRatioMap.ReadAll()
}

// WarnUnpricedVideoTier records that a model in a managed family asked for a
// tier nobody has priced.
//
// This is the whole point of the exercise. The old code answered 1.0 and moved
// on, so a new model generation undercharged every request until someone
// happened to audit the invoices. Now it says so, once per occurrence, in a
// form that is greppable and that the log-based alerting already watches.
func WarnUnpricedVideoTier(modelName, tier string) {
	common.SysError("video tier not priced: model=" + modelName + " tier=" + tier +
		" -- billing fell back to 1.0x, which undercharges if this tier costs more." +
		" Add it to the video_tier_ratio option.")
}

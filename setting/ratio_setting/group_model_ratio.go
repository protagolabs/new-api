package ratio_setting

import (
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/types"
)

// Per-group, per-model discount multipliers.
//
// The existing knobs cannot express this: ModelRatio is global (same for every
// user), GroupRatio applies one factor to every model a group uses, and
// GroupGroupRatio crosses user group with *channel* group, not with the model.
// billing_expr cannot help either -- its variables are token dimensions plus
// request headers/body, with no notion of the caller.
//
// So a customer who should get, say, 55% off one model family and 95% off
// another had to be handed one API key per discount tier. This adds the missing
// (group, model) lookup, keeping a single key and the original model names.
//
// Shape, stored under the `group_model_ratio` option:
//
//	{
//	  "vip-0806": {
//	    "MiniMax-H3":        0.95,
//	    "happyhorse-1.1-*":  0.55,
//	    "grok-imagine-video*": 0.65
//	  }
//	}
//
// A trailing `*` matches by prefix. An exact model name always wins over a
// pattern, and among patterns the longest prefix wins, so a specific override
// can be layered on top of a family-wide rule.
var groupModelRatioMap = types.NewRWMap[string, map[string]float64]()

type GroupModelRatioSetting struct {
	GroupModelRatio *types.RWMap[string, map[string]float64] `json:"group_model_ratio"`
}

var groupModelRatioSetting GroupModelRatioSetting

func init() {
	groupModelRatioSetting = GroupModelRatioSetting{GroupModelRatio: groupModelRatioMap}
	config.GlobalConfig.Register("group_model_ratio_setting", &groupModelRatioSetting)
}

func GetGroupModelRatioSetting() *GroupModelRatioSetting {
	if groupModelRatioSetting.GroupModelRatio == nil {
		groupModelRatioSetting.GroupModelRatio = types.NewRWMap[string, map[string]float64]()
	}
	return &groupModelRatioSetting
}

// UserRuleKey is the scope key for a single user's rules: `user:<id>`.
//
// Scoping by group alone turned out to be unusable for a one-customer discount:
// a group only routes to channels that list it explicitly, so moving the
// customer into a new group cut him off from the 25 `default` channels (72
// models) and every channel added later would have to remember to include the
// new group. Scoping by user leaves routing untouched.
func UserRuleKey(userID int) string {
	if userID <= 0 {
		return ""
	}
	return "user:" + strconv.Itoa(userID)
}

// GetModelRatioForUser resolves the discount for a request, preferring a rule
// scoped to this specific user over one scoped to their group.
//
// Returns false when neither scope has a matching rule, in which case the
// caller must fall back to the normal group ratio.
func GetModelRatioForUser(userID int, group, modelName string) (float64, bool) {
	if r, ok := GetGroupModelRatio(UserRuleKey(userID), modelName); ok {
		return r, true
	}
	return GetGroupModelRatio(group, modelName)
}

// GetGroupModelRatio resolves the discount for a (scope, model) pair, where
// scope is a user group or a `user:<id>` key.
//
// Returns false when the scope has no entry or no rule matches the model, in
// which case the caller must fall back to the normal group ratio -- a missing
// rule must never be read as "free".
func GetGroupModelRatio(group, modelName string) (float64, bool) {
	if group == "" || modelName == "" {
		return 0, false
	}
	rules, ok := groupModelRatioMap.Get(group)
	if !ok || len(rules) == 0 {
		return 0, false
	}

	// Exact match wins outright.
	if r, ok := rules[modelName]; ok {
		return r, true
	}

	// Otherwise the longest matching prefix, so a narrower pattern beats a
	// broader one regardless of map iteration order.
	best, bestLen, found := 0.0, -1, false
	for pattern, r := range rules {
		if !strings.HasSuffix(pattern, "*") {
			continue
		}
		prefix := strings.TrimSuffix(pattern, "*")
		if prefix == "" {
			// A bare "*" is a group-wide default; keep it as the weakest match.
			if bestLen < 0 {
				best, bestLen, found = r, 0, true
			}
			continue
		}
		if strings.HasPrefix(modelName, prefix) && len(prefix) > bestLen {
			best, bestLen, found = r, len(prefix), true
		}
	}
	return best, found
}

// GroupModelRatioCopy returns a snapshot for the admin API / UI.
func GroupModelRatioCopy() map[string]map[string]float64 {
	return groupModelRatioMap.ReadAll()
}

// ContainsGroupModelRules reports whether a group has any per-model rule,
// letting callers skip the lookup entirely for the common case.
func ContainsGroupModelRules(group string) bool {
	rules, ok := groupModelRatioMap.Get(group)
	return ok && len(rules) > 0
}

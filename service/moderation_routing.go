package service

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// moderationScanKeys are the top-level request body keys whose text is scanned
// for moderation-routing words, covering the OpenAI chat (`messages`), Claude
// messages (`messages` + `system`), Gemini (`contents` + `system_instruction`),
// and completions/Responses (`prompt` / `input`) request shapes.
var moderationScanKeys = []string{"messages", "system", "contents", "system_instruction", "prompt", "input"}

// PromptMatchesModerationSkipWords reports whether the request prompt contains
// any configured moderation-routing word. When true, channel selection should
// exclude channels flagged with dto.ChannelSettings.PerformsUpstreamModeration.
//
// It returns false cheaply when the feature is disabled or the word set is empty
// (no body read). Otherwise it runs Aho-Corasick (cached by dict hash via
// getOrBuildAC) over the lowercased raw JSON of the relevant body keys. Scanning
// the raw blob (keys/roles included) is acceptable for a deliberately chosen
// routing word set and is format-agnostic across the supported request shapes.
func PromptMatchesModerationSkipWords(c *gin.Context) bool {
	s := operation_setting.GetModerationRoutingSetting()
	if s == nil || !s.Enabled || len(s.Words) == 0 {
		return false
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return false
	}
	body, err := storage.Bytes()
	if err != nil || len(body) == 0 {
		return false
	}
	var sb strings.Builder
	for _, key := range moderationScanKeys {
		if res := gjson.GetBytes(body, key); res.Exists() {
			sb.WriteString(res.Raw)
			sb.WriteByte('\n')
		}
	}
	if sb.Len() == 0 {
		return false
	}
	contains, _ := AcSearch(strings.ToLower(sb.String()), s.Words, true)
	return contains
}

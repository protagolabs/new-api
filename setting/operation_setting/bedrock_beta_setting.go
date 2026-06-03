package operation_setting

import (
	"strings"

	"github.com/QuantumNous/new-api/setting/config"
)

// BedrockBetaSetting holds the whitelist of anthropic-beta flags that Bedrock
// backends accept. Channels with dto.ChannelOtherSettings.FilterAnthropicBeta
// set will forward only the flags present here, dropping the rest so that
// unknown flags (e.g. prompt-caching-scope-*, redact-thinking-*) no longer trip
// Bedrock's "ValidationException: invalid beta flag".
//
// The list is whatever Bedrock currently supports for the deployed models and
// regions; it is operator-maintained and hot-reloadable (dotted option key
// bedrock_beta_setting.whitelist) so AWS adding/removing a flag needs no rebuild.
type BedrockBetaSetting struct {
	Whitelist []string `json:"whitelist"`
}

// Default whitelist seeded with Bedrock-documented beta flags (2026-06). Adjust
// via the option as AWS rolls versions forward.
var bedrockBetaSetting = BedrockBetaSetting{
	Whitelist: []string{
		"computer-use-2025-01-24",
		"computer-use-2025-11-24",
		"token-efficient-tools-2025-02-19",
		"interleaved-thinking-2025-05-14",
		"fine-grained-tool-streaming-2025-05-14",
		"context-management-2025-06-27",
		"tool-search-tool-2025-10-19",
	},
}

func init() {
	config.GlobalConfig.Register("bedrock_beta_setting", &bedrockBetaSetting)
}

func GetBedrockBetaSetting() *BedrockBetaSetting {
	return &bedrockBetaSetting
}

// FilterAnthropicBetaByWhitelist takes the raw anthropic-beta header value
// (comma-separated flags) and returns it with only the whitelisted flags kept,
// preserving original order. Returns "" if nothing survives, in which case the
// caller should omit the header entirely.
func FilterAnthropicBetaByWhitelist(headerValue string) string {
	if headerValue == "" {
		return ""
	}
	allowed := make(map[string]struct{}, len(bedrockBetaSetting.Whitelist))
	for _, w := range bedrockBetaSetting.Whitelist {
		if w = strings.TrimSpace(w); w != "" {
			allowed[w] = struct{}{}
		}
	}
	kept := make([]string, 0, len(allowed))
	for _, flag := range strings.Split(headerValue, ",") {
		f := strings.TrimSpace(flag)
		if f == "" {
			continue
		}
		if _, ok := allowed[f]; ok {
			kept = append(kept, f)
		}
	}
	return strings.Join(kept, ",")
}

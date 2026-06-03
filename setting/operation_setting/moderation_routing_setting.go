package operation_setting

import "github.com/QuantumNous/new-api/setting/config"

// ModerationRoutingSetting routes prompts containing any configured word away
// from channels flagged as performing upstream content moderation
// (dto.ChannelSettings.PerformsUpstreamModeration). Prompts that match no word
// use normal scheduling across all channels.
//
// Matching is case-insensitive substring (Aho-Corasick); choose words that will
// not accidentally match as substrings of unrelated text. This is intentionally
// separate from setting.SensitiveWords, which BLOCKS requests rather than routes
// them.
type ModerationRoutingSetting struct {
	Enabled bool     `json:"enabled"`
	Words   []string `json:"words"`
}

// 默认配置：关闭 + 空词表，未配置前为完全 no-op。
var moderationRoutingSetting = ModerationRoutingSetting{
	Enabled: false,
	Words:   []string{},
}

func init() {
	config.GlobalConfig.Register("moderation_routing_setting", &moderationRoutingSetting)
}

func GetModerationRoutingSetting() *ModerationRoutingSetting {
	return &moderationRoutingSetting
}

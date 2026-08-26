package operation_setting

import "github.com/QuantumNous/new-api/setting/config"

// AssetSetting 参考素材相关配置
type AssetSetting struct {
	MaxUserAssets int `json:"max_user_assets"` // 每用户最大素材数量
}

// The account-wide ceiling upstream is 50, and it is SHARED with the real-person
// portrait library -- so this per-user cap is what decides how many customers
// fit. At 10 that is roughly 5 customers. Expect to retune it, which is why it
// lives in the option table (hot-editable) rather than as a constant.
var assetSetting = AssetSetting{
	MaxUserAssets: 10,
}

func init() {
	config.GlobalConfig.Register("asset_setting", &assetSetting)
}

// GetAssetSetting 获取素材配置
func GetAssetSetting() *AssetSetting {
	return &assetSetting
}

// GetMaxUserAssets 获取每用户最大素材数量
func GetMaxUserAssets() int {
	return GetAssetSetting().MaxUserAssets
}

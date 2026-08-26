package model

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// Asset maps a customer-facing reference-asset id onto the vendor's own id.
//
// The indirection is the whole point. BytePlus asset ids are account-scoped:
// every asset we register lives in one shared account, so a customer holding
// another customer's `asset-2026...` id could reference their portrait and the
// vendor would happily serve it -- there is no per-customer boundary upstream to
// lean on. So customers only ever see AssetId, and the mapping to UpstreamId is
// resolved (and ownership-checked) at relay time.
//
// UpstreamId therefore MUST stay `json:"-"`. Leaking it anywhere -- a list
// response, an error message, a log line handed to a customer -- collapses the
// isolation back to nothing.
type Asset struct {
	Id          int            `json:"-"`
	AssetId     string         `json:"id" gorm:"type:varchar(191);uniqueIndex"`
	UserId      int            `json:"-" gorm:"index"`
	UpstreamId  string         `json:"-" gorm:"type:varchar(191);index"`
	GroupId     string         `json:"-" gorm:"type:varchar(191)"`
	Name        string         `json:"name" gorm:"type:varchar(255)"`
	AssetType   string         `json:"type" gorm:"type:varchar(16)"`
	Status      string         `json:"status" gorm:"type:varchar(16)"`
	CreatedTime int64          `json:"created_at" gorm:"bigint"`
	UpdatedTime int64          `json:"-" gorm:"bigint"`
	DeletedAt   gorm.DeletedAt `json:"-" gorm:"index"`
}

// Asset lifecycle states, mirroring the vendor's own vocabulary.
const (
	AssetStatusProcessing = "Processing"
	AssetStatusActive     = "Active"
	AssetStatusFailed     = "Failed"
)

// AssetIDPrefix marks ids we minted. Anything without it is passed through to
// the vendor untouched, so a customer with their own BytePlus account can still
// use their own asset ids directly.
const AssetIDPrefix = "asset_"

// NewAssetID mints a customer-facing id. Deliberately unrelated to the vendor
// id so that neither can be guessed from the other.
func NewAssetID() string {
	return AssetIDPrefix + common.GetUUID()
}

// IsManagedAssetID reports whether an id is one of ours.
func IsManagedAssetID(id string) bool {
	return strings.HasPrefix(id, AssetIDPrefix)
}

func (asset *Asset) Insert() error {
	now := common.GetTimestamp()
	asset.CreatedTime = now
	asset.UpdatedTime = now
	return DB.Create(asset).Error
}

// UpdateStatus records the latest vendor-reported state. Nothing else about an
// asset is mutable -- name and type are fixed at registration.
func (asset *Asset) UpdateStatus(status string) error {
	asset.Status = status
	asset.UpdatedTime = common.GetTimestamp()
	return DB.Model(asset).Select("status", "updated_time").Updates(asset).Error
}

// GetAssetByAssetID looks up one asset owned by userId.
//
// userId is not optional: without it this becomes the cross-customer read the
// whole indirection exists to prevent. Same reasoning as GetTokenByIds.
func GetAssetByAssetID(assetId string, userId int) (*Asset, error) {
	if assetId == "" || userId == 0 {
		return nil, errors.New("assetId or userId is empty")
	}
	var asset Asset
	err := DB.Where("asset_id = ? and user_id = ?", assetId, userId).First(&asset).Error
	if err != nil {
		return nil, err
	}
	return &asset, nil
}

// GetAssetsByAssetIDs resolves several ids at once, returning only those owned
// by userId. A caller that gets back fewer rows than it asked for knows some id
// was unknown or belongs to someone else, without needing to tell the two apart
// -- and it must not tell the customer either, or the endpoint becomes an
// existence oracle for other people's assets.
func GetAssetsByAssetIDs(assetIds []string, userId int) (map[string]*Asset, error) {
	found := make(map[string]*Asset, len(assetIds))
	if len(assetIds) == 0 || userId == 0 {
		return found, nil
	}
	var assets []*Asset
	err := DB.Where("asset_id in ? and user_id = ?", assetIds, userId).Find(&assets).Error
	if err != nil {
		return nil, err
	}
	for _, asset := range assets {
		found[asset.AssetId] = asset
	}
	return found, nil
}

func GetAllUserAssets(userId int, startIdx int, num int) ([]*Asset, error) {
	var assets []*Asset
	err := DB.Where("user_id = ?", userId).Order("id desc").Limit(num).Offset(startIdx).Find(&assets).Error
	return assets, err
}

func CountUserAssets(userId int) (int64, error) {
	var total int64
	err := DB.Model(&Asset{}).Where("user_id = ?", userId).Count(&total).Error
	return total, err
}

// DeleteAssetByAssetID soft-deletes one asset owned by userId. Returns the row
// so the caller can delete it upstream too.
func DeleteAssetByAssetID(assetId string, userId int) (*Asset, error) {
	asset, err := GetAssetByAssetID(assetId, userId)
	if err != nil {
		return nil, err
	}
	if err := DB.Delete(asset).Error; err != nil {
		return nil, err
	}
	return asset, nil
}

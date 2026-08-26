package controller

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Reference asset endpoints, backing Seedance's `asset://` inputs.
//
// These live on /v1 and are called with the same sk- keys as the relay
// endpoints, so they answer with real HTTP status codes and the OpenAI error
// envelope -- not the dashboard's HTTP-200-plus-success:false convention. A
// client that gets 403 from /v1/chat/completions should get 403 here too.

type assetCreateRequest struct {
	URL  string `json:"url"`
	Type string `json:"type"`
	Name string `json:"name"`
}

func assetError(c *gin.Context, statusCode int, message string) {
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"message": common.MessageWithRequestId(message, c.GetString(common.RequestIdKey)),
			"type":    "new_api_error",
			"code":    "",
		},
	})
}

// requireAssetService rejects early when the deployment has no BytePlus
// credentials, so the failure names the actual problem instead of surfacing as
// a signing error from deep inside the client.
func requireAssetService(c *gin.Context) bool {
	if !service.BytePlusAssetConfigured() {
		assetError(c, http.StatusServiceUnavailable,
			"Reference assets are not enabled on this deployment.")
		return false
	}
	return true
}

// CreateAsset registers a publicly reachable URL as a reference asset.
func CreateAsset(c *gin.Context) {
	if !requireAssetService(c) {
		return
	}
	userId := c.GetInt("id")

	var req assetCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		assetError(c, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		assetError(c, http.StatusBadRequest, "url is required.")
		return
	}
	// The vendor fetches this itself, so anything it cannot dereference -- a
	// data URI, a presigned link that has expired, a host behind our VPN --
	// fails there with a vague download error. Catch the obvious case here.
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		assetError(c, http.StatusBadRequest,
			"url must be a publicly reachable http:// or https:// address. "+
				"Base64 data URIs are not supported.")
		return
	}

	assetType := req.Type
	if assetType == "" {
		assetType = "Image"
	}
	switch assetType {
	case "Image", "Video", "Audio":
	default:
		assetError(c, http.StatusBadRequest, "type must be one of Image, Video, Audio.")
		return
	}

	// Per-user cap first: it is the limit a customer can act on themselves.
	maxAssets := operation_setting.GetMaxUserAssets()
	count, err := model.CountUserAssets(userId)
	if err != nil {
		assetError(c, http.StatusInternalServerError, "Failed to count assets: "+err.Error())
		return
	}
	if int(count) >= maxAssets {
		assetError(c, http.StatusForbidden, fmt.Sprintf(
			"You have reached the maximum of %d reference assets. "+
				"Delete an unused asset with DELETE /v1/assets/{id} before uploading a new one.",
			maxAssets))
		return
	}

	// Account-wide capacity second. This one the customer cannot fix, so it is
	// phrased as an operational limit and logged for us.
	if quota, qErr := service.BytePlusGetAssetQuota(); qErr == nil && quota.Max > 0 && quota.Used >= quota.Max {
		common.SysError(fmt.Sprintf(
			"byteplus asset library is full (%d/%d) -- user %d could not register a new asset",
			quota.Used, quota.Max, userId))
		assetError(c, http.StatusServiceUnavailable, fmt.Sprintf(
			"The shared reference asset library is full (%d/%d). "+
				"This is an account-wide limit; please contact support.",
			quota.Used, quota.Max))
		return
	}

	groupID := service.BytePlusAssetGroupID()
	upstreamID, err := service.BytePlusCreateAsset(groupID, req.URL, assetType, req.Name)
	if err != nil {
		// Vendor-side rejections are the customer's to fix (bad URL, unsupported
		// format), so pass the upstream text through rather than flattening it.
		assetError(c, http.StatusBadGateway, "Upstream rejected the asset: "+err.Error())
		return
	}

	asset := &model.Asset{
		AssetId:    model.NewAssetID(),
		UserId:     userId,
		UpstreamId: upstreamID,
		GroupId:    groupID,
		Name:       req.Name,
		AssetType:  assetType,
		Status:     model.AssetStatusProcessing,
	}
	if err := asset.Insert(); err != nil {
		// The upstream asset exists but we failed to record it. Drop it rather
		// than leaking an orphan that still counts against the shared quota.
		if delErr := service.BytePlusDeleteAsset(upstreamID); delErr != nil {
			common.SysError(fmt.Sprintf(
				"orphaned byteplus asset %s: insert failed (%v) and cleanup failed (%v)",
				upstreamID, err, delErr))
		}
		assetError(c, http.StatusInternalServerError, "Failed to store asset: "+err.Error())
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf(
		"user %d registered reference asset %s", userId, asset.AssetId))
	c.JSON(http.StatusCreated, asset)
}

// GetAsset returns one asset, refreshing its status from upstream.
func GetAsset(c *gin.Context) {
	if !requireAssetService(c) {
		return
	}
	userId := c.GetInt("id")
	assetId := c.Param("id")

	asset, err := model.GetAssetByAssetID(assetId, userId)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			assetError(c, http.StatusNotFound, "Asset not found.")
			return
		}
		assetError(c, http.StatusInternalServerError, err.Error())
		return
	}

	// Registration is asynchronous and terminal states never change again, so
	// only poll upstream while the asset is still settling.
	if asset.Status == model.AssetStatusProcessing {
		if status, sErr := service.BytePlusGetAssetStatus(asset.UpstreamId); sErr == nil && status != "" {
			if status != asset.Status {
				if uErr := asset.UpdateStatus(status); uErr != nil {
					common.SysError("failed to persist asset status: " + uErr.Error())
				}
			}
			asset.Status = status
		}
	}
	c.JSON(http.StatusOK, asset)
}

// ListAssets returns the caller's own assets.
func ListAssets(c *gin.Context) {
	if !requireAssetService(c) {
		return
	}
	userId := c.GetInt("id")
	pageInfo := common.GetPageQuery(c)

	assets, err := model.GetAllUserAssets(userId, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		assetError(c, http.StatusInternalServerError, err.Error())
		return
	}
	if assets == nil {
		assets = make([]*model.Asset, 0)
	}
	total, _ := model.CountUserAssets(userId)
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   assets,
		"total":  total,
	})
}

// DeleteAsset removes an asset here and upstream.
func DeleteAsset(c *gin.Context) {
	if !requireAssetService(c) {
		return
	}
	userId := c.GetInt("id")
	assetId := c.Param("id")

	asset, err := model.DeleteAssetByAssetID(assetId, userId)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			assetError(c, http.StatusNotFound, "Asset not found.")
			return
		}
		assetError(c, http.StatusInternalServerError, err.Error())
		return
	}

	// Local delete already succeeded. An upstream failure here only wastes a
	// slot in the shared quota -- it must not fail the customer's request, but
	// we do need to know about it.
	if err := service.BytePlusDeleteAsset(asset.UpstreamId); err != nil {
		common.SysError(fmt.Sprintf(
			"asset %s deleted locally but upstream delete failed: %v", assetId, err))
	}

	c.JSON(http.StatusOK, gin.H{
		"id":      assetId,
		"object":  "asset.deleted",
		"deleted": true,
	})
}

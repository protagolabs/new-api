package service

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// BytePlus private asset library client.
//
// Seedance refuses reference images containing real human faces outright -- a
// photoreal portrait is rejected at submission with an opaque
// `PrivacyInformation` error. The sanctioned way around it is to register the
// asset first and then reference it as `asset://<id>`, which skips input
// moderation entirely.
//
// These are control-plane actions and need AK/SK request signing: an ARK API
// key does not authenticate them, and arkcli only exposes GetAssetQuota. Hence
// the signing implementation here rather than reusing an existing client.
//
// Docs: https://docs.byteplus.com/en/docs/ModelArk/2333565
const (
	bytePlusAssetHost    = "ark.ap-southeast-1.byteplusapi.com"
	bytePlusAssetRegion  = "ap-southeast-1"
	bytePlusAssetService = "ark"
	bytePlusAssetVersion = "2024-01-01"
)

// Credentials are read from the environment, not from the option table: every
// DB-backed setting here is plaintext and at least one admin endpoint hands
// values back, which is the wrong home for an account-wide key pair. This
// matches where the codebase already keeps real secrets (SQL_DSN, CRYPTO_SECRET).
func bytePlusAssetCredentials() (accessKey, secretKey, groupID string) {
	return common.GetEnvOrDefaultString("BYTEPLUS_ACCESS_KEY", ""),
		common.GetEnvOrDefaultString("BYTEPLUS_SECRET_KEY", ""),
		common.GetEnvOrDefaultString("BYTEPLUS_ASSET_GROUP_ID", "")
}

// BytePlusAssetConfigured reports whether asset endpoints can serve requests at
// all. Everything else in the gateway keeps working when this is false; only
// the asset routes degrade, and they say so explicitly rather than failing
// deep inside a signing routine.
func BytePlusAssetConfigured() bool {
	ak, sk, group := bytePlusAssetCredentials()
	return ak != "" && sk != "" && group != ""
}

// BytePlusAssetGroupID is the asset group new uploads land in.
func BytePlusAssetGroupID() string {
	_, _, group := bytePlusAssetCredentials()
	return group
}

// BytePlusAssetError carries the vendor's own error code and message so callers
// can distinguish "the URL you gave me is unreachable" from "the account is out
// of quota" without string-matching a wrapped error.
type BytePlusAssetError struct {
	Code    string
	Message string
}

func (e *BytePlusAssetError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

type bytePlusAssetResponse struct {
	ResponseMetadata struct {
		RequestId string `json:"RequestId"`
		Error     *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	} `json:"ResponseMetadata"`
	Result map[string]any `json:"Result"`
}

func hmacSHA256(key []byte, content string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(content))
	return mac.Sum(nil)
}

// signingKey derives the per-day request signing key.
func signingKey(secretKey, dateStamp string) []byte {
	k := hmacSHA256([]byte(secretKey), dateStamp)
	k = hmacSHA256(k, bytePlusAssetRegion)
	k = hmacSHA256(k, bytePlusAssetService)
	return hmacSHA256(k, "request")
}

// callBytePlusAsset issues one signed control-plane action.
func callBytePlusAsset(action string, payload map[string]any) (map[string]any, error) {
	accessKey, secretKey, _ := bytePlusAssetCredentials()
	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("byteplus asset credentials are not configured")
	}

	body, err := common.Marshal(payload)
	if err != nil {
		return nil, err
	}
	bodyHash := sha256.Sum256(body)
	bodyHashHex := hex.EncodeToString(bodyHash[:])

	now := time.Now().UTC()
	xDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	query := url.Values{}
	query.Set("Action", action)
	query.Set("Version", bytePlusAssetVersion)
	canonicalQuery := query.Encode()

	const signedHeaders = "content-type;host;x-content-sha256;x-date"
	canonicalRequest := strings.Join([]string{
		http.MethodPost,
		"/",
		canonicalQuery,
		"content-type:application/json",
		"host:" + bytePlusAssetHost,
		"x-content-sha256:" + bodyHashHex,
		"x-date:" + xDate,
		"",
		signedHeaders,
		bodyHashHex,
	}, "\n")

	canonicalHash := sha256.Sum256([]byte(canonicalRequest))
	scope := fmt.Sprintf("%s/%s/%s/request", dateStamp, bytePlusAssetRegion, bytePlusAssetService)
	stringToSign := strings.Join([]string{
		"HMAC-SHA256",
		xDate,
		scope,
		hex.EncodeToString(canonicalHash[:]),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(signingKey(secretKey, dateStamp), stringToSign))

	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("https://%s/?%s", bytePlusAssetHost, canonicalQuery), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Date", xDate)
	req.Header.Set("X-Content-Sha256", bodyHashHex)
	req.Header.Set("Authorization", fmt.Sprintf(
		"HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, scope, signedHeaders, signature))

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var parsed bytePlusAssetResponse
	if err := common.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("byteplus asset %s: unparseable response (http %d)", action, resp.StatusCode)
	}
	// The vendor returns HTTP 200 with an Error block rather than a 4xx, so the
	// status code alone never tells you whether the call worked.
	if parsed.ResponseMetadata.Error != nil {
		return nil, &BytePlusAssetError{
			Code:    parsed.ResponseMetadata.Error.Code,
			Message: parsed.ResponseMetadata.Error.Message,
		}
	}
	return parsed.Result, nil
}

// BytePlusCreateAsset registers a publicly reachable URL as a reference asset.
//
// The vendor downloads the URL itself, so it must be reachable from their
// network and must be real HTTP(S) -- base64 data URIs are rejected with
// `URL must be a valid HTTP or HTTPS URL`. Registration is asynchronous: the
// returned asset is not usable until GetAsset reports Active.
func BytePlusCreateAsset(groupID, assetURL, assetType, name string) (string, error) {
	payload := map[string]any{
		"GroupId":     groupID,
		"URL":         assetURL,
		"AssetType":   assetType,
		"ProjectName": "default",
	}
	if name != "" {
		payload["Name"] = name
	}
	result, err := callBytePlusAsset("CreateAsset", payload)
	if err != nil {
		return "", err
	}
	id, _ := result["Id"].(string)
	if id == "" {
		return "", fmt.Errorf("byteplus asset: CreateAsset returned no id")
	}
	return id, nil
}

// BytePlusGetAssetStatus returns the vendor-reported state of one asset.
// Status is also the moderation verdict: assets that resemble a real person
// settle on Failed rather than Active.
func BytePlusGetAssetStatus(upstreamID string) (string, error) {
	result, err := callBytePlusAsset("GetAsset", map[string]any{
		"Id":          upstreamID,
		"ProjectName": "default",
	})
	if err != nil {
		return "", err
	}
	status, _ := result["Status"].(string)
	return status, nil
}

// BytePlusDeleteAsset removes an asset upstream. Deletion is irreversible.
func BytePlusDeleteAsset(upstreamID string) error {
	_, err := callBytePlusAsset("DeleteAsset", map[string]any{
		"Id":          upstreamID,
		"ProjectName": "default",
	})
	return err
}

// BytePlusAssetQuota is the account-wide capacity, shared between the virtual
// portrait library and the real-person library.
type BytePlusAssetQuota struct {
	Used int
	Max  int
}

// BytePlusGetAssetQuota reads remaining account capacity. Used to turn an
// account-level exhaustion into an explicit message before we bother the
// vendor with an upload that cannot succeed.
func BytePlusGetAssetQuota() (*BytePlusAssetQuota, error) {
	result, err := callBytePlusAsset("GetAssetQuota", map[string]any{})
	if err != nil {
		return nil, err
	}
	quota, ok := result["quota"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("byteplus asset: GetAssetQuota returned no quota block")
	}
	toInt := func(v any) int {
		if f, ok := v.(float64); ok {
			return int(f)
		}
		return 0
	}
	return &BytePlusAssetQuota{
		Used: toInt(quota["used_assets"]),
		Max:  toInt(quota["max_assets"]),
	}, nil
}

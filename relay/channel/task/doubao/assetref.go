package doubao

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
)

// Reference-asset id translation.
//
// Seedance reference assets are addressed as `asset://<id>`, and BytePlus asset
// ids are ACCOUNT-scoped: every customer's asset lives in the same account, so
// an id leaked from one customer would let another reference their portrait and
// the vendor would serve it. There is no upstream boundary to enforce.
//
// So customers only ever hold ids we minted (`asset_<uuid>`), and this file
// resolves them to the real vendor ids at relay time, refusing anything the
// caller does not own.
//
// Ids without our prefix are passed through untouched -- a customer with their
// own BytePlus account may legitimately reference their own `asset-2026...`
// directly, and that is between them and the vendor.
const assetURIScheme = "asset://"

// contextKeyResolvedAssets holds the id mapping between validation and body
// construction. The two run in different phases: ownership is checked before
// billing (so a foreign id is a clean 400 with nothing to refund), while the
// rewrite happens later, where the request struct is a mutable copy.
const contextKeyResolvedAssets = "resolved_asset_refs"

// assetTaskError builds a local (non-upstream) task error. Marked LocalError so
// the relay layer does not attribute the failure to the channel and penalise it.
func assetTaskError(err error, code string, statusCode int) *taskdto.TaskError {
	return &taskdto.TaskError{
		Code:       code,
		Message:    err.Error(),
		StatusCode: statusCode,
		LocalError: true,
		Error:      err,
	}
}

// collectManagedAssetIDs finds every asset id of ours referenced by a request.
func collectManagedAssetIDs(req *relaycommon.TaskSubmitReq) []string {
	seen := make(map[string]struct{})
	var ids []string
	add := func(raw string) {
		id, ok := managedAssetID(raw)
		if !ok {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	add(req.Image)
	add(req.InputReference)
	for _, img := range req.Images {
		add(img)
	}
	walkMetadataURLs(req.Metadata, add)
	return ids
}

// managedAssetID extracts our asset id from an `asset://asset_x` reference.
func managedAssetID(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, assetURIScheme) {
		return "", false
	}
	id := strings.TrimPrefix(trimmed, assetURIScheme)
	if !model.IsManagedAssetID(id) {
		return "", false
	}
	return id, true
}

// walkMetadataURLs visits every string that could carry an asset reference.
//
// metadata is passed through to the vendor verbatim rather than parsed into a
// fixed schema, so this walks the decoded JSON generically: a reference may sit
// in content[].image_url.url today and somewhere else after the next vendor
// revision, and a structural walk keeps working either way.
func walkMetadataURLs(node any, visit func(string)) {
	switch v := node.(type) {
	case map[string]any:
		for key, child := range v {
			if s, ok := child.(string); ok {
				if key == "url" || key == "image" || key == "video" || key == "audio" {
					visit(s)
				}
				continue
			}
			walkMetadataURLs(child, visit)
		}
	case []any:
		for _, child := range v {
			walkMetadataURLs(child, visit)
		}
	}
}

// rewriteMetadataURLs applies the resolved mapping in place.
func rewriteMetadataURLs(node any, mapping map[string]string) {
	switch v := node.(type) {
	case map[string]any:
		for key, child := range v {
			if s, ok := child.(string); ok {
				if rewritten, changed := rewriteAssetURI(s, mapping); changed {
					v[key] = rewritten
				}
				_ = key
				continue
			}
			rewriteMetadataURLs(child, mapping)
		}
	case []any:
		for _, child := range v {
			rewriteMetadataURLs(child, mapping)
		}
	}
}

// rewriteAssetURI swaps our id for the vendor id, leaving everything else alone.
func rewriteAssetURI(raw string, mapping map[string]string) (string, bool) {
	id, ok := managedAssetID(raw)
	if !ok {
		return raw, false
	}
	upstream, ok := mapping[id]
	if !ok {
		return raw, false
	}
	return assetURIScheme + upstream, true
}

// resolveAssetReferences checks that every referenced asset belongs to the
// caller and stashes the mapping for the rewrite phase.
//
// Called during validation, i.e. before the pre-charge in RelayTaskSubmit, so a
// bad reference costs the customer nothing and returns a 400 rather than a 500
// from deep inside body construction.
func resolveAssetReferences(c *gin.Context, info *relaycommon.RelayInfo) *taskdto.TaskError {
	req, err := relaycommon.GetTaskRequest(c)
	if err != nil {
		return nil // request shape problems are the basic validator's business
	}
	ids := collectManagedAssetIDs(&req)
	if len(ids) == 0 {
		return nil
	}

	userId := info.UserId
	if userId == 0 {
		userId = c.GetInt("id")
	}
	assets, err := model.GetAssetsByAssetIDs(ids, userId)
	if err != nil {
		return assetTaskError(err, "asset_lookup_failed", http.StatusInternalServerError)
	}

	mapping := make(map[string]string, len(ids))
	for _, id := range ids {
		asset, ok := assets[id]
		if !ok {
			// Deliberately does not distinguish "no such asset" from "not
			// yours": saying which would turn this into an existence oracle for
			// other customers' assets.
			return assetTaskError(
				fmt.Errorf("reference asset %s not found", id),
				"asset_not_found", http.StatusBadRequest)
		}
		if asset.Status != model.AssetStatusActive {
			return assetTaskError(
				fmt.Errorf("reference asset %s is not ready (status %s); poll GET /v1/assets/%s until it is Active",
					id, asset.Status, id),
				"asset_not_active", http.StatusBadRequest)
		}
		mapping[id] = asset.UpstreamId
	}
	common.SetContextKey(c, contextKeyResolvedAssets, mapping)
	return nil
}

// applyResolvedAssetReferences rewrites a request copy using the mapping
// established during validation. Mutates req, which is the caller's own copy --
// the stored context request keeps the customer-facing ids.
func applyResolvedAssetReferences(c *gin.Context, req *relaycommon.TaskSubmitReq) {
	raw, exists := c.Get(contextKeyResolvedAssets)
	if !exists {
		return
	}
	mapping, ok := raw.(map[string]string)
	if !ok || len(mapping) == 0 {
		return
	}

	if rewritten, changed := rewriteAssetURI(req.Image, mapping); changed {
		req.Image = rewritten
	}
	if rewritten, changed := rewriteAssetURI(req.InputReference, mapping); changed {
		req.InputReference = rewritten
	}
	for i, img := range req.Images {
		if rewritten, changed := rewriteAssetURI(img, mapping); changed {
			req.Images[i] = rewritten
		}
	}
	rewriteMetadataURLs(req.Metadata, mapping)
}

package doubao

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/gin-gonic/gin"
)

// A real omni-reference request body: the reference lives at
// metadata.content[].image_url.url, and the prompt addresses it positionally as
// "Image 1" rather than by id -- which is why rewriting the url is safe and
// rewriting the prompt would be wrong.
func omniRequest(assetURI string) relaycommon.TaskSubmitReq {
	return relaycommon.TaskSubmitReq{
		Model:  "dreamina-seedance-2-5-260628",
		Prompt: "the woman in Image 1 turns her head slightly and smiles",
		Metadata: map[string]any{
			"resolution": "480p",
			"duration":   float64(4),
			"content": []any{
				map[string]any{"type": "text", "text": "the woman in Image 1 turns her head slightly and smiles"},
				map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": assetURI},
					"role":      "reference_image",
				},
			},
		},
	}
}

func TestCollectManagedAssetIDs(t *testing.T) {
	req := omniRequest("asset://asset_7f3a9c")
	req.Image = "asset://asset_toplevel"
	req.Images = []string{"asset://asset_list1", "https://example.com/plain.png"}
	req.InputReference = "asset://asset_inputref"

	got := collectManagedAssetIDs(&req)
	want := map[string]bool{
		"asset_7f3a9c":   true,
		"asset_toplevel": true,
		"asset_list1":    true,
		"asset_inputref": true,
	}
	if len(got) != len(want) {
		t.Fatalf("collected %v, want %d ids", got, len(want))
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected id %q", id)
		}
	}
}

// Ids we did not mint belong to a customer's own BytePlus account. Rewriting or
// rejecting them would break a legitimate direct reference.
func TestForeignAssetIDsAreIgnored(t *testing.T) {
	req := omniRequest("asset://asset-20260826083451-rhqzb")
	if ids := collectManagedAssetIDs(&req); len(ids) != 0 {
		t.Errorf("vendor-native id was claimed as ours: %v", ids)
	}

	plain := omniRequest("https://example.com/portrait.png")
	if ids := collectManagedAssetIDs(&plain); len(ids) != 0 {
		t.Errorf("plain url was treated as an asset reference: %v", ids)
	}
}

func TestRewriteAssetURI(t *testing.T) {
	mapping := map[string]string{"asset_7f3a9c": "asset-20260826083451-rhqzb"}

	got, changed := rewriteAssetURI("asset://asset_7f3a9c", mapping)
	if !changed || got != "asset://asset-20260826083451-rhqzb" {
		t.Errorf("rewrite = %q (changed=%v)", got, changed)
	}

	// Unmapped, vendor-native and plain urls all pass through untouched.
	for _, raw := range []string{
		"asset://asset_unmapped",
		"asset://asset-20260826083451-rhqzb",
		"https://example.com/a.png",
		"",
	} {
		if got, changed := rewriteAssetURI(raw, mapping); changed || got != raw {
			t.Errorf("%q was altered to %q", raw, got)
		}
	}
}

// The rewrite must reach the url nested inside metadata.content, and must leave
// the prompt alone -- the model resolves references positionally, and an id
// substituted into the prompt would be read as literal text.
func TestApplyResolvedAssetReferencesRewritesMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	common.SetContextKey(c, contextKeyResolvedAssets, map[string]string{
		"asset_7f3a9c": "asset-20260826083451-rhqzb",
	})

	req := omniRequest("asset://asset_7f3a9c")
	req.Images = []string{"asset://asset_7f3a9c"}
	originalPrompt := req.Prompt

	applyResolvedAssetReferences(c, &req)

	content, _ := req.Metadata["content"].([]any)
	entry, _ := content[1].(map[string]any)
	imageURL, _ := entry["image_url"].(map[string]any)
	if got := imageURL["url"]; got != "asset://asset-20260826083451-rhqzb" {
		t.Errorf("metadata url = %v, want the upstream id", got)
	}
	if req.Images[0] != "asset://asset-20260826083451-rhqzb" {
		t.Errorf("images[0] = %q, not rewritten", req.Images[0])
	}
	if req.Prompt != originalPrompt {
		t.Errorf("prompt was rewritten to %q; references are positional", req.Prompt)
	}
}

// With no mapping in context -- the ordinary case, a request with no asset
// references -- the request must come through byte-for-byte.
func TestApplyResolvedAssetReferencesNoopWithoutMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)

	req := omniRequest("https://example.com/portrait.png")
	applyResolvedAssetReferences(c, &req)

	content, _ := req.Metadata["content"].([]any)
	entry, _ := content[1].(map[string]any)
	imageURL, _ := entry["image_url"].(map[string]any)
	if got := imageURL["url"]; got != "https://example.com/portrait.png" {
		t.Errorf("url mutated to %v", got)
	}
}

func TestManagedAssetIDParsing(t *testing.T) {
	cases := map[string]string{
		"asset://asset_abc":         "asset_abc",
		"  asset://asset_abc  ":     "asset_abc",
		"asset://asset-2026-native": "",
		"https://example.com/a.png": "",
		"asset_abc":                 "",
		"":                          "",
	}
	for raw, want := range cases {
		got, ok := managedAssetID(raw)
		if want == "" {
			if ok {
				t.Errorf("managedAssetID(%q) claimed %q", raw, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("managedAssetID(%q) = %q,%v want %q", raw, got, ok, want)
		}
	}
}

func TestIsManagedAssetID(t *testing.T) {
	if !model.IsManagedAssetID("asset_7f3a9c") {
		t.Error("our own id not recognised")
	}
	if model.IsManagedAssetID("asset-20260826083451-rhqzb") {
		t.Error("vendor id misread as ours")
	}
}

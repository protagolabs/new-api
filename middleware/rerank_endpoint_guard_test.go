package middleware

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
)

func TestIsRerankPath(t *testing.T) {
	cases := map[string]bool{
		"/v1/rerank":           true,
		"/v1/rerank/":          true,
		"/rerank":              true,
		"/v1/chat/completions": false,
		"/v1/rerank/something": false,
		"/v1/embeddings":       false,
		"/v1/messages":         false,
		"/v1/reranked":         false,
	}
	for path, want := range cases {
		if got := isRerankPath(path); got != want {
			t.Errorf("isRerankPath(%q) = %v, want %v", path, got, want)
		}
	}
}

// An unknown model must fall through rather than being rejected here. The
// channel lookup answers those with a 404 that includes a spelling suggestion,
// which is more useful than "endpoint not supported" to someone who typed the
// name wrong.
func TestModelServesEndpointFallsThroughForUnknownModels(t *testing.T) {
	if !modelServesEndpoint("a-model-that-does-not-exist-anywhere", constant.EndpointTypeJinaRerank) {
		t.Error("unknown model was rejected here; it should reach the channel lookup instead")
	}
	if !modelServesEndpoint("", constant.EndpointTypeJinaRerank) {
		t.Error("empty model name was rejected here; the caller checks that separately")
	}
}

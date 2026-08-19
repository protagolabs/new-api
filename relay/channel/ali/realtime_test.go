package ali

import (
	"strings"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/constant"
)

func TestToWebsocketScheme(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://dashscope-intl.aliyuncs.com", "wss://dashscope-intl.aliyuncs.com"},
		{"http://localhost:3000", "ws://localhost:3000"},
		// Already-upgraded and unrecognised forms pass through untouched.
		{"wss://example.com", "wss://example.com"},
		{"ws://example.com", "ws://example.com"},
		{"", ""},
	} {
		if got := toWebsocketScheme(tc.in); got != tc.want {
			t.Errorf("toWebsocketScheme(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Realtime must dial Alibaba's own path with the model in the query string.
// Using OpenAI's /v1/realtime, or leaving the model out, fails the handshake.
func TestGetRequestURLRealtime(t *testing.T) {
	a := &Adaptor{}
	info := &relaycommon.RelayInfo{RelayMode: constant.RelayModeRealtime}
	info.ChannelMeta = &relaycommon.ChannelMeta{
		ChannelBaseUrl:    "https://dashscope-intl.aliyuncs.com",
		UpstreamModelName: "qwen3.5-omni-flash-realtime",
	}

	got, err := a.GetRequestURL(info)
	if err != nil {
		t.Fatalf("GetRequestURL: %v", err)
	}
	want := "wss://dashscope-intl.aliyuncs.com/api-ws/v1/realtime?model=qwen3.5-omni-flash-realtime"
	if got != want {
		t.Errorf("realtime URL = %q, want %q", got, want)
	}
}

// A model name needing escaping must not break the query string.
func TestGetRequestURLRealtimeEscapesModel(t *testing.T) {
	a := &Adaptor{}
	info := &relaycommon.RelayInfo{RelayMode: constant.RelayModeRealtime}
	info.ChannelMeta = &relaycommon.ChannelMeta{
		ChannelBaseUrl:    "https://dashscope-intl.aliyuncs.com",
		UpstreamModelName: "model with space&x",
	}
	got, _ := a.GetRequestURL(info)
	if strings.Contains(got, " ") || strings.Count(got, "&") != 0 {
		t.Errorf("model name not escaped: %q", got)
	}
}

// Non-realtime modes must keep their existing HTTP endpoints.
func TestGetRequestURLNonRealtimeUnchanged(t *testing.T) {
	a := &Adaptor{}
	for _, tc := range []struct {
		mode int
		want string
	}{
		{constant.RelayModeChatCompletions, "https://x.test/compatible-mode/v1/chat/completions"},
		{constant.RelayModeEmbeddings, "https://x.test/compatible-mode/v1/embeddings"},
	} {
		info := &relaycommon.RelayInfo{RelayMode: tc.mode}
		info.ChannelMeta = &relaycommon.ChannelMeta{
			ChannelBaseUrl:    "https://x.test",
			UpstreamModelName: "qwen3.5-omni-flash",
		}
		got, _ := a.GetRequestURL(info)
		if got != tc.want {
			t.Errorf("mode %d URL = %q, want %q", tc.mode, got, tc.want)
		}
		if strings.HasPrefix(got, "ws") {
			t.Errorf("mode %d must not be upgraded to a websocket URL", tc.mode)
		}
	}
}

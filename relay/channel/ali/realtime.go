package ali

import "strings"

// toWebsocketScheme rewrites an http(s) base URL for a WebSocket dial.
//
// Channel base URLs are stored as http(s) because every other endpoint on the
// same channel is plain HTTP; only realtime needs the ws(s) form. gorilla's
// dialer rejects an http:// URL outright rather than upgrading it, so the
// rewrite has to happen before the dial.
//
// A URL already in ws(s) form, or in any other shape, is returned untouched --
// this is a normaliser, not a validator, and the dial reports a bad URL far
// more clearly than a guess here would.
func toWebsocketScheme(baseURL string) string {
	switch {
	case strings.HasPrefix(baseURL, "https://"):
		return "wss://" + strings.TrimPrefix(baseURL, "https://")
	case strings.HasPrefix(baseURL, "http://"):
		return "ws://" + strings.TrimPrefix(baseURL, "http://")
	default:
		return baseURL
	}
}

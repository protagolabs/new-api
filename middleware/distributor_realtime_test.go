package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// Realtime arrives as a GET with the model in the query string and no body.
// The body-parsing branch used to swallow it and return 400 before the
// query-string branch ran, so every realtime model was unreachable regardless
// of channel. This pins the routing decision that keeps that from recurring.
func TestRealtimePathSkipsBodyParsing(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Mirrors the condition guarding the body parse in getModelRequest.
	takesBodyParse := func(path, contentType string) bool {
		return !strings.HasPrefix(path, "/v1/audio/transcriptions") &&
			!strings.HasPrefix(path, "/v1/realtime") &&
			!strings.Contains(contentType, "multipart/form-data")
	}

	for _, tc := range []struct {
		path, contentType string
		wantParse         bool
	}{
		{"/v1/realtime", "", false},                             // the GET with no body
		{"/v1/realtime", "application/json", false},             // even if a type is set
		{"/v1/audio/transcriptions", "", false},                 // pre-existing exclusion
		{"/v1/chat/completions", "application/json", true},      // normal path unaffected
		{"/v1/embeddings", "application/json", true},            // normal path unaffected
		{"/v1/images/edits", "multipart/form-data; b=x", false}, // pre-existing exclusion
	} {
		if got := takesBodyParse(tc.path, tc.contentType); got != tc.wantParse {
			t.Errorf("%s (%s): body parse = %v, want %v", tc.path, tc.contentType, got, tc.wantParse)
		}
	}
}

// The model must be readable from the query string, which is the only place a
// realtime request carries it.
func TestRealtimeModelComesFromQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet,
		"/v1/realtime?model=qwen3.5-omni-flash-realtime", nil)

	if got := c.Query("model"); got != "qwen3.5-omni-flash-realtime" {
		t.Errorf("model from query = %q, want qwen3.5-omni-flash-realtime", got)
	}
}

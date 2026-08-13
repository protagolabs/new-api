package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// Regression: /api routes registered in SetApiRouter must include CORS in
// their own handler chain. The global CORS middleware lives in SetRelayRouter,
// which runs after SetApiRouter, and gin snapshots the global chain at route
// registration time -- so without this, /api responses carry no
// Access-Control-Allow-Origin header and cross-origin callers are blocked.
func TestApiRoutesRespondWithCORS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	SetApiRouter(engine)

	// /api/log/stat requires AdminAuth; the 401 happens after CORS, so the
	// header must still be present on the response.
	req := httptest.NewRequest(http.MethodGet, "/api/log/stat?p=1", nil)
	// Origin must differ from the request Host (httptest defaults to
	// example.com) -- the cors middleware treats an origin equal to the host as
	// same-origin and intentionally adds no headers.
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"),
		"/api routes must include CORS in their chain")
}

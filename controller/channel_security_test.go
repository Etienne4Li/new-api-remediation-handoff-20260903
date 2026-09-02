package controller

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The streaming controller must not replace the exact origin selected by the
// route-level CORS middleware with a wildcard.  This test simulates the
// middleware having already selected an origin and verifies the SSE helper
// leaves it intact.
func TestOllamaPullStreamHeadersPreserveCORSOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Header("Access-Control-Allow-Origin", "https://panel.example.com")
	ctx.Header("Access-Control-Allow-Credentials", "true")

	setOllamaPullStreamHeaders(ctx)

	require.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
	require.Equal(t, "https://panel.example.com", recorder.Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, "true", recorder.Header().Get("Access-Control-Allow-Credentials"))
	require.Equal(t, "no-cache", recorder.Header().Get("Cache-Control"))
	require.Equal(t, "keep-alive", recorder.Header().Get("Connection"))
}

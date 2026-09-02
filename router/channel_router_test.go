package router

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelStatusRoutesUseOperatePermission(t *testing.T) {
	assertChannelRoutePermission(t, http.MethodPost, "/:id/status", authz.ChannelOperate, controller.UpdateChannelStatus)
	assertChannelRoutePermission(t, http.MethodPost, "/status/batch", authz.ChannelOperate, controller.BatchUpdateChannelStatus)
	assertChannelRoutePermission(t, http.MethodPut, "/", authz.ChannelWrite, controller.UpdateChannel)
}

func TestChannelDeleteRoutesUseSensitiveWritePermission(t *testing.T) {
	assertChannelRoutePermission(t, http.MethodDelete, "/:id", authz.ChannelSensitiveWrite, controller.DeleteChannel)
	assertChannelRoutePermission(t, http.MethodPost, "/batch", authz.ChannelSensitiveWrite, controller.DeleteChannelBatch)
	assertChannelRoutePermission(t, http.MethodDelete, "/disabled", authz.ChannelSensitiveWrite, controller.DeleteDisabledChannel)
	assertChannelRoutePermission(t, http.MethodPut, "/", authz.ChannelWrite, controller.UpdateChannel)
	assertChannelRoutePermission(t, http.MethodPut, "/tag", authz.ChannelWrite, controller.EditTagChannels)
	assertChannelRoutePermission(t, http.MethodPost, "/batch/tag", authz.ChannelWrite, controller.BatchSetChannelTag)
}

func TestChannelStatusRoutesRegisterWithoutConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	api := engine.Group("/api")

	require.NotPanics(t, func() {
		registerChannelRoutes(api)
	})
}

// Channel management endpoints are authenticated browser APIs too.  Keep
// their cross-origin policy on the route group so a hand-written response
// header in a streaming handler cannot accidentally broaden it to `*`.
func TestChannelRoutesApplyConfiguredCORSBeforeAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://panel.example.com")
	t.Setenv("CORS_ALLOW_CREDENTIALS", "true")

	engine := gin.New()
	api := engine.Group("/api")
	registerChannelRoutes(api)

	// A matched request must pass through CORS before AdminAuth touches the
	// database. (The channel group intentionally registers POST only; clients
	// should preflight against the same group-level policy.)
	request := httptest.NewRequest(http.MethodPost, "/api/channel/ollama/pull/stream", strings.NewReader(`{}`))
	request.Header.Set("Origin", "https://panel.example.com")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	require.NotEqual(t, http.StatusForbidden, recorder.Code)
	assert.Equal(t, "https://panel.example.com", recorder.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", recorder.Header().Get("Access-Control-Allow-Credentials"))

	// Browser clients preflight the streaming POST before sending credentials.
	// The explicit OPTIONS catch-all must be reached by CORS and answered
	// without invoking AdminAuth or any controller.
	request = httptest.NewRequest(http.MethodOptions, "/api/channel/ollama/pull/stream", nil)
	request.Header.Set("Origin", "https://panel.example.com")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
	recorder = httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.Equal(t, "https://panel.example.com", recorder.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", recorder.Header().Get("Access-Control-Allow-Credentials"))
	assert.Contains(t, recorder.Header().Get("Access-Control-Allow-Methods"), http.MethodPost)

	// The same preflight from an untrusted site must be rejected before the
	// wildcard OPTIONS handler can produce a usable response.
	request = httptest.NewRequest(http.MethodOptions, "/api/channel/ollama/pull/stream", nil)
	request.Header.Set("Origin", "https://evil.example")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	recorder = httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Empty(t, recorder.Header().Get("Access-Control-Allow-Origin"))

	// An untrusted origin must be rejected at the CORS boundary, rather than
	// reaching a state-changing channel controller.
	request = httptest.NewRequest(http.MethodPost, "/api/channel/ollama/pull/stream", strings.NewReader(`{}`))
	request.Header.Set("Origin", "https://evil.example")
	recorder = httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Empty(t, recorder.Header().Get("Access-Control-Allow-Origin"))
}

func assertChannelRoutePermission(t *testing.T, method string, path string, permission authz.Permission, handler any) {
	t.Helper()
	for _, route := range channelPermissionRoutes {
		if route.method == method && route.path == path {
			assert.Equal(t, permission, route.permission)
			assert.Equal(t, reflect.ValueOf(handler).Pointer(), reflect.ValueOf(route.handler).Pointer())
			return
		}
	}
	t.Fatalf("route %s %s not found", method, path)
}

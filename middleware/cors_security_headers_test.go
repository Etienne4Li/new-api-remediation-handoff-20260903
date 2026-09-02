package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func serveCORSRequest(t *testing.T, method, origin string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	router.Use(CORS())
	router.Any("/resource", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	request := httptest.NewRequest(method, "https://api.example.com/resource", nil)
	request.Host = "api.example.com"
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestCORSAllowsConfiguredExactOriginWithCredentials(t *testing.T) {
	t.Setenv(corsAllowedOriginsEnv, "https://panel.example.com")
	t.Setenv("CORS_ALLOW_CREDENTIALS", "true")

	response := serveCORSRequest(t, http.MethodGet, "https://panel.example.com", nil)

	assert.Equal(t, http.StatusNoContent, response.Code)
	assert.Equal(t, "https://panel.example.com", response.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", response.Header().Get("Access-Control-Allow-Credentials"))
	assert.Contains(t, response.Header().Get("Vary"), "Origin")
}

func TestCORSRejectsUnconfiguredOrigin(t *testing.T) {
	t.Setenv(corsAllowedOriginsEnv, "https://panel.example.com")

	response := serveCORSRequest(t, http.MethodGet, "https://evil.example", nil)

	assert.Equal(t, http.StatusForbidden, response.Code)
	assert.Empty(t, response.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORSUsesFrontendBaseURLWhenAllowlistIsUnset(t *testing.T) {
	t.Setenv(corsAllowedOriginsEnv, "")
	t.Setenv("FRONTEND_BASE_URL", "https://panel.example.com/app")

	response := serveCORSRequest(t, http.MethodGet, "https://panel.example.com", nil)

	assert.Equal(t, http.StatusForbidden, response.Code, "a URL path must not broaden the origin allowlist")
}

func TestCORSPreflightReturnsExactOriginAndAllowedHeaders(t *testing.T) {
	t.Setenv(corsAllowedOriginsEnv, "https://panel.example.com")

	response := serveCORSRequest(t, http.MethodOptions, "https://panel.example.com", map[string]string{
		"Access-Control-Request-Method":  http.MethodPost,
		"Access-Control-Request-Headers": "authorization,content-type,x-auth-session",
	})

	assert.Equal(t, http.StatusNoContent, response.Code)
	assert.Equal(t, "https://panel.example.com", response.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, response.Header().Get("Access-Control-Allow-Methods"), "POST")
	assert.Contains(t, response.Header().Get("Access-Control-Allow-Headers"), "Authorization")
}

func TestSecurityHeadersSetConservativeDefaults(t *testing.T) {
	t.Setenv("SECURITY_HEADERS_HSTS", "true")
	t.Setenv("SECURITY_HEADERS_HSTS_INCLUDE_SUBDOMAINS", "true")
	router := gin.New()
	router.Use(SecurityHeaders())
	router.GET("/resource", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "https://api.example.com/resource", nil)
	request.Host = "api.example.com"
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusNoContent, recorder.Code)
	assert.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "strict-origin-when-cross-origin", recorder.Header().Get("Referrer-Policy"))
	assert.Equal(t, "camera=(), geolocation=(), microphone=(), payment=(), usb=()", recorder.Header().Get("Permissions-Policy"))
	assert.Equal(t, "SAMEORIGIN", recorder.Header().Get("X-Frame-Options"))
	assert.Equal(t, "object-src 'none'; base-uri 'self'; frame-ancestors 'self'", recorder.Header().Get("Content-Security-Policy"))
	assert.Equal(t, "max-age=31536000; includeSubDomains", recorder.Header().Get("Strict-Transport-Security"))
}

func TestSecurityHeadersSkipHSTSForLocalhostByDefault(t *testing.T) {
	t.Setenv("SECURITY_HEADERS_HSTS", "")
	router := gin.New()
	router.Use(SecurityHeaders())
	router.GET("/resource", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://localhost:3000/resource", nil)
	request.Host = "localhost:3000"
	router.ServeHTTP(recorder, request)

	assert.Empty(t, recorder.Header().Get("Strict-Transport-Security"))
}

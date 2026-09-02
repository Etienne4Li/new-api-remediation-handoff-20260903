package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requestClientIP(router http.Handler, remoteAddr string, forwardedFor string) string {
	_, body := requestClientIPWithHeaders(router, remoteAddr, map[string][]string{
		"X-Forwarded-For": {forwardedFor},
	})
	return body
}

func requestClientIPWithHeaders(router http.Handler, remoteAddr string, headers map[string][]string) (int, string) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/client-ip", nil)
	request.RemoteAddr = remoteAddr
	for name, values := range headers {
		for _, value := range values {
			if value != "" {
				request.Header.Add(name, value)
			}
		}
	}
	router.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

func newClientIPRouter() *gin.Engine {
	router := gin.New()
	// Install before the route is registered; Gin snapshots the middleware
	// chain when handle() is called. The sanitizer reads the engine's current
	// trusted-proxy list at request time so each test can configure the env
	// immediately after constructing the router.
	router.Use(SanitizeProxyHeaders())
	router.GET("/client-ip", func(c *gin.Context) {
		c.String(http.StatusOK, c.ClientIP())
	})
	return router
}

func TestConfigureTrustedProxiesDefaultsToDirectPeerOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("TRUSTED_PROXIES", "")
	router := newClientIPRouter()
	require.NoError(t, ConfigureTrustedProxies(router))

	testCases := []struct {
		name       string
		remoteAddr string
		expected   string
	}{
		{name: "IPv4 loopback", remoteAddr: "127.0.0.1:12345", expected: "127.0.0.1"},
		{name: "IPv6 loopback", remoteAddr: "[::1]:12345", expected: "::1"},
		{name: "10 private network", remoteAddr: "10.20.30.40:12345", expected: "10.20.30.40"},
		{name: "172 private network", remoteAddr: "172.20.0.2:12345", expected: "172.20.0.2"},
		{name: "192 private network", remoteAddr: "192.168.10.2:12345", expected: "192.168.10.2"},
		{name: "IPv6 unique local network", remoteAddr: "[fd12:3456::2]:12345", expected: "fd12:3456::2"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			clientIP := requestClientIP(router, testCase.remoteAddr, "203.0.113.10")
			assert.Equal(t, testCase.expected, clientIP)
		})
	}
}

func TestConfigureTrustedProxiesDefaultRejectsPublicPeerHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("TRUSTED_PROXIES", " \t ")
	router := newClientIPRouter()
	require.NoError(t, ConfigureTrustedProxies(router))

	for _, header := range []string{"X-Forwarded-For", "X-Real-IP", "CF-Connecting-IP"} {
		_, clientIP := requestClientIPWithHeaders(router, "198.51.100.10:12345", map[string][]string{
			header: {"203.0.113.10"},
		})
		assert.Equal(t, "198.51.100.10", clientIP, "%s from a public peer must not be authoritative", header)
	}
}

func TestConfigureTrustedProxiesExplicitListStopsAtPublicClientInForwardedChain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("TRUSTED_PROXIES", "172.16.0.0/12,10.0.0.0/8")
	router := newClientIPRouter()
	require.NoError(t, ConfigureTrustedProxies(router))

	clientIP := requestClientIP(router, "172.20.0.2:12345", "192.0.2.99, 203.0.113.10")
	assert.Equal(t, "203.0.113.10", clientIP, "the first public hop from the trusted proxy must win over a client-supplied prefix")

	clientIP = requestClientIP(router, "172.20.0.2:12345", "203.0.113.10, 10.20.0.3")
	assert.Equal(t, "203.0.113.10", clientIP, "trusted proxy hops are skipped from the right")

	clientIP = requestClientIP(router, "172.20.0.2:12345", "203.0.113.10, 10.20.0.3, 10.20.0.4")
	assert.Equal(t, "203.0.113.10", clientIP, "multiple trusted proxy hops must not expose a forged prefix")
}

func TestConfigureTrustedProxiesExplicitListHandlesCompatibilityHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("TRUSTED_PROXIES", "10.0.0.0/8")
	router := newClientIPRouter()
	require.NoError(t, ConfigureTrustedProxies(router))

	_, body := requestClientIPWithHeaders(router, "10.0.0.2:12345", map[string][]string{
		"X-Real-IP": {"198.51.100.20"},
	})
	assert.Equal(t, "198.51.100.20", body)
	_, body = requestClientIPWithHeaders(router, "10.0.0.2:12345", map[string][]string{
		"CF-Connecting-IP": {"198.51.100.21"},
	})
	assert.Equal(t, "198.51.100.21", body)
	for _, header := range []string{"X-Forwarded-For", "X-Real-IP", "CF-Connecting-IP"} {
		status, body := requestClientIPWithHeaders(router, "10.0.0.2:12345", map[string][]string{
			header: {"not-an-ip"},
		})
		assert.Equal(t, http.StatusOK, status, "malformed %s must fall back safely", header)
		assert.Equal(t, "10.0.0.2", body)
	}

	// Duplicate forwarding headers are not accepted as a client identity. A
	// proxy configuration must sanitize these at the edge before forwarding.
	status, body := requestClientIPWithHeaders(router, "10.0.0.2:12345", map[string][]string{
		"X-Forwarded-For": {"198.51.100.22", "203.0.113.22"},
	})
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "10.0.0.2", body)
}

func TestConfigureTrustedProxiesNoneDisablesForwardedHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("TRUSTED_PROXIES", " NoNe ")
	router := newClientIPRouter()
	require.NoError(t, ConfigureTrustedProxies(router))

	clientIP := requestClientIP(router, "127.0.0.1:12345", "203.0.113.10")
	assert.Equal(t, "127.0.0.1", clientIP)
}

func TestConfigureTrustedProxiesAcceptsTrimmedIPsAndCIDRs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("TRUSTED_PROXIES", " 192.0.2.0/24, 198.51.100.30 ")
	router := newClientIPRouter()
	require.NoError(t, ConfigureTrustedProxies(router))

	trustedClientIP := requestClientIP(router, "192.0.2.10:12345", "203.0.113.20")
	assert.Equal(t, "203.0.113.20", trustedClientIP)

	trustedExactIP := requestClientIP(router, "198.51.100.30:12345", "203.0.113.21")
	assert.Equal(t, "203.0.113.21", trustedExactIP)

	untrustedClientIP := requestClientIP(router, "198.51.100.20:12345", "203.0.113.22")
	assert.Equal(t, "198.51.100.20", untrustedClientIP)

	defaultProxyIP := requestClientIP(router, "127.0.0.1:12345", "203.0.113.23")
	assert.Equal(t, "127.0.0.1", defaultProxyIP, "an explicit list must replace, not extend, the compatibility defaults")
}

func TestConfigureTrustedProxiesRejectsInvalidConfiguration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	testCases := []struct {
		name  string
		value string
	}{
		{name: "no entries", value: ", ,"},
		{name: "invalid entry", value: "not-an-ip"},
		{name: "hostname entry", value: "proxy.internal"},
		{name: "mixed valid and invalid entries", value: "127.0.0.1, not-an-ip"},
		{name: "none mixed with valid entry", value: "none,127.0.0.1"},
		{name: "valid entry mixed with none", value: "127.0.0.1,NONE"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("TRUSTED_PROXIES", testCase.value)
			router := newClientIPRouter()
			assert.Error(t, ConfigureTrustedProxies(router))
		})
	}
}

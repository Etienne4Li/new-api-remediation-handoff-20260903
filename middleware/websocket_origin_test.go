package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
)

func TestWebSocketOriginAllowsNonBrowserClientWithoutOrigin(t *testing.T) {
	req := httptest.NewRequest("GET", "https://api.example.com/v1/realtime", nil)
	assert.True(t, WebSocketOriginAllowed(req))
}

func TestWebSocketOriginRequiresExactConfiguredOrigin(t *testing.T) {
	t.Setenv("WEBSOCKET_ALLOWED_ORIGINS", "https://panel.example.com")
	req := httptest.NewRequest("GET", "https://api.example.com/v1/realtime", nil)
	req.Header.Set("Origin", "https://panel.example.com")
	assert.True(t, WebSocketOriginAllowed(req))

	req.Header.Set("Origin", "https://evil.example.com")
	assert.False(t, WebSocketOriginAllowed(req))
}

func TestWebSocketOriginRejectsAmbiguousAndInvalidValues(t *testing.T) {
	t.Setenv("WEBSOCKET_ALLOWED_ORIGINS", "https://panel.example.com")
	for _, origin := range []string{
		"https://panel.example.com, https://evil.example.com",
		"null",
		"https://panel.example.com/path",
		"https://panel.example.com.evil.example.com",
		"https://*.example.com",
	} {
		req := httptest.NewRequest("GET", "https://api.example.com/v1/realtime", nil)
		req.Header.Set("Origin", origin)
		assert.False(t, WebSocketOriginAllowed(req), "origin %q must be rejected", origin)
	}
}

func TestWebSocketOriginAllowsConfiguredSessionTrustedURL(t *testing.T) {
	previous := common.SessionCookieTrustedURLs
	common.SessionCookieTrustedURLs = []string{"https://panel.example.com"}
	t.Cleanup(func() { common.SessionCookieTrustedURLs = previous })

	req := httptest.NewRequest("GET", "https://api.example.com/v1/realtime", nil)
	req.Header.Set("Origin", "https://panel.example.com")
	assert.True(t, WebSocketOriginAllowed(req))
}

func TestWebSocketOriginAllowsSameOriginOnly(t *testing.T) {
	req := httptest.NewRequest("GET", "https://api.example.com/v1/realtime", nil)
	req.Header.Set("Origin", "https://api.example.com")
	assert.True(t, WebSocketOriginAllowed(req))

	req.Header.Set("Origin", "http://api.example.com")
	assert.False(t, WebSocketOriginAllowed(req), "scheme downgrade must not be accepted")
}

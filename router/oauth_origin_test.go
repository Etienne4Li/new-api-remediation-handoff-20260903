package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOAuthStateRouteEnforcesSessionCookieOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	require.NoError(t, i18n.Init())
	previousSecure := common.SessionCookieSecure
	previousTrusted := common.SessionCookieTrustedURLs
	previousGlobalRate := common.GlobalApiRateLimitEnable
	previousCriticalRate := common.CriticalRateLimitEnable
	common.SessionCookieSecure = true
	common.SessionCookieTrustedURLs = []string{"https://trusted.example.com"}
	common.GlobalApiRateLimitEnable = false
	common.CriticalRateLimitEnable = false
	t.Cleanup(func() {
		common.SessionCookieSecure = previousSecure
		common.SessionCookieTrustedURLs = previousTrusted
		common.GlobalApiRateLimitEnable = previousGlobalRate
		common.CriticalRateLimitEnable = previousCriticalRate
	})

	engine := gin.New()
	SetApiRouter(engine)

	request := func(origin, referer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "https://panel.example.com/api/oauth/state", strings.NewReader(`{}`))
		req.Host = "panel.example.com"
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if referer != "" {
			req.Header.Set("Referer", referer)
		}
		res := httptest.NewRecorder()
		engine.ServeHTTP(res, req)
		return res
	}

	// Origin checks run before the handler; these requests must never reach the
	// OAuth state generator.
	if response := request("https://evil.example.com", ""); response.Code != http.StatusForbidden {
		t.Fatalf("evil origin status = %d, want %d", response.Code, http.StatusForbidden)
	}
	if response := request("", ""); response.Code != http.StatusForbidden {
		t.Fatalf("missing origin status = %d, want %d", response.Code, http.StatusForbidden)
	}

	// Same-origin, trusted frontend, and Referer fallback pass the guard. The
	// empty payload is intentionally invalid, so the handler returns a normal
	// validation response rather than creating a flow or touching the database.
	for _, tc := range []struct {
		name    string
		origin  string
		referer string
	}{
		{name: "same origin", origin: "https://panel.example.com"},
		{name: "trusted origin", origin: "https://trusted.example.com"},
		{name: "same origin referer", referer: "https://panel.example.com/sign-in"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := request(tc.origin, tc.referer)
			if response.Code == http.StatusForbidden {
				t.Fatalf("allowed origin was rejected: status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

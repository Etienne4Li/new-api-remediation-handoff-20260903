package middleware

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tokenAuthStatusRouter exposes only the authentication result and resolved
// user id.  Keeping the handler deliberately small lets these tests compare
// the read-only and relay authentication boundaries without coupling them to
// any particular controller response shape.
func tokenAuthStatusRouter(t *testing.T, auth gin.HandlerFunc) *gin.Engine {
	t.Helper()
	router := gin.New()
	// Do not let Gin's test default (trust all proxies) make an allowlist test
	// depend on forwarding headers.  Production config applies the same
	// direct-peer/trusted-proxy policy before either middleware runs.
	require.NoError(t, router.SetTrustedProxies(nil))
	router.GET("/protected", auth, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"id": c.GetInt("id")})
	})
	return router
}

func performReadOnlyAuthRequest(router http.Handler, key, remoteAddr string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.RemoteAddr = remoteAddr
	request.Header.Set("Authorization", "Bearer sk-"+key)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func performRawAuthRequest(router http.Handler, authorization, remoteAddr string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.RemoteAddr = remoteAddr
	request.Header.Set("Authorization", authorization)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestTokenAuthReadOnlyEnforcesTokenAndUserSecurityBoundariesLikeTokenAuth(t *testing.T) {
	setupRelayTokenAuthTest(t)

	enabledUser := createRelayAuthUser(t, "readonly-enabled-user", common.RoleCommonUser)
	disabledUser := createRelayAuthUser(t, "readonly-disabled-user", common.RoleCommonUser)
	disabledUser.Status = common.UserStatusDisabled
	require.NoError(t, model.DB.Model(disabledUser).Update("status", common.UserStatusDisabled).Error)

	// Use separate rows for each boundary.  TokenAuth marks an expired token as
	// expired when Redis is disabled; sharing that row would hide whether the
	// read-only path performed its own expiry check.
	expiredToken := createRelayAuthToken(t, enabledUser.Id, "readonly-expired", common.TokenStatusEnabled)
	expiredToken.ExpiredTime = time.Now().Add(-time.Minute).Unix()
	require.NoError(t, model.DB.Model(expiredToken).Update("expired_time", expiredToken.ExpiredTime).Error)
	disabledToken := createRelayAuthToken(t, enabledUser.Id, "readonly-disabled", common.TokenStatusDisabled)
	createRelayAuthToken(t, disabledUser.Id, "readonly-user-disabled", common.TokenStatusEnabled)

	allowIP := "203.0.113.10"
	ipToken := createRelayAuthToken(t, enabledUser.Id, "readonly-ip", common.TokenStatusEnabled)
	ipToken.AllowIps = &allowIP
	require.NoError(t, model.DB.Model(ipToken).Update("allow_ips", allowIP).Error)

	readOnlyRouter := tokenAuthStatusRouter(t, TokenAuthReadOnly())
	normalRouter := tokenAuthStatusRouter(t, TokenAuth())
	for _, test := range []struct {
		name       string
		key        string
		remoteAddr string
		wantStatus int
	}{
		{name: "expired token", key: expiredToken.Key, wantStatus: http.StatusUnauthorized},
		{name: "disabled token", key: disabledToken.Key, wantStatus: http.StatusUnauthorized},
		{name: "disabled user", key: "readonly-user-disabled", wantStatus: http.StatusForbidden},
		{name: "ip allowlist mismatch", key: ipToken.Key, remoteAddr: "198.51.100.20:1234", wantStatus: http.StatusForbidden},
		{name: "ip allowlist match", key: ipToken.Key, remoteAddr: net.JoinHostPort(allowIP, "1234"), wantStatus: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			remoteAddr := test.remoteAddr
			if remoteAddr == "" {
				remoteAddr = "198.51.100.20:1234"
			}
			readOnly := performReadOnlyAuthRequest(readOnlyRouter, test.key, remoteAddr)
			assert.Equal(t, test.wantStatus, readOnly.Code)

			// The normal relay path is the contract for expiry, explicit token
			// disablement, user status, and IP restrictions.  These checks should
			// not silently diverge just because the endpoint is read-only.
			normal := performReadOnlyAuthRequest(normalRouter, test.key, remoteAddr)
			assert.Equal(t, test.wantStatus, normal.Code)
		})
	}
}

func TestTokenAuthReadOnlyDoesNotFoldAnUppercaseSkPrefix(t *testing.T) {
	setupRelayTokenAuthTest(t)

	prefixOwner := createRelayAuthUser(t, "readonly-prefix-owner", common.RoleCommonUser)
	exactOwner := createRelayAuthUser(t, "readonly-exact-owner", common.RoleCommonUser)
	createRelayAuthToken(t, prefixOwner.Id, "uppercase-prefix-collision", common.TokenStatusEnabled)
	exactToken := createRelayAuthToken(t, exactOwner.Id, "SK-uppercase-prefix-collision", common.TokenStatusEnabled)

	readOnlyRouter := tokenAuthStatusRouter(t, TokenAuthReadOnly())
	normalRouter := tokenAuthStatusRouter(t, TokenAuth())
	authorization := "Bearer " + exactToken.Key
	readOnly := performRawAuthRequest(readOnlyRouter, authorization, "198.51.100.20:1234")
	normal := performRawAuthRequest(normalRouter, authorization, "198.51.100.20:1234")

	// Token keys are opaque.  Read-only auth must resolve the same row as the
	// relay path; case-folding the conventional `sk-` transport prefix would
	// otherwise turn an exact `SK-...` key into another user's token.
	assert.Equal(t, http.StatusOK, normal.Code)
	assert.Equal(t, normal.Code, readOnly.Code)
	var normalBody, readOnlyBody struct {
		ID int `json:"id"`
	}
	require.NoError(t, common.Unmarshal(normal.Body.Bytes(), &normalBody))
	require.NoError(t, common.Unmarshal(readOnly.Body.Bytes(), &readOnlyBody))
	assert.Equal(t, exactOwner.Id, normalBody.ID)
	assert.Equal(t, normalBody.ID, readOnlyBody.ID)
}

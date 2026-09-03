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

// setupReadOnlyTokenAuthTest builds on the dashboard auth helper (in-memory
// sqlite, Redis disabled) and additionally initializes the quoted column names
// that token lookups rely on, which only initCol sets. InitLogDB with an empty
// LOG_SQL_DSN reuses model.DB and runs initCol without opening a second DB.
func setupReadOnlyTokenAuthTest(t *testing.T) {
	t.Helper()
	setupDashboardAuthMiddlewareTest(t)
	previousLogDB := model.LOG_DB
	previousLogType := common.LogDatabaseType()
	t.Setenv("LOG_SQL_DSN", "")
	require.NoError(t, model.InitLogDB())
	t.Cleanup(func() {
		model.LOG_DB = previousLogDB
		common.SetLogDatabaseType(previousLogType)
	})
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}))
	gin.SetMode(gin.TestMode)
}

func createReadOnlyAuthUser(t *testing.T, username string, status int) *model.User {
	t.Helper()
	user := &model.User{
		Username:    username,
		Password:    "password-placeholder",
		Role:        common.RoleCommonUser,
		Status:      status,
		Group:       "default",
		AuthVersion: 1,
		AffCode:     username + "-aff",
	}
	require.NoError(t, model.DB.Create(user).Error)
	return user
}

func createReadOnlyAuthToken(t *testing.T, userID int, key string, status int) *model.Token {
	t.Helper()
	token := &model.Token{
		UserId:         userID,
		Key:            key,
		Name:           key,
		Status:         status,
		ExpiredTime:    -1,
		RemainQuota:    1,
		UnlimitedQuota: true,
	}
	require.NoError(t, model.DB.Create(token).Error)
	return token
}

func readOnlyAuthRouter(t *testing.T) *gin.Engine {
	t.Helper()
	router := gin.New()
	// Gin's test default trusts every proxy; pin the policy so the IP allowlist
	// cases depend on RemoteAddr only, like production behind TRUSTED_PROXIES.
	require.NoError(t, router.SetTrustedProxies(nil))
	router.GET("/protected", TokenAuthReadOnly(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"id": c.GetInt("id"), "token_id": c.GetInt("token_id")})
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

func TestTokenAuthReadOnlyEnforcesTokenSecurityBoundaries(t *testing.T) {
	setupReadOnlyTokenAuthTest(t)

	enabledUser := createReadOnlyAuthUser(t, "readonly-enabled-user", common.UserStatusEnabled)
	disabledUser := createReadOnlyAuthUser(t, "readonly-disabled-user", common.UserStatusDisabled)

	okToken := createReadOnlyAuthToken(t, enabledUser.Id, "readonlyok", common.TokenStatusEnabled)
	exhaustedToken := createReadOnlyAuthToken(t, enabledUser.Id, "readonlyexhausted", common.TokenStatusExhausted)
	// Status still says enabled, but the deadline has passed: the read-only path
	// must perform its own expiry check instead of trusting the status column.
	expiredByTime := createReadOnlyAuthToken(t, enabledUser.Id, "readonlyexpiredtime", common.TokenStatusEnabled)
	expiredByTime.ExpiredTime = time.Now().Add(-time.Minute).Unix()
	require.NoError(t, model.DB.Model(expiredByTime).Update("expired_time", expiredByTime.ExpiredTime).Error)
	expiredByStatus := createReadOnlyAuthToken(t, enabledUser.Id, "readonlyexpiredstatus", common.TokenStatusExpired)
	disabledToken := createReadOnlyAuthToken(t, enabledUser.Id, "readonlydisabled", common.TokenStatusDisabled)
	// The status column carries a gorm default, so a zero value must be forced
	// after insert to model an old or corrupted row.
	zeroStatusToken := createReadOnlyAuthToken(t, enabledUser.Id, "readonlyzerostatus", common.TokenStatusEnabled)
	require.NoError(t, model.DB.Model(zeroStatusToken).Update("status", 0).Error)
	createReadOnlyAuthToken(t, disabledUser.Id, "readonlyuserdisabled", common.TokenStatusEnabled)

	allowIP := "203.0.113.10"
	ipToken := createReadOnlyAuthToken(t, enabledUser.Id, "readonlyip", common.TokenStatusEnabled)
	ipToken.AllowIps = &allowIP
	require.NoError(t, model.DB.Model(ipToken).Update("allow_ips", allowIP).Error)

	router := readOnlyAuthRouter(t)
	const otherAddr = "198.51.100.20:1234"
	for _, test := range []struct {
		name       string
		key        string
		remoteAddr string
		wantStatus int
	}{
		{name: "enabled token", key: okToken.Key, wantStatus: http.StatusOK},
		{name: "exhausted token may still inspect usage", key: exhaustedToken.Key, wantStatus: http.StatusOK},
		{name: "expired by deadline", key: expiredByTime.Key, wantStatus: http.StatusUnauthorized},
		{name: "expired by status", key: expiredByStatus.Key, wantStatus: http.StatusUnauthorized},
		{name: "disabled token", key: disabledToken.Key, wantStatus: http.StatusUnauthorized},
		{name: "unknown zero status", key: zeroStatusToken.Key, wantStatus: http.StatusUnauthorized},
		{name: "unknown key", key: "readonlydoesnotexist", wantStatus: http.StatusUnauthorized},
		{name: "disabled user", key: "readonlyuserdisabled", wantStatus: http.StatusForbidden},
		{name: "ip allowlist mismatch", key: ipToken.Key, remoteAddr: otherAddr, wantStatus: http.StatusForbidden},
		{name: "ip allowlist match", key: ipToken.Key, remoteAddr: net.JoinHostPort(allowIP, "1234"), wantStatus: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			remoteAddr := test.remoteAddr
			if remoteAddr == "" {
				remoteAddr = otherAddr
			}
			response := performReadOnlyAuthRequest(router, test.key, remoteAddr)
			assert.Equal(t, test.wantStatus, response.Code, response.Body.String())
			if test.wantStatus == http.StatusOK {
				assert.Equal(t, "no-store, private, max-age=0", response.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestTokenAuthReadOnlyRejectsMissingOrEmptyKey(t *testing.T) {
	setupReadOnlyTokenAuthTest(t)
	router := readOnlyAuthRouter(t)

	for _, authorization := range []string{"", "Bearer ", "Bearer sk-"} {
		request := httptest.NewRequest(http.MethodGet, "/protected", nil)
		request.RemoteAddr = "198.51.100.20:1234"
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assert.Equal(t, http.StatusUnauthorized, response.Code, "authorization=%q", authorization)
	}
}

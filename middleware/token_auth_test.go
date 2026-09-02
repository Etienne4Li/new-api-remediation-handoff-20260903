package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupRelayTokenAuthTest(t *testing.T) {
	t.Helper()
	setupDashboardAuthMiddlewareTest(t)
	// The middleware auth helper uses a minimal in-memory database and therefore
	// does not run model.initCol. InitLogDB reuses that database while
	// initializing the quoted column names used by token lookups.
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

func createRelayAuthUser(t *testing.T, username string, role int) *model.User {
	t.Helper()
	user := &model.User{
		Username:    username,
		Password:    "password-placeholder",
		Role:        role,
		Status:      common.UserStatusEnabled,
		Group:       "default",
		AuthVersion: 1,
		AffCode:     username + "-aff",
	}
	require.NoError(t, model.DB.Create(user).Error)
	return user
}

func createRelayAuthToken(t *testing.T, userID int, key string, status int) *model.Token {
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

func relayTokenAuthRouter() *gin.Engine {
	router := gin.New()
	router.GET("/protected", TokenAuth(), func(c *gin.Context) {
		specific, _ := c.Get("specific_channel_id")
		c.JSON(http.StatusOK, gin.H{
			"id":                  c.GetInt("id"),
			"token_key":           c.GetString("token_key"),
			"specific_channel_id": fmt.Sprint(specific),
		})
	})
	return router
}

func performRelayTokenAuthRequest(router http.Handler, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.Header.Set("Authorization", "Bearer sk-"+key)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func geminiTokenAuthRouter() *gin.Engine {
	router := gin.New()
	router.GET("/v1/models", TokenAuth(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	return router
}

func performGeminiTokenAuthRequest(router http.Handler, path string, headerKey string, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if headerKey != "" {
		request.Header.Set(headerKey, key)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestGeminiQueryKeyAuthEnabledDefaultsClosed(t *testing.T) {
	t.Setenv(GeminiQueryKeyAuthEnv, "")
	assert.False(t, GeminiQueryKeyAuthEnabled())

	t.Setenv(GeminiQueryKeyAuthEnv, "true")
	assert.True(t, GeminiQueryKeyAuthEnabled())

	t.Setenv(GeminiQueryKeyAuthEnv, "not-a-bool")
	assert.False(t, GeminiQueryKeyAuthEnabled())
}

func TestTokenAuthRejectsGeminiQueryKeyByDefault(t *testing.T) {
	setupRelayTokenAuthTest(t)
	t.Setenv(GeminiQueryKeyAuthEnv, "")
	user := createRelayAuthUser(t, "relay-gemini-query-user", common.RoleCommonUser)
	createRelayAuthToken(t, user.Id, "relay-gemini-query-key", common.TokenStatusEnabled)

	response := performGeminiTokenAuthRequest(
		geminiTokenAuthRouter(), "/v1/models?key=relay-gemini-query-key", "", "",
	)

	assert.Equal(t, http.StatusUnauthorized, response.Code)
}

func TestTokenAuthAcceptsGeminiHeaderWithoutQueryKeyOptIn(t *testing.T) {
	setupRelayTokenAuthTest(t)
	t.Setenv(GeminiQueryKeyAuthEnv, "")
	user := createRelayAuthUser(t, "relay-gemini-header-user", common.RoleCommonUser)
	createRelayAuthToken(t, user.Id, "relay-gemini-header-key", common.TokenStatusEnabled)

	response := performGeminiTokenAuthRequest(
		geminiTokenAuthRouter(), "/v1/models", "x-goog-api-key", "relay-gemini-header-key",
	)

	assert.Equal(t, http.StatusNoContent, response.Code)
}

func TestTokenAuthRejectsGeminiQueryKeyEvenWithHeaderByDefault(t *testing.T) {
	setupRelayTokenAuthTest(t)
	t.Setenv(GeminiQueryKeyAuthEnv, "")
	user := createRelayAuthUser(t, "relay-gemini-header-query-user", common.RoleCommonUser)
	createRelayAuthToken(t, user.Id, "relay-gemini-header-query-key", common.TokenStatusEnabled)

	request := httptest.NewRequest(http.MethodGet, "/v1/models?key=legacy-query-secret", nil)
	request.Header.Set("x-goog-api-key", "relay-gemini-header-query-key")
	response := httptest.NewRecorder()
	geminiTokenAuthRouter().ServeHTTP(response, request)

	assert.Equal(t, http.StatusUnauthorized, response.Code)
}

func TestTokenAuthAcceptsGeminiQueryKeyOnlyWithExplicitOptIn(t *testing.T) {
	setupRelayTokenAuthTest(t)
	t.Setenv(GeminiQueryKeyAuthEnv, "true")
	user := createRelayAuthUser(t, "relay-gemini-query-optin-user", common.RoleCommonUser)
	createRelayAuthToken(t, user.Id, "relay-gemini-query-optin-key", common.TokenStatusEnabled)

	response := performGeminiTokenAuthRequest(
		geminiTokenAuthRouter(), "/v1/models?key=relay-gemini-query-optin-key", "", "",
	)

	assert.Equal(t, http.StatusNoContent, response.Code)
}

func TestTokenAuthPreservesHyphenatedTokenKey(t *testing.T) {
	setupRelayTokenAuthTest(t)
	user := createRelayAuthUser(t, "relay-hyphenated-user", common.RoleCommonUser)
	token := createRelayAuthToken(t, user.Id, "hyphenated-token-key", common.TokenStatusEnabled)

	response := performRelayTokenAuthRequest(relayTokenAuthRouter(), token.Key)

	assert.Equal(t, http.StatusOK, response.Code)
	var body struct {
		ID       int    `json:"id"`
		TokenKey string `json:"token_key"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	assert.Equal(t, user.Id, body.ID)
	assert.Equal(t, token.Key, body.TokenKey)
}

func TestTokenAuthDoesNotUsePrefixCollision(t *testing.T) {
	setupRelayTokenAuthTest(t)
	prefixUser := createRelayAuthUser(t, "relay-prefix-user", common.RoleCommonUser)
	fullUser := createRelayAuthUser(t, "relay-full-user", common.RoleCommonUser)
	createRelayAuthToken(t, prefixUser.Id, "prefix", common.TokenStatusEnabled)
	fullToken := createRelayAuthToken(t, fullUser.Id, "prefix-hyphenated", common.TokenStatusEnabled)

	response := performRelayTokenAuthRequest(relayTokenAuthRouter(), fullToken.Key)

	assert.Equal(t, http.StatusOK, response.Code)
	var body struct {
		ID       int    `json:"id"`
		TokenKey string `json:"token_key"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	assert.Equal(t, fullUser.Id, body.ID)
	assert.Equal(t, fullToken.Key, body.TokenKey)
}

func TestTokenAuthDoesNotFallbackFromDisabledExactToken(t *testing.T) {
	setupRelayTokenAuthTest(t)
	user := createRelayAuthUser(t, "relay-disabled-exact-user", common.RoleCommonUser)
	createRelayAuthToken(t, user.Id, "prefix", common.TokenStatusEnabled)
	disabledToken := createRelayAuthToken(t, user.Id, "prefix-hyphenated", common.TokenStatusDisabled)

	response := performRelayTokenAuthRequest(relayTokenAuthRouter(), disabledToken.Key)

	assert.Equal(t, http.StatusUnauthorized, response.Code)
}

func TestTokenAuthAllowsAdminLegacyChannelSuffix(t *testing.T) {
	setupRelayTokenAuthTest(t)
	admin := createRelayAuthUser(t, "relay-admin-user", common.RoleAdminUser)
	createRelayAuthToken(t, admin.Id, "admin-base-key", common.TokenStatusEnabled)

	response := performRelayTokenAuthRequest(relayTokenAuthRouter(), "admin-base-key-123")

	assert.Equal(t, http.StatusOK, response.Code)
	var body struct {
		ID                int    `json:"id"`
		SpecificChannelID string `json:"specific_channel_id"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &body))
	assert.Equal(t, admin.Id, body.ID)
	assert.Equal(t, "123", body.SpecificChannelID)
}

func TestTokenAuthRejectsNonCanonicalAdminChannelSuffix(t *testing.T) {
	setupRelayTokenAuthTest(t)
	admin := createRelayAuthUser(t, "relay-admin-malformed-user", common.RoleAdminUser)
	createRelayAuthToken(t, admin.Id, "admin-malformed-base", common.TokenStatusEnabled)
	router := relayTokenAuthRouter()

	for _, suffix := range []string{"123-extra", "0", "-1", "+2", "01", " 123"} {
		t.Run(suffix, func(t *testing.T) {
			response := performRelayTokenAuthRequest(router, "admin-malformed-base-"+suffix)
			assert.Equal(t, http.StatusUnauthorized, response.Code)
		})
	}
}

func TestTokenAuthRejectsSpecificChannelSuffixForOrdinaryUser(t *testing.T) {
	setupRelayTokenAuthTest(t)
	user := createRelayAuthUser(t, "relay-ordinary-suffix-user", common.RoleCommonUser)
	createRelayAuthToken(t, user.Id, "ordinary-base-key", common.TokenStatusEnabled)

	response := performRelayTokenAuthRequest(relayTokenAuthRouter(), "ordinary-base-key-123")

	assert.Equal(t, http.StatusForbidden, response.Code)
}

func TestTokenAuthAllowsOrdinaryHyphenatedExactToken(t *testing.T) {
	setupRelayTokenAuthTest(t)
	user := createRelayAuthUser(t, "relay-ordinary-hyphen-user", common.RoleCommonUser)
	token := createRelayAuthToken(t, user.Id, "ordinary-hyphenated-key", common.TokenStatusEnabled)

	response := performRelayTokenAuthRequest(relayTokenAuthRouter(), token.Key)

	assert.Equal(t, http.StatusOK, response.Code)
}

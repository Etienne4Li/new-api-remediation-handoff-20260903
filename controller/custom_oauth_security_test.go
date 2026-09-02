package controller

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

func newUserOAuthBindingsContext(t *testing.T, role int, userID int, path string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("role", role)
	ctx.Set("id", userID)
	ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(userID)}}
	ctx.Request = httptest.NewRequest(http.MethodGet, path, nil)
	return ctx, recorder
}

func decodeUserOAuthBindingsPayload(t *testing.T, recorder *httptest.ResponseRecorder) (bool, []map[string]interface{}) {
	t.Helper()
	var payload struct {
		Success bool                     `json:"success"`
		Data    []map[string]interface{} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	return payload.Success, payload.Data
}

func TestAdminOAuthBindingsRedactProviderIdentity(t *testing.T) {
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.CustomOAuthProvider{}, &model.UserOAuthBinding{}))
	admin := &model.User{Username: "oauth-admin", Password: "password", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, AffCode: "oauth-admin-aff"}
	target := &model.User{Username: "oauth-target", Password: "password", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AffCode: "oauth-target-aff"}
	peer := &model.User{Username: "oauth-peer", Password: "password", Role: common.RoleAdminUser, Status: common.UserStatusEnabled, AffCode: "oauth-peer-aff"}
	for _, user := range []*model.User{admin, target, peer} {
		require.NoError(t, db.Create(user).Error)
	}
	provider := &model.CustomOAuthProvider{
		Name: "Security Provider", Slug: "security-provider", Enabled: true,
		ClientId: "client", ClientSecret: "client-secret",
		AuthorizationEndpoint: "https://issuer.example/authorize",
		TokenEndpoint:         "https://issuer.example/token",
		UserInfoEndpoint:      "https://issuer.example/userinfo",
	}
	require.NoError(t, db.Create(provider).Error)
	const providerIdentity = "provider-sensitive-identity-123"
	require.NoError(t, db.Create(&model.UserOAuthBinding{
		UserId: target.Id, ProviderId: provider.Id, ProviderUserId: providerIdentity,
	}).Error)

	ctx, recorder := newUserOAuthBindingsContext(t, common.RoleAdminUser, target.Id, "/api/user/"+fmt.Sprint(target.Id)+"/oauth/bindings")
	GetUserOAuthBindingsByAdmin(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	success, data := decodeUserOAuthBindingsPayload(t, recorder)
	require.True(t, success, recorder.Body.String())
	require.Len(t, data, 1)
	assert.EqualValues(t, provider.Id, data[0]["provider_id"])
	assert.Equal(t, provider.Name, data[0]["provider_name"])
	assert.True(t, data[0]["is_bound"].(bool))
	assert.NotContains(t, data[0], "provider_user_id")
	assert.NotContains(t, recorder.Body.String(), providerIdentity)

	// The self endpoint retains the identity value for the account owner; this
	// is a separate authorization boundary and keeps profile unbind UX intact.
	ctx, recorder = newUserOAuthBindingsContext(t, common.RoleCommonUser, target.Id, "/api/user/oauth/bindings")
	GetUserOAuthBindings(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	success, data = decodeUserOAuthBindingsPayload(t, recorder)
	require.True(t, success, recorder.Body.String())
	require.Len(t, data, 1)
	assert.Equal(t, providerIdentity, data[0]["provider_user_id"])

	// A regular admin must not inspect a same-level administrator's bindings.
	ctx, recorder = newUserOAuthBindingsContext(t, common.RoleAdminUser, peer.Id, "/api/user/"+fmt.Sprint(peer.Id)+"/oauth/bindings")
	GetUserOAuthBindingsByAdmin(ctx)
	success, data = decodeUserOAuthBindingsPayload(t, recorder)
	assert.False(t, success)
	assert.Nil(t, data)
}

func TestAdminBuiltInBindingStatusContainsOnlyBooleans(t *testing.T) {
	db := setupManageUserTestDB(t)
	target := &model.User{
		Username: "binding-status-target", Password: "password", Role: common.RoleCommonUser, Status: common.UserStatusEnabled,
		AffCode: "binding-status-aff",
		Email:   "binding-status@example.com", GitHubId: "github-secret-id", OidcId: "oidc-secret-id",
		TelegramId: "telegram-secret-id", Setting: `{"webhook_secret":"do-not-return"}`,
	}
	require.NoError(t, db.Create(target).Error)

	ctx, recorder := newUserOAuthBindingsContext(t, common.RoleAdminUser, target.Id, "/api/user/"+fmt.Sprint(target.Id)+"/binding-status")
	GetUserBindingStatusByAdmin(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Success bool                   `json:"success"`
		Data    map[string]interface{} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	require.True(t, payload.Success, recorder.Body.String())
	assert.Equal(t, true, payload.Data["email"])
	assert.Equal(t, true, payload.Data["github_id"])
	assert.Equal(t, true, payload.Data["oidc_id"])
	assert.Equal(t, true, payload.Data["telegram_id"])
	assert.Equal(t, false, payload.Data["discord_id"])
	assert.NotContains(t, recorder.Body.String(), "github-secret-id")
	assert.NotContains(t, recorder.Body.String(), "oidc-secret-id")
	assert.NotContains(t, recorder.Body.String(), "telegram-secret-id")
	assert.NotContains(t, recorder.Body.String(), "do-not-return")
}

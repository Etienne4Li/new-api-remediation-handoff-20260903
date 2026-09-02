package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newAdminUserDetailContext(t *testing.T, role int, userID int) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("role", role)
	ctx.Request = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/user/%d", userID), nil)
	ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(userID)}}
	return ctx, recorder
}

func decodeAdminUserDetailPayload(t *testing.T, recorder *httptest.ResponseRecorder) (bool, map[string]interface{}) {
	t.Helper()
	var payload struct {
		Success bool                   `json:"success"`
		Data    map[string]interface{} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	return payload.Success, payload.Data
}

func insertUsersForAdminDetailSecurityTest(t *testing.T) (root, peer, ordinary model.User) {
	t.Helper()
	users := []*model.User{
		{
			Username: "detail-root", Password: "detail-root-password", DisplayName: "Detail Root",
			Role: common.RoleRootUser, Status: common.UserStatusEnabled, Group: "default",
			Email: "detail-root@example.com", GitHubId: "detail-root-github", DiscordId: "detail-root-discord",
			OidcId: "detail-root-oidc", WeChatId: "detail-root-wechat", TelegramId: "detail-root-telegram",
			LinuxDOId: "detail-root-linuxdo", Setting: `{"webhook_secret":"detail-webhook","gotify_token":"detail-gotify"}`,
			StripeCustomer: "cus_detail_root", AffCode: "detail-root-aff", Remark: "root remark",
		},
		{
			Username: "detail-peer", Password: "detail-peer-password", DisplayName: "Detail Peer",
			Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Group: "vip",
			Email: "detail-peer@example.com", GitHubId: "detail-peer-github", Setting: `{"webhook_secret":"peer-webhook"}`,
			StripeCustomer: "cus_detail_peer", AffCode: "detail-peer-aff", Remark: "peer remark",
		},
		{
			Username: "detail-ordinary", Password: "detail-ordinary-password", DisplayName: "Detail Ordinary",
			Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default",
			Email: "detail-ordinary@example.com", GitHubId: "detail-ordinary-github", OidcId: "detail-ordinary-oidc",
			Setting:        `{"webhook_secret":"ordinary-webhook","gotify_token":"ordinary-gotify"}`,
			StripeCustomer: "cus_detail_ordinary", AffCode: "detail-ordinary-aff", AffCount: 4,
			AffQuota: 5, AffHistoryQuota: 6, InviterId: 7, Quota: 100, UsedQuota: 40, RequestCount: 9,
			Remark: "ordinary remark",
		},
	}
	for _, user := range users {
		require.NoError(t, model.DB.Create(user).Error)
	}
	return *users[0], *users[1], *users[2]
}

func assertAdminUserDetailSafe(t *testing.T, data map[string]interface{}) {
	t.Helper()
	allowed := map[string]bool{
		"id": true, "username": true, "display_name": true, "role": true,
		"status": true, "quota": true, "used_quota": true, "request_count": true,
		"group": true, "aff_count": true, "aff_quota": true, "aff_history_quota": true,
		"inviter_id": true, "remark": true, "created_at": true, "last_login_at": true,
		"DeletedAt": true, "admin_permissions": true,
	}
	for field := range data {
		assert.True(t, allowed[field], "unexpected admin-detail field %s", field)
	}
	for _, field := range []string{
		"password", "access_token", "email", "github_id", "discord_id", "oidc_id",
		"wechat_id", "telegram_id", "linux_do_id", "setting", "stripe_customer", "aff_code",
	} {
		assert.NotContains(t, data, field, "admin detail must not expose %s", field)
	}
}

func TestGetUserReturnsSafeDetailForOrdinaryTarget(t *testing.T) {
	setupManageUserTestDB(t)
	_, _, ordinary := insertUsersForAdminDetailSecurityTest(t)

	ctx, recorder := newAdminUserDetailContext(t, common.RoleAdminUser, ordinary.Id)
	GetUser(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	success, data := decodeAdminUserDetailPayload(t, recorder)
	require.True(t, success, recorder.Body.String())
	assert.Equal(t, ordinary.Username, data["username"])
	assert.EqualValues(t, ordinary.Quota, data["quota"])
	assert.EqualValues(t, ordinary.UsedQuota, data["used_quota"])
	assert.EqualValues(t, ordinary.AffHistoryQuota, data["aff_history_quota"])
	assertAdminUserDetailSafe(t, data)
	for _, secret := range []string{
		ordinary.Password, ordinary.Email, ordinary.GitHubId, ordinary.OidcId,
		"ordinary-webhook", "ordinary-gotify", ordinary.StripeCustomer, ordinary.AffCode,
	} {
		assert.NotContains(t, recorder.Body.String(), secret)
	}
}

func TestGetUserRejectsSameLevelAndHigherTargets(t *testing.T) {
	setupManageUserTestDB(t)
	root, peer, _ := insertUsersForAdminDetailSecurityTest(t)

	for _, target := range []model.User{peer, root} {
		ctx, recorder := newAdminUserDetailContext(t, common.RoleAdminUser, target.Id)
		GetUser(ctx)
		success, data := decodeAdminUserDetailPayload(t, recorder)
		assert.False(t, success, "admin must not inspect %s", target.Username)
		assert.Nil(t, data)
		assert.NotContains(t, recorder.Body.String(), target.Email)
	}
}

func TestGetUserRootCanInspectRootWithoutSensitiveFields(t *testing.T) {
	setupManageUserTestDB(t)
	root, _, _ := insertUsersForAdminDetailSecurityTest(t)

	ctx, recorder := newAdminUserDetailContext(t, common.RoleRootUser, root.Id)
	GetUser(ctx)
	success, data := decodeAdminUserDetailPayload(t, recorder)
	require.True(t, success, recorder.Body.String())
	assert.Equal(t, root.Username, data["username"])
	assert.EqualValues(t, common.RoleRootUser, data["role"])
	assertAdminUserDetailSafe(t, data)
	for _, secret := range []string{
		root.Password, root.Email, root.GitHubId, root.DiscordId, root.OidcId,
		root.WeChatId, root.TelegramId, root.LinuxDOId, "detail-webhook", "detail-gotify",
		root.StripeCustomer, root.AffCode,
	} {
		assert.False(t, strings.Contains(recorder.Body.String(), secret), "response leaked %q", secret)
	}
}

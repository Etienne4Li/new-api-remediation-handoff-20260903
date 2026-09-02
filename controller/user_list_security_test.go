package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type adminUserListPayload struct {
	Success bool `json:"success"`
	Data    struct {
		Items []map[string]interface{} `json:"items"`
		Total int                      `json:"total"`
	} `json:"data"`
}

func newAdminUserListContext(t *testing.T, role int, path string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("role", role)
	ctx.Request = httptest.NewRequest(http.MethodGet, path, nil)
	return ctx, recorder
}

func decodeAdminUserListPayload(t *testing.T, recorder *httptest.ResponseRecorder) adminUserListPayload {
	t.Helper()
	require.Equal(t, http.StatusOK, recorder.Code)
	var payload adminUserListPayload
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	require.True(t, payload.Success, recorder.Body.String())
	return payload
}

func insertUsersForAdminListSecurityTest(t *testing.T) (root, peer, ordinary model.User) {
	t.Helper()
	users := []*model.User{
		{
			Username: "list-root", Password: "root-password", DisplayName: "List Root",
			Role: common.RoleRootUser, Status: common.UserStatusEnabled, Group: "default",
			Email: "root-list@example.com", GitHubId: "root-github", OidcId: "root-oidc",
			TelegramId: "root-telegram", LinuxDOId: "root-linuxdo", AffCode: "root-aff",
			StripeCustomer: "cus_root", Setting: `{"webhook_secret":"root-webhook","gotify_token":"root-gotify"}`,
			Remark: "root internal note",
		},
		{
			Username: "list-peer-admin", Password: "peer-password", DisplayName: "List Peer",
			Role: common.RoleAdminUser, Status: common.UserStatusEnabled, Group: "default",
			Email: "peer-list@example.com", GitHubId: "peer-github", AffCode: "peer-aff", Setting: `{"webhook_secret":"peer-webhook"}`,
			Remark: "peer internal note",
		},
		{
			Username: "list-ordinary", Password: "ordinary-password", DisplayName: "List Ordinary",
			Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default",
			Email: "ordinary-list@example.com", GitHubId: "ordinary-github", AffCode: "ordinary-aff", Setting: `{"gotify_token":"ordinary-gotify"}`,
			AffCount: 2, AffHistoryQuota: 300, InviterId: 123, Remark: "ordinary internal note",
		},
	}
	for _, user := range users {
		require.NoError(t, model.DB.Create(user).Error)
	}
	return *users[0], *users[1], *users[2]
}

func assertAdminListOmitsSensitiveFields(t *testing.T, item map[string]interface{}) {
	t.Helper()
	allowed := map[string]bool{
		"id": true, "username": true, "display_name": true, "role": true,
		"status": true, "quota": true, "used_quota": true, "request_count": true,
		"group": true, "aff_count": true, "aff_quota": true, "aff_history_quota": true, "inviter_id": true,
		"remark": true, "created_at": true, "last_login_at": true, "DeletedAt": true,
	}
	for field := range item {
		assert.True(t, allowed[field], "unexpected admin-list field %s", field)
	}
	for _, field := range []string{
		"password", "access_token", "email", "github_id", "discord_id", "oidc_id",
		"wechat_id", "telegram_id", "linux_do_id", "setting", "stripe_customer", "aff_code",
	} {
		assert.NotContains(t, item, field, "admin list must not expose %s", field)
	}
}

func TestAdminUserListScopesRolesAndUsesSafeProjection(t *testing.T) {
	setupManageUserTestDB(t)
	root, peer, ordinary := insertUsersForAdminListSecurityTest(t)

	ctx, recorder := newAdminUserListContext(t, common.RoleAdminUser, "/api/user/?p=1&page_size=20")
	GetAllUsers(ctx)
	payload := decodeAdminUserListPayload(t, recorder)
	require.Equal(t, 1, payload.Data.Total)
	require.Len(t, payload.Data.Items, 1)
	assert.Equal(t, ordinary.Username, payload.Data.Items[0]["username"])
	assert.EqualValues(t, ordinary.AffHistoryQuota, payload.Data.Items[0]["aff_history_quota"])
	assert.EqualValues(t, ordinary.InviterId, payload.Data.Items[0]["inviter_id"])
	assertAdminListOmitsSensitiveFields(t, payload.Data.Items[0])
	assert.NotContains(t, recorder.Body.String(), "root-webhook")
	assert.NotContains(t, recorder.Body.String(), "ordinary-gotify")

	// Root is explicitly allowed to enumerate every role, but the same safe
	// projection still applies to root responses.
	ctx, recorder = newAdminUserListContext(t, common.RoleRootUser, "/api/user/?p=1&page_size=20")
	GetAllUsers(ctx)
	payload = decodeAdminUserListPayload(t, recorder)
	require.Equal(t, 3, payload.Data.Total)
	require.Len(t, payload.Data.Items, 3)
	seen := make(map[string]bool, len(payload.Data.Items))
	for _, item := range payload.Data.Items {
		seen[fmt.Sprint(item["username"])] = true
		assertAdminListOmitsSensitiveFields(t, item)
	}
	assert.True(t, seen[root.Username])
	assert.True(t, seen[peer.Username])
	assert.True(t, seen[ordinary.Username])
	assert.NotContains(t, recorder.Body.String(), "root-webhook")
	assert.NotContains(t, recorder.Body.String(), "root-gotify")
}

func TestAdminUserSearchCannotBypassRoleScope(t *testing.T) {
	setupManageUserTestDB(t)
	root, peer, _ := insertUsersForAdminListSecurityTest(t)

	keywords := []string{root.Username, peer.Username, root.Email, peer.Email, strconv.Itoa(root.Id), strconv.Itoa(peer.Id)}
	for _, keyword := range keywords {
		t.Run(keyword, func(t *testing.T) {
			path := "/api/user/search?keyword=" + keyword + "&page_size=20"
			ctx, recorder := newAdminUserListContext(t, common.RoleAdminUser, path)
			SearchUsers(ctx)
			payload := decodeAdminUserListPayload(t, recorder)
			assert.Zero(t, payload.Data.Total)
			assert.Empty(t, payload.Data.Items)
			assert.NotContains(t, recorder.Body.String(), "root-webhook")
		})
	}

	// Root can request a role-specific search and receive the root row, but no
	// credential-bearing fields are added to that response.
	ctx, recorder := newAdminUserListContext(t, common.RoleRootUser, "/api/user/search?keyword="+strings.TrimSpace(root.Username))
	SearchUsers(ctx)
	payload := decodeAdminUserListPayload(t, recorder)
	require.Equal(t, 1, payload.Data.Total)
	require.Len(t, payload.Data.Items, 1)
	assert.Equal(t, root.Username, payload.Data.Items[0]["username"])
	assertAdminListOmitsSensitiveFields(t, payload.Data.Items[0])
}

func TestAdminUserListFailsClosedWithoutValidatedAdminRole(t *testing.T) {
	setupManageUserTestDB(t)
	insertUsersForAdminListSecurityTest(t)

	for _, actorRole := range []int{common.RoleGuestUser, common.RoleCommonUser, 42} {
		ctx, recorder := newAdminUserListContext(t, actorRole, "/api/user/?p=1&page_size=20")
		GetAllUsers(ctx)
		payload := decodeAdminUserListPayload(t, recorder)
		assert.Zero(t, payload.Data.Total, "role %d must not enumerate users", actorRole)
		assert.Empty(t, payload.Data.Items)
	}
}

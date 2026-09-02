package controller

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type adminTopUpListPayload struct {
	Success bool `json:"success"`
	Data    struct {
		Items []model.TopUp `json:"items"`
		Total int           `json:"total"`
	} `json:"data"`
}

func decodeAdminTopUpListPayload(t *testing.T, recorderBody []byte) adminTopUpListPayload {
	t.Helper()
	var payload adminTopUpListPayload
	require.NoError(t, common.Unmarshal(recorderBody, &payload))
	require.True(t, payload.Success)
	return payload
}

func TestAdminTopUpListAndSearchRespectTargetRoles(t *testing.T) {
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.TopUp{}))
	root, peer, ordinary := insertUsersForAdminListSecurityTest(t)

	orders := []model.TopUp{
		{UserId: root.Id, TradeNo: "ROLE-TOPUP-ROOT", CreateTime: common.GetTimestamp(), Status: common.TopUpStatusPending},
		{UserId: peer.Id, TradeNo: "ROLE-TOPUP-PEER", CreateTime: common.GetTimestamp(), Status: common.TopUpStatusPending},
		{UserId: ordinary.Id, TradeNo: "ROLE-TOPUP-COMMON", CreateTime: common.GetTimestamp(), Status: common.TopUpStatusPending},
	}
	for i := range orders {
		require.NoError(t, db.Create(&orders[i]).Error)
	}

	c, recorder := newAdminUserListContext(t, common.RoleAdminUser, "/api/user/topup?page_size=20")
	GetAllTopUps(c)
	payload := decodeAdminTopUpListPayload(t, recorder.Body.Bytes())
	require.Equal(t, 1, payload.Data.Total)
	require.Len(t, payload.Data.Items, 1)
	assert.Equal(t, ordinary.Id, payload.Data.Items[0].UserId)

	c, recorder = newAdminUserListContext(t, common.RoleAdminUser, "/api/user/topup?keyword=ROLE-TOPUP-ROOT&page_size=20")
	GetAllTopUps(c)
	payload = decodeAdminTopUpListPayload(t, recorder.Body.Bytes())
	assert.Zero(t, payload.Data.Total)
	assert.Empty(t, payload.Data.Items)

	c, recorder = newAdminUserListContext(t, common.RoleRootUser, "/api/user/topup?page_size=20")
	GetAllTopUps(c)
	payload = decodeAdminTopUpListPayload(t, recorder.Body.Bytes())
	assert.Equal(t, 3, payload.Data.Total)
	assert.Len(t, payload.Data.Items, 3)
}

func TestAdminCannotManuallyCompleteHigherRoleTopUp(t *testing.T) {
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.TopUp{}))
	root := model.User{
		Username: "manual-topup-root", Password: "unused", Role: common.RoleRootUser,
		Status: common.UserStatusEnabled, Group: "default", AffCode: "manual-topup-root-aff",
	}
	require.NoError(t, db.Create(&root).Error)
	order := model.TopUp{
		UserId: root.Id, TradeNo: "ROLE-MANUAL-ROOT", Amount: 100, Money: 1,
		CreditedQuota: 100, CreateTime: common.GetTimestamp(), Status: common.TopUpStatusPending,
	}
	require.NoError(t, db.Create(&order).Error)

	c, recorder := newSubscriptionAdminContext(
		http.MethodPost,
		"/api/user/topup/complete",
		fmt.Sprintf(`{"trade_no":%q}`, order.TradeNo),
		gin.Params{},
	)
	AdminCompleteTopUp(c)
	assertSubscriptionRoleDenied(t, c, recorder)

	var persisted model.TopUp
	require.NoError(t, db.First(&persisted, order.Id).Error)
	assert.Equal(t, common.TopUpStatusPending, persisted.Status)
	var persistedRoot model.User
	require.NoError(t, db.First(&persistedRoot, root.Id).Error)
	assert.Zero(t, persistedRoot.Quota)
}

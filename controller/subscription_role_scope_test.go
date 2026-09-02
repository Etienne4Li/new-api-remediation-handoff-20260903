package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSubscriptionAdminContext(method string, path string, body string, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	c.Set("id", 9001)
	c.Set("role", common.RoleAdminUser)
	c.Set("username", "subscription-admin")
	return c, recorder
}

func assertSubscriptionRoleDenied(t *testing.T, c *gin.Context, recorder *httptest.ResponseRecorder) {
	t.Helper()
	var response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Success)
	assert.Equal(t, common.TranslateMessage(c, i18n.MsgUserNoPermissionHigherLevel), response.Message)
}

func TestSubscriptionAdminEndpointsRejectHigherRoleTargets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.SubscriptionPlan{},
		&model.UserSubscription{},
		&model.SubscriptionOrder{},
	))
	confirmPaymentComplianceForTest(t)

	root := model.User{
		Username: "subscription-root-target", Password: "unused-password-hash",
		Role: common.RoleRootUser, Status: common.UserStatusEnabled, Group: "default",
	}
	require.NoError(t, db.Create(&root).Error)
	plan := model.SubscriptionPlan{
		Title: "Role boundary plan", DurationUnit: model.SubscriptionDurationMonth,
		DurationValue: 1, TotalAmount: 1000, Enabled: true,
	}
	require.NoError(t, db.Create(&plan).Error)
	subscription := model.UserSubscription{
		UserId: root.Id, PlanId: plan.Id, AmountTotal: 1000, AmountUsed: 500,
		StartTime: time.Now().Add(-time.Hour).Unix(), EndTime: time.Now().Add(time.Hour).Unix(), Status: "active",
	}
	require.NoError(t, db.Create(&subscription).Error)
	order := model.SubscriptionOrder{
		UserId: root.Id, PlanId: plan.Id, TradeNo: "SUB-ROOT-RETRY", Status: "paid_uncredited",
	}
	require.NoError(t, db.Create(&order).Error)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		params gin.Params
		call   func(*gin.Context)
	}{
		{
			name: "list", method: http.MethodGet, path: fmt.Sprintf("/api/subscription/admin/users/%d/subscriptions", root.Id),
			params: gin.Params{{Key: "id", Value: fmt.Sprint(root.Id)}}, call: AdminListUserSubscriptions,
		},
		{
			name: "bind", method: http.MethodPost, path: "/api/subscription/admin/bind",
			body: fmt.Sprintf(`{"user_id":%d,"plan_id":%d}`, root.Id, plan.Id), call: AdminBindSubscription,
		},
		{
			name: "create", method: http.MethodPost, path: fmt.Sprintf("/api/subscription/admin/users/%d/subscriptions", root.Id),
			body: fmt.Sprintf(`{"plan_id":%d}`, plan.Id), params: gin.Params{{Key: "id", Value: fmt.Sprint(root.Id)}}, call: AdminCreateUserSubscription,
		},
		{
			name: "reset", method: http.MethodPost, path: fmt.Sprintf("/api/subscription/admin/users/%d/subscriptions/reset", root.Id),
			body: fmt.Sprintf(`{"plan_id":%d}`, plan.Id), params: gin.Params{{Key: "id", Value: fmt.Sprint(root.Id)}}, call: AdminResetUserSubscriptionsByPlan,
		},
		{
			name: "invalidate", method: http.MethodPost, path: fmt.Sprintf("/api/subscription/admin/user_subscriptions/%d/invalidate", subscription.Id),
			params: gin.Params{{Key: "id", Value: fmt.Sprint(subscription.Id)}}, call: AdminInvalidateUserSubscription,
		},
		{
			name: "delete", method: http.MethodDelete, path: fmt.Sprintf("/api/subscription/admin/user_subscriptions/%d", subscription.Id),
			params: gin.Params{{Key: "id", Value: fmt.Sprint(subscription.Id)}}, call: AdminDeleteUserSubscription,
		},
		{
			name: "retry paid uncredited", method: http.MethodPost, path: "/api/subscription/admin/orders/SUB-ROOT-RETRY/retry-paid-uncredited",
			params: gin.Params{{Key: "trade_no", Value: order.TradeNo}}, call: AdminRetryPaidUncreditedSubscriptionOrder,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, recorder := newSubscriptionAdminContext(test.method, test.path, test.body, test.params)
			test.call(c)
			assertSubscriptionRoleDenied(t, c, recorder)
		})
	}

	var persistedSubscription model.UserSubscription
	require.NoError(t, db.First(&persistedSubscription, subscription.Id).Error)
	assert.Equal(t, "active", persistedSubscription.Status)
	assert.EqualValues(t, 500, persistedSubscription.AmountUsed)
	var count int64
	require.NoError(t, db.Model(&model.UserSubscription{}).Where("user_id = ?", root.Id).Count(&count).Error)
	assert.Equal(t, int64(1), count)
	var persistedOrder model.SubscriptionOrder
	require.NoError(t, db.Where("trade_no = ?", order.TradeNo).First(&persistedOrder).Error)
	assert.Equal(t, "paid_uncredited", persistedOrder.Status)
}

func TestSubscriptionAdminEndpointsRejectMalformedPathIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupManageUserTestDB(t)
	require.NoError(t, db.AutoMigrate(
		&model.SubscriptionPlan{},
		&model.UserSubscription{},
		&model.SubscriptionOrder{},
	))
	confirmPaymentComplianceForTest(t)

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		param  string
		call   func(*gin.Context)
	}{
		{name: "update plan", method: http.MethodPut, path: "/api/subscription/admin/plans/not-an-id", body: `{"plan":{"title":"x"}}`, param: "not-an-id", call: AdminUpdateSubscriptionPlan},
		{name: "update plan status", method: http.MethodPut, path: "/api/subscription/admin/plans/not-an-id/status", body: `{"enabled":true}`, param: "not-an-id", call: AdminUpdateSubscriptionPlanStatus},
		{name: "list user subscriptions", method: http.MethodGet, path: "/api/subscription/admin/users/not-an-id/subscriptions", param: "not-an-id", call: AdminListUserSubscriptions},
		{name: "create user subscription", method: http.MethodPost, path: "/api/subscription/admin/users/not-an-id/subscriptions", body: `{"plan_id":1}`, param: "not-an-id", call: AdminCreateUserSubscription},
		{name: "reset user subscriptions", method: http.MethodPost, path: "/api/subscription/admin/users/not-an-id/subscriptions/reset", body: `{"plan_id":1}`, param: "not-an-id", call: AdminResetUserSubscriptionsByPlan},
		{name: "reset plan subscriptions", method: http.MethodPost, path: "/api/subscription/admin/plans/not-an-id/reset", body: `{}`, param: "not-an-id", call: AdminResetPlanSubscriptions},
		{name: "invalidate subscription", method: http.MethodPost, path: "/api/subscription/admin/user_subscriptions/not-an-id/invalidate", param: "not-an-id", call: AdminInvalidateUserSubscription},
		{name: "delete subscription", method: http.MethodDelete, path: "/api/subscription/admin/user_subscriptions/not-an-id", param: "not-an-id", call: AdminDeleteUserSubscription},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, recorder := newSubscriptionAdminContext(test.method, test.path, test.body, gin.Params{{Key: "id", Value: test.param}})
			test.call(c)
			var response struct {
				Success bool `json:"success"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			assert.False(t, response.Success, "malformed path id must fail closed")
		})
	}
}

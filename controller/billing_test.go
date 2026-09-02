package controller

import (
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func preserveBillingHandlerSettings(t *testing.T) {
	t.Helper()
	previousGeneral := common.GetGeneralRuntimeConfig()
	previousDisplay := *operation_setting.GetGeneralSetting()
	previousQuotaPerUnit := common.GetQuotaPerUnit()
	t.Cleanup(func() {
		common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) { *cfg = previousGeneral })
		*operation_setting.GetGeneralSetting() = previousDisplay
		common.SetQuotaPerUnit(previousQuotaPerUnit)
	})
}

func invokeBillingHandler(t *testing.T, handler gin.HandlerFunc, tokenID int) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/dashboard/billing", nil)
	c.Set("id", 1)
	c.Set("token_id", tokenID)
	handler(c)
	return recorder
}

func TestBillingHandlersFailClosedWhenTokenDisappears(t *testing.T) {
	preserveBillingHandlerSettings(t)
	setupAccountBalanceTestDB(t)
	common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) {
		cfg.DisplayTokenStatEnabled = true
	})

	for _, test := range []struct {
		name    string
		handler gin.HandlerFunc
	}{
		{name: "subscription", handler: GetSubscription},
		{name: "usage", handler: GetUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := invokeBillingHandler(t, test.handler, 999999)
			// The handler keeps the historical OpenAI-compatible 200 envelope, but
			// must return an error object instead of dereferencing a nil Token.
			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"error"`)
			assert.NotContains(t, recorder.Body.String(), `"object":"billing_subscription"`)
		})
	}
}

func TestGetSubscriptionPromotesQuotaBeforeSumming(t *testing.T) {
	preserveBillingHandlerSettings(t)
	setupAccountBalanceTestDB(t)
	common.UpdateGeneralRuntimeConfig(func(cfg *common.GeneralRuntimeConfig) {
		cfg.DisplayTokenStatEnabled = true
	})
	common.SetQuotaPerUnit(1)
	token := &model.Token{
		UserId:      1,
		Key:         "billing-overflow-token",
		Name:        "overflow",
		Status:      common.TokenStatusEnabled,
		ExpiredTime: -1,
		RemainQuota: math.MaxInt,
		UsedQuota:   math.MaxInt,
	}
	require.NoError(t, model.DB.Create(token).Error)

	recorder := invokeBillingHandler(t, GetSubscription, token.Id)
	assert.Equal(t, http.StatusOK, recorder.Code)
	var payload OpenAISubscriptionResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	// Even an int64-sized addition would overflow when both values are maximal;
	// the exported billing contract must retain a large positive balance.
	assert.Equal(t, float64(math.MaxInt), payload.SoftLimitUSD)
	assert.Equal(t, payload.SoftLimitUSD, payload.HardLimitUSD)
}

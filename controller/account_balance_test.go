package controller

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func preserveAccountBalanceSettings(t *testing.T) {
	t.Helper()
	originalQuotaPerUnit := common.QuotaPerUnit
	originalUSDExchangeRate := operation_setting.USDExchangeRate
	originalGeneralSetting := *operation_setting.GetGeneralSetting()
	t.Cleanup(func() {
		common.QuotaPerUnit = originalQuotaPerUnit
		operation_setting.USDExchangeRate = originalUSDExchangeRate
		*operation_setting.GetGeneralSetting() = originalGeneralSetting
	})
}

func setupAccountBalanceTestDB(t *testing.T) {
	t.Helper()

	previousDB := model.DB
	previousLogDB := model.LOG_DB
	previousSQLitePath := common.SQLitePath
	previousIsMasterNode := common.IsMasterNode
	previousRedisEnabled := common.RedisEnabled
	previousMainDatabaseType := common.MainDatabaseType()
	previousLogDatabaseType := common.LogDatabaseType()

	common.SQLitePath = filepath.Join(t.TempDir(), "account-balance.db")
	common.IsMasterNode = false
	common.RedisEnabled = false
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	t.Setenv("SQL_DSN", "local")
	require.NoError(t, model.InitDB())
	require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.Token{}))

	t.Cleanup(func() {
		if sqlDB, err := model.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		common.SQLitePath = previousSQLitePath
		common.IsMasterNode = previousIsMasterNode
		common.RedisEnabled = previousRedisEnabled
		common.SetDatabaseTypes(previousMainDatabaseType, previousLogDatabaseType)
	})
}

func performAccountBalanceRequest(router http.Handler, key string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/api/usage/balance/", nil)
	if key != "" {
		request.Header.Set("Authorization", "Bearer sk-"+key)
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestAccountBalanceUsesFixedCNYUnit(t *testing.T) {
	preserveAccountBalanceSettings(t)
	common.QuotaPerUnit = 500_000

	tests := []struct {
		name         string
		displayType  string
		exchangeRate float64
	}{
		{name: "USD display", displayType: operation_setting.QuotaDisplayTypeUSD, exchangeRate: 7.3},
		{name: "CNY display", displayType: operation_setting.QuotaDisplayTypeCNY, exchangeRate: 7.3},
		{name: "tokens display", displayType: operation_setting.QuotaDisplayTypeTokens, exchangeRate: 7.3},
		{name: "custom display", displayType: operation_setting.QuotaDisplayTypeCustom, exchangeRate: 0.8},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation_setting.GetGeneralSetting().QuotaDisplayType = test.displayType
			operation_setting.USDExchangeRate = test.exchangeRate

			balance := accountBalanceForDisplay(1_250_000)

			assert.InDelta(t, 2.5, balance.Remaining, 0.000001)
			assert.Equal(t, "CNY", balance.Unit)
		})
	}
}

func TestGetAccountBalanceUsesUserQuotaThroughReadOnlyTokenAuth(t *testing.T) {
	preserveAccountBalanceSettings(t)
	setupAccountBalanceTestDB(t)
	gin.SetMode(gin.TestMode)
	common.QuotaPerUnit = 500_000
	operation_setting.USDExchangeRate = 7.3
	operation_setting.GetGeneralSetting().QuotaDisplayType = operation_setting.QuotaDisplayTypeCNY

	user := &model.User{
		Username: "balance-user",
		Password: "password-placeholder",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusEnabled,
		Group:    "default",
		Quota:    1_250_000,
		AffCode:  "balance-user-aff",
	}
	require.NoError(t, model.DB.Create(user).Error)
	tokens := []model.Token{
		{UserId: user.Id, Key: "balancelimited", Name: "limited", Status: common.TokenStatusEnabled, RemainQuota: 7},
		{UserId: user.Id, Key: "balanceunlimited", Name: "unlimited", Status: common.TokenStatusEnabled, RemainQuota: 999_999_999, UnlimitedQuota: true},
		{UserId: user.Id, Key: "balancedisabled", Name: "disabled", Status: common.TokenStatusDisabled},
	}
	for index := range tokens {
		require.NoError(t, model.DB.Create(&tokens[index]).Error)
	}

	bannedUser := &model.User{
		Username: "banned-balance-user",
		Password: "password-placeholder",
		Role:     common.RoleCommonUser,
		Status:   common.UserStatusDisabled,
		Group:    "default",
		Quota:    5_000_000,
		AffCode:  "banned-balance-user-aff",
	}
	require.NoError(t, model.DB.Create(bannedUser).Error)
	require.NoError(t, model.DB.Create(&model.Token{
		UserId: bannedUser.Id,
		Key:    "balancebanned",
		Name:   "banned user token",
		Status: common.TokenStatusEnabled,
	}).Error)

	router := gin.New()
	router.GET("/api/usage/balance/", middleware.TokenAuthReadOnly(), GetAccountBalance)

	for _, key := range []string{"balancelimited", "balanceunlimited"} {
		recorder := performAccountBalanceRequest(router, key)
		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))

		var payload map[string]any
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
		assert.Len(t, payload, 2)
		assert.Equal(t, true, payload["success"])
		data, ok := payload["data"].(map[string]any)
		require.True(t, ok)
		assert.Len(t, data, 2)
		assert.InDelta(t, 2.5, data["remaining"], 0.000001)
		assert.Equal(t, "CNY", data["unit"])
		assert.NotContains(t, recorder.Body.String(), user.Username)
		assert.NotContains(t, recorder.Body.String(), key)
		assert.NotContains(t, data, "user_id")
		assert.NotContains(t, data, "token_id")
	}

	authTests := []struct {
		name       string
		key        string
		wantStatus int
	}{
		{name: "invalid key", key: "missingbalancekey", wantStatus: http.StatusUnauthorized},
		{name: "disabled key", key: "balancedisabled", wantStatus: http.StatusUnauthorized},
		{name: "banned user", key: "balancebanned", wantStatus: http.StatusForbidden},
	}
	for _, test := range authTests {
		t.Run(test.name, func(t *testing.T) {
			recorder := performAccountBalanceRequest(router, test.key)
			assert.Equal(t, test.wantStatus, recorder.Code)
			assert.Contains(t, recorder.Body.String(), `"success":false`)
			assert.NotContains(t, recorder.Body.String(), test.key)
		})
	}
}

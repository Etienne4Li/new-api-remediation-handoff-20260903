package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestManualCompleteTopUpUsesFrozenCreditedQuotaForCreem(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 750, 0)
	order := &TopUp{
		UserId:             user.Id,
		Amount:             2, // user-facing quantity; not the entitlement in quota units
		Money:              10,
		TradeNo:            "creem-manual-snapshot",
		PaymentMethod:      PaymentMethodCreem,
		PaymentProvider:    PaymentProviderCreem,
		Status:             common.TopUpStatusPending,
		CreateTime:         time.Now().Unix(),
		CreditedQuota:      1234,
		ProviderMerchantID: "creem",
		ProviderOrderName:  "Credits",
		ProviderAmount:     "10.00",
		ProviderProductID:  "prod_credits",
		ProviderCurrency:   "USD",
	}
	require.NoError(t, DB.Create(order).Error)

	require.NoError(t, ManualCompleteTopUp(order.TradeNo, "127.0.0.1"))
	require.Equal(t, 1234, getUserQuotaForPaymentGuardTest(t, user.Id))
	stored := GetTopUpByTradeNo(order.TradeNo)
	require.NotNil(t, stored)
	require.Equal(t, common.TopUpStatusSuccess, stored.Status)
}

func TestManualCompleteTopUpRetainsLegacyFallbackWithoutSnapshot(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 751, 0)
	order := &TopUp{
		UserId:          user.Id,
		Amount:          2,
		Money:           10,
		TradeNo:         "legacy-manual-fallback",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, DB.Create(order).Error)

	require.NoError(t, ManualCompleteTopUp(order.TradeNo, "127.0.0.1"))
	require.Equal(t, 2*int(common.QuotaPerUnit), getUserQuotaForPaymentGuardTest(t, user.Id))
}

func TestManualCompleteTopUpLegacyCreemAmountIsAlreadyQuota(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 752, 0)
	order := &TopUp{
		UserId:          user.Id,
		Amount:          321, // historical Creem rows stored quota directly
		Money:           10,
		TradeNo:         "legacy-creem-manual-fallback",
		PaymentMethod:   PaymentMethodCreem,
		PaymentProvider: PaymentProviderCreem,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, DB.Create(order).Error)

	require.NoError(t, ManualCompleteTopUp(order.TradeNo, "127.0.0.1"))
	require.Equal(t, 321, getUserQuotaForPaymentGuardTest(t, user.Id))
}

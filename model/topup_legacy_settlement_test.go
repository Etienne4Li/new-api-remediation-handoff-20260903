package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLegacyTopUpSettlementsPersistPaymentEvidenceBeforeWalletCredit covers
// all historical Recharge* compatibility entry points.  A full wallet makes
// phase two fail deterministically; phase one must still leave a durable
// paid_uncredited row and a payment-event ledger record.  After an operator
// frees capacity, the same entry point retries the wallet side exactly once.
func TestLegacyTopUpSettlementsPersistPaymentEvidenceBeforeWalletCredit(t *testing.T) {
	testCases := []struct {
		name     string
		provider string
		settle   func(string) error
	}{
		{
			name:     "stripe",
			provider: PaymentProviderStripe,
			settle: func(tradeNo string) error {
				return Recharge(tradeNo, "cus_legacy", "127.0.0.1")
			},
		},
		{
			name:     "creem",
			provider: PaymentProviderCreem,
			settle: func(tradeNo string) error {
				return RechargeCreem(tradeNo, "legacy@example.com", "Legacy User", "127.0.0.1")
			},
		},
		{
			name:     "waffo",
			provider: PaymentProviderWaffo,
			settle: func(tradeNo string) error {
				return RechargeWaffo(tradeNo, "127.0.0.1")
			},
		},
		{
			name:     "waffo pancake",
			provider: PaymentProviderWaffoPancake,
			settle: func(tradeNo string) error {
				return RechargeWaffoPancake(tradeNo)
			},
		},
	}

	for i, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			order := insertRechargeSnapshotTopUpTest(t, "legacy-evidence-"+tc.name, 920+i, tc.provider, 2, 10, 1234)
			user, userErr := GetUserById(order.UserId, false)
			require.NoError(t, userErr)
			require.NotNil(t, user)
			require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).Update("quota", common.MaxWalletQuota-1233).Error)

			err := tc.settle(order.TradeNo)
			require.ErrorIs(t, err, ErrTopUpQuotaLimitExceeded)
			stored := GetTopUpByTradeNo(order.TradeNo)
			require.NotNil(t, stored)
			assert.Equal(t, common.TopUpStatusPaidUncredited, stored.Status)
			assert.Equal(t, TopUpSettlementResolutionCreditRequired, stored.SettlementResolution)
			assert.Equal(t, TopUpSettlementErrorWalletQuota, stored.SettlementErrorCode)
			assert.Equal(t, 1234, stored.CreditedQuota)
			assert.Equal(t, common.MaxWalletQuota-1233, getUserQuotaForPaymentGuardTest(t, user.Id))

			var eventCount int64
			require.NoError(t, DB.Model(&PaymentEvent{}).
				Where("provider = ? AND order_trade_no = ?", tc.provider, order.TradeNo).
				Count(&eventCount).Error)
			assert.Equal(t, int64(1), eventCount)

			// Free capacity and retry through the same compatibility seam.  The
			// prior evidence row is reused; no second event or wallet increment is
			// allowed.
			require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).Update("quota", 0).Error)
			require.NoError(t, tc.settle(order.TradeNo))
			assert.Equal(t, 1234, getUserQuotaForPaymentGuardTest(t, user.Id))
			stored = GetTopUpByTradeNo(order.TradeNo)
			require.NotNil(t, stored)
			assert.Equal(t, common.TopUpStatusSuccess, stored.Status)
			assert.Empty(t, stored.SettlementResolution)
			assert.Empty(t, stored.SettlementErrorCode)
			require.NoError(t, DB.Model(&PaymentEvent{}).
				Where("provider = ? AND order_trade_no = ?", tc.provider, order.TradeNo).
				Count(&eventCount).Error)
			assert.Equal(t, int64(1), eventCount)
		})
	}
}

func TestLegacyTopUpDoesNotClaimModernPaidUncreditedOrder(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 930, 0)
	order := &TopUp{
		UserId:               user.Id,
		Amount:               2,
		Money:                10,
		TradeNo:              "legacy-modern-guard",
		PaymentMethod:        PaymentProviderWaffo,
		PaymentProvider:      PaymentProviderWaffo,
		Status:               common.TopUpStatusPaidUncredited,
		SettlementResolution: TopUpSettlementResolutionCreditRequired,
		SettlementErrorCode:  TopUpSettlementErrorWalletQuota,
		CreateTime:           time.Now().Unix(),
		CreditedQuota:        1234,
		ProviderTradeNo:      stringPtrForLegacyTopUpTest("real-provider-trade"),
		ProviderEventID:      "evt-modern",
		ProviderPayload:      `{"provider":"waffo"}`,
		ProviderMerchantID:   "merchant_test",
		ProviderOrderName:    "Credits",
		ProviderAmount:       "10.00",
		ProviderProductID:    "product_test",
		ProviderCurrency:     "USD",
	}
	require.NoError(t, DB.Create(order).Error)

	err := RechargeWaffo(order.TradeNo, "127.0.0.1")
	require.ErrorIs(t, err, ErrLegacyTopUpEvidenceMissing)
	assert.Equal(t, 0, getUserQuotaForPaymentGuardTest(t, user.Id))
	stored := GetTopUpByTradeNo(order.TradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, common.TopUpStatusPaidUncredited, stored.Status)
	assert.Equal(t, TopUpSettlementResolutionCreditRequired, stored.SettlementResolution)
}

func stringPtrForLegacyTopUpTest(value string) *string {
	return &value
}

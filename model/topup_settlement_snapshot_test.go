package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func insertRechargeSnapshotTopUpTest(t *testing.T, tradeNo string, userID int, provider string, amount int64, money float64, creditedQuota int) *TopUp {
	t.Helper()
	user := insertUserForPaymentGuardTest(t, userID, 0)
	order := &TopUp{
		UserId:             user.Id,
		Amount:             amount,
		Money:              money,
		TradeNo:            tradeNo,
		PaymentMethod:      provider,
		PaymentProvider:    provider,
		Status:             common.TopUpStatusPending,
		CreateTime:         time.Now().Unix(),
		CreditedQuota:      creditedQuota,
		ProviderMerchantID: "merchant_test",
		ProviderOrderName:  "Credits",
		ProviderAmount:     "10.00",
		ProviderProductID:  "product_test",
		ProviderStoreID:    "store_test",
		ProviderCheckoutID: "checkout_test",
		ProviderCurrency:   "USD",
	}
	require.NoError(t, DB.Create(order).Error)
	return order
}

func TestRechargeWaffoUsesFrozenQuotaAndIsIdempotent(t *testing.T) {
	truncateTables(t)
	order := insertRechargeSnapshotTopUpTest(t, "waffo-snapshot", 810, PaymentProviderWaffo, 2, 10, 1234)

	require.NoError(t, RechargeWaffo(order.TradeNo, "127.0.0.1"))
	assert.Equal(t, 1234, getUserQuotaForPaymentGuardTest(t, order.UserId))
	assert.Equal(t, common.TopUpStatusSuccess, GetTopUpByTradeNo(order.TradeNo).Status)

	// A provider retry must not apply the frozen delta a second time.
	require.NoError(t, RechargeWaffo(order.TradeNo, "127.0.0.1"))
	assert.Equal(t, 1234, getUserQuotaForPaymentGuardTest(t, order.UserId))
}

func TestRechargeWaffoPancakeUsesFrozenQuotaAndIsIdempotent(t *testing.T) {
	truncateTables(t)
	order := insertRechargeSnapshotTopUpTest(t, "pancake-snapshot", 811, PaymentProviderWaffoPancake, 2, 10, 2345)

	require.NoError(t, RechargeWaffoPancake(order.TradeNo))
	assert.Equal(t, 2345, getUserQuotaForPaymentGuardTest(t, order.UserId))
	assert.Equal(t, common.TopUpStatusSuccess, GetTopUpByTradeNo(order.TradeNo).Status)

	require.NoError(t, RechargeWaffoPancake(order.TradeNo))
	assert.Equal(t, 2345, getUserQuotaForPaymentGuardTest(t, order.UserId))
}

func TestRechargeCreemUsesFrozenQuotaAndIsIdempotent(t *testing.T) {
	truncateTables(t)
	order := insertRechargeSnapshotTopUpTest(t, "creem-snapshot", 812, PaymentProviderCreem, 2, 10, 3456)

	require.NoError(t, RechargeCreem(order.TradeNo, "", "", "127.0.0.1"))
	assert.Equal(t, 3456, getUserQuotaForPaymentGuardTest(t, order.UserId))
	assert.Equal(t, common.TopUpStatusSuccess, GetTopUpByTradeNo(order.TradeNo).Status)

	// Creem retries use the same successful local order and must be harmless.
	require.NoError(t, RechargeCreem(order.TradeNo, "", "", "127.0.0.1"))
	assert.Equal(t, 3456, getUserQuotaForPaymentGuardTest(t, order.UserId))
}

func TestRechargeStripeUsesFrozenQuotaAndIsIdempotent(t *testing.T) {
	truncateTables(t)
	order := insertRechargeSnapshotTopUpTest(t, "stripe-snapshot", 813, PaymentProviderStripe, 2, 99, 4567)

	require.NoError(t, Recharge(order.TradeNo, "cus_test", "127.0.0.1"))
	assert.Equal(t, 4567, getUserQuotaForPaymentGuardTest(t, order.UserId))
	assert.Equal(t, common.TopUpStatusSuccess, GetTopUpByTradeNo(order.TradeNo).Status)

	require.NoError(t, Recharge(order.TradeNo, "cus_test", "127.0.0.1"))
	assert.Equal(t, 4567, getUserQuotaForPaymentGuardTest(t, order.UserId))
}

func TestTopUpQuotaForSettlementRequiresEmptyLegacySnapshot(t *testing.T) {
	oldQuotaPerUnit := common.QuotaPerUnit
	common.QuotaPerUnit = 5
	t.Cleanup(func() { common.QuotaPerUnit = oldQuotaPerUnit })

	legacy := &TopUp{PaymentProvider: PaymentProviderWaffo, Amount: 2}
	quota, err := topUpQuotaForSettlement(legacy, PaymentProviderWaffo)
	require.NoError(t, err)
	assert.Equal(t, 10, quota)

	partial := &TopUp{
		PaymentProvider: PaymentProviderWaffo,
		Amount:          2,
		ProviderAmount:  "10.00",
	}
	_, err = topUpQuotaForSettlement(partial, PaymentProviderWaffo)
	require.ErrorIs(t, err, ErrProviderSnapshotMissing)
}

func TestTopUpQuotaForSettlementRejectsSnapshotAndLegacyOverflow(t *testing.T) {
	modernOverflow := &TopUp{
		PaymentProvider: PaymentProviderCreem,
		CreditedQuota:   common.MaxWalletQuota + 1,
	}
	_, err := topUpQuotaForSettlement(modernOverflow, PaymentProviderCreem)
	require.ErrorIs(t, err, ErrInvalidTopUpQuota)

	legacyOverflow := &TopUp{
		PaymentProvider: PaymentProviderWaffo,
		Amount:          common.MaxWalletQuota,
	}
	_, err = topUpQuotaForSettlement(legacyOverflow, PaymentProviderWaffo)
	require.ErrorIs(t, err, ErrInvalidTopUpQuota)
}

func TestRetryPaidUncreditedTopUpDoesNotOverrideRefundResolution(t *testing.T) {
	truncateTables(t)
	order := insertRechargeSnapshotTopUpTest(t, "topup-refund-resolution", 814, PaymentProviderWaffo, 2, 10, 1234)
	// Simulate a provider-authenticated payment whose deterministic failure was
	// classified as requiring a refund.  A later credit retry must not turn that
	// operator decision into a wallet mutation.
	require.NoError(t, DB.Model(&TopUp{}).Where("id = ?", order.Id).Updates(map[string]interface{}{
		"status":                common.TopUpStatusPaidUncredited,
		"settlement_resolution": TopUpSettlementResolutionRefundRequired,
		"settlement_error_code": TopUpSettlementErrorUserNotFound,
	}).Error)

	err := RetryPaidUncreditedTopUp(order.TradeNo)
	require.ErrorIs(t, err, ErrTopUpSettlementResolutionInvalid)
	assert.Equal(t, 0, getUserQuotaForPaymentGuardTest(t, order.UserId))
	stored := GetTopUpByTradeNo(order.TradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, common.TopUpStatusPaidUncredited, stored.Status)
	assert.Equal(t, TopUpSettlementResolutionRefundRequired, stored.SettlementResolution)
}

func TestRetryPaidUncreditedTopUpAllowsCreditPendingResolution(t *testing.T) {
	truncateTables(t)
	order := insertRechargeSnapshotTopUpTest(t, "topup-credit-pending-resolution", 815, PaymentProviderWaffo, 2, 10, 1234)
	require.NoError(t, DB.Model(&TopUp{}).Where("id = ?", order.Id).Updates(map[string]interface{}{
		"status":                common.TopUpStatusPaidUncredited,
		"settlement_resolution": TopUpSettlementResolutionCreditPending,
	}).Error)

	require.NoError(t, RetryPaidUncreditedTopUp(order.TradeNo))
	assert.Equal(t, 1234, getUserQuotaForPaymentGuardTest(t, order.UserId))
	stored := GetTopUpByTradeNo(order.TradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, common.TopUpStatusSuccess, stored.Status)
	assert.Empty(t, stored.SettlementResolution)
}

func TestRetryPaidUncreditedTopUpRejectsUnknownResolution(t *testing.T) {
	truncateTables(t)
	order := insertRechargeSnapshotTopUpTest(t, "topup-unknown-resolution", 816, PaymentProviderWaffo, 2, 10, 1234)
	require.NoError(t, DB.Model(&TopUp{}).Where("id = ?", order.Id).Updates(map[string]interface{}{
		"status":                common.TopUpStatusPaidUncredited,
		"settlement_resolution": "manual_reconciliation",
	}).Error)

	err := RetryPaidUncreditedTopUp(order.TradeNo)
	require.ErrorIs(t, err, ErrTopUpSettlementResolutionInvalid)
	assert.Equal(t, 0, getUserQuotaForPaymentGuardTest(t, order.UserId))
}

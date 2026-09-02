package model

import (
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func insertSnapshotEpayTopUp(t *testing.T, tradeNo string, userID int) *TopUp {
	t.Helper()
	topUp := &TopUp{
		UserId:                 userID,
		Amount:                 2,
		Money:                  10,
		TradeNo:                tradeNo,
		PaymentMethod:          "alipay",
		PaymentProvider:        PaymentProviderEpay,
		Status:                 common.TopUpStatusPending,
		CreateTime:             time.Now().Unix(),
		CreditedQuota:          100,
		ProviderMerchantID:     "merchant-1",
		ProviderOrderName:      "TUC2",
		ProviderAmount:         "10.00",
		ProviderKeyFingerprint: EpayKeyFingerprint("test-key"),
	}
	require.NoError(t, DB.Create(topUp).Error)
	return topUp
}

func snapshotEpayCallback(tradeNo string) EpayCallback {
	return EpayCallback{
		ServiceTradeNo:          tradeNo,
		ProviderTradeNo:         "provider-trade-1",
		PaymentMethod:           "alipay",
		Name:                    "TUC2",
		Money:                   "10.00",
		MerchantID:              "merchant-1",
		TradeStatus:             "TRADE_SUCCESS",
		RawPayload:              `{"trade_status":"TRADE_SUCCESS"}`,
		SignatureKeyFingerprint: EpayKeyFingerprint("test-key"),
	}
}

func loadGuardUserQuota(t *testing.T, userID int) int {
	t.Helper()
	var user User
	require.NoError(t, DB.Select("quota").Where("id = ?", userID).First(&user).Error)
	return user.Quota
}

func TestRechargeEpayVerifiedBindsSnapshotAndIsIdempotent(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 601, 0)
	order := insertSnapshotEpayTopUp(t, "epay-snapshot-once", user.Id)

	alreadyDone, err := RechargeEpayVerified(snapshotEpayCallback(order.TradeNo), "127.0.0.1")
	require.NoError(t, err)
	assert.False(t, alreadyDone)
	assert.Equal(t, 100, loadGuardUserQuota(t, user.Id))

	var event PaymentEvent
	require.NoError(t, DB.Where("provider = ? AND provider_trade_no = ?", PaymentProviderEpay, "provider-trade-1").First(&event).Error)
	assert.Equal(t, order.TradeNo, event.OrderTradeNo)
	assert.Equal(t, PaymentEventOrderTopUp, event.OrderKind)

	alreadyDone, err = RechargeEpayVerified(snapshotEpayCallback(order.TradeNo), "127.0.0.1")
	require.NoError(t, err)
	assert.True(t, alreadyDone)
	assert.Equal(t, 100, loadGuardUserQuota(t, user.Id))
}

func TestRechargeEpayVerifiedRejectsCallbackSnapshotMismatches(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(*EpayCallback)
		want   error
	}{
		{name: "amount", mutate: func(callback *EpayCallback) { callback.Money = "9.99" }, want: ErrEpayCallbackMismatch},
		{name: "merchant", mutate: func(callback *EpayCallback) { callback.MerchantID = "other-merchant" }, want: ErrEpayCallbackMismatch},
		{name: "product", mutate: func(callback *EpayCallback) { callback.Name = "TUC999" }, want: ErrEpayCallbackMismatch},
		{name: "payment method", mutate: func(callback *EpayCallback) { callback.PaymentMethod = "wxpay" }, want: ErrPaymentMethodMismatch},
		{name: "provider transaction", mutate: func(callback *EpayCallback) { callback.ProviderTradeNo = "provider-trade-2" }, want: ErrEpayProviderTradeConflict},
		{name: "fractional precision", mutate: func(callback *EpayCallback) { callback.Money = "10.001" }, want: ErrEpayCallbackMismatch},
	}

	for index, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			truncateTables(t)
			user := insertUserForPaymentGuardTest(t, 610+index, 0)
			order := insertSnapshotEpayTopUp(t, "epay-snapshot-mismatch-"+tc.name, user.Id)
			callback := snapshotEpayCallback(order.TradeNo)
			if tc.name == "provider transaction" {
				bound := "provider-trade-1"
				order.ProviderTradeNo = &bound
				require.NoError(t, DB.Save(order).Error)
			}
			tc.mutate(&callback)

			alreadyDone, err := RechargeEpayVerified(callback, "127.0.0.1")
			require.ErrorIs(t, err, tc.want)
			assert.False(t, alreadyDone)
			assert.Equal(t, 0, loadGuardUserQuota(t, user.Id))
			assert.Equal(t, common.TopUpStatusPending, getTopUpStatusForPaymentGuardTest(t, order.TradeNo))
		})
	}
}

func TestRechargeEpayVerifiedRejectsLegacyOrderWithoutSnapshot(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 620, 0)
	legacy := &TopUp{
		UserId:          user.Id,
		Amount:          2,
		Money:           10,
		TradeNo:         "epay-legacy-no-snapshot",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, DB.Create(legacy).Error)

	callback := snapshotEpayCallback(legacy.TradeNo)
	_, err := RechargeEpayVerified(callback, "127.0.0.1")
	require.ErrorIs(t, err, ErrEpayOrderSnapshotMissing)
	assert.Equal(t, 0, loadGuardUserQuota(t, user.Id))
	assert.Equal(t, common.TopUpStatusPending, getTopUpStatusForPaymentGuardTest(t, legacy.TradeNo))

	// The old trusted internal seam remains available for an operator/manual
	// completion flow; it derives all callback fields from the local order.
	_, err = RechargeEpay(legacy.TradeNo, "alipay", "127.0.0.1")
	require.NoError(t, err)
	assert.Equal(t, 2*int(common.QuotaPerUnit), loadGuardUserQuota(t, user.Id))
}

func TestRechargeEpayDoesNotFallbackForPartialSnapshot(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 621, 0)
	partial := &TopUp{
		UserId:             user.Id,
		Amount:             2,
		Money:              10,
		TradeNo:            "epay-partial-snapshot",
		PaymentMethod:      "alipay",
		PaymentProvider:    PaymentProviderEpay,
		Status:             common.TopUpStatusPending,
		CreateTime:         time.Now().Unix(),
		ProviderMerchantID: "merchant-written-before-crash",
	}
	require.NoError(t, DB.Create(partial).Error)

	_, err := RechargeEpay(partial.TradeNo, "alipay", "127.0.0.1")
	require.ErrorIs(t, err, ErrEpayOrderSnapshotMissing)
	assert.Equal(t, 0, loadGuardUserQuota(t, user.Id))
	assert.Equal(t, common.TopUpStatusPending, getTopUpStatusForPaymentGuardTest(t, partial.TradeNo))
}

func TestRechargeEpayVerifiedAcknowledgesLegacySuccessfulOrderWithoutCreditingAgain(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 625, 321)
	legacy := &TopUp{
		UserId:          user.Id,
		Amount:          2,
		Money:           10,
		TradeNo:         "epay-legacy-success-no-snapshot",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		Status:          common.TopUpStatusSuccess,
		CreateTime:      time.Now().Add(-time.Hour).Unix(),
		CompleteTime:    time.Now().Add(-30 * time.Minute).Unix(),
	}
	require.NoError(t, DB.Create(legacy).Error)
	t.Setenv("EPAY_LEGACY_SUCCESS_ACK_TRADE_NOS", legacy.TradeNo)

	alreadyDone, err := RechargeEpayVerified(snapshotEpayCallback(legacy.TradeNo), "127.0.0.1")
	require.NoError(t, err)
	assert.True(t, alreadyDone)
	assert.Equal(t, 321, loadGuardUserQuota(t, user.Id))

	stored := GetTopUpByTradeNo(legacy.TradeNo)
	require.NotNil(t, stored)
	assert.Nil(t, stored.ProviderTradeNo, "a callback cannot prove the original provider transaction for a legacy order")
	assert.Zero(t, stored.CreditedQuota)

	var eventCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).Count(&eventCount).Error)
	assert.Zero(t, eventCount, "legacy acknowledgement must remain read-only")
}

func TestRechargeEpayVerifiedAcknowledgesMismatchedAllowlistedLegacySuccessWithoutWriting(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 626, 321)
	legacy := &TopUp{
		UserId:          user.Id,
		Amount:          2,
		Money:           10,
		TradeNo:         "epay-legacy-success-mismatch",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		Status:          common.TopUpStatusSuccess,
		CreateTime:      time.Now().Add(-time.Hour).Unix(),
		CompleteTime:    time.Now().Add(-30 * time.Minute).Unix(),
	}
	require.NoError(t, DB.Create(legacy).Error)
	t.Setenv("EPAY_LEGACY_SUCCESS_ACK_TRADE_NOS", legacy.TradeNo)

	callback := snapshotEpayCallback(legacy.TradeNo)
	callback.Money = "9.99"
	alreadyDone, err := RechargeEpayVerified(callback, "127.0.0.1")
	require.NoError(t, err)
	assert.True(t, alreadyDone)
	assert.Equal(t, 321, loadGuardUserQuota(t, user.Id))

	var eventCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).Count(&eventCount).Error)
	assert.Zero(t, eventCount)
}

func TestCompleteSubscriptionOrderEpayAcknowledgesLegacySuccessWithoutGrantingAgain(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 627, 0)
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 628)
	legacy := &SubscriptionOrder{
		UserId:          user.Id,
		PlanId:          plan.Id,
		Money:           10,
		TradeNo:         "epay-legacy-subscription-success",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		Status:          common.TopUpStatusSuccess,
		CreateTime:      time.Now().Add(-time.Hour).Unix(),
		CompleteTime:    time.Now().Add(-30 * time.Minute).Unix(),
	}
	require.NoError(t, DB.Create(legacy).Error)
	t.Setenv("EPAY_LEGACY_SUCCESS_ACK_TRADE_NOS", legacy.TradeNo)

	require.NoError(t, CompleteSubscriptionOrderEpay(snapshotEpayCallback(legacy.TradeNo)))
	assert.Zero(t, countUserSubscriptionsForPaymentGuardTest(t, user.Id))

	stored := GetSubscriptionOrderByTradeNo(legacy.TradeNo)
	require.NotNil(t, stored)
	assert.Nil(t, stored.ProviderTradeNo, "a callback cannot prove the original provider transaction for a legacy order")

	var eventCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).Count(&eventCount).Error)
	assert.Zero(t, eventCount, "legacy acknowledgement must remain read-only")
}

func TestCompleteSubscriptionOrderEpayRejectsLegacyPendingOrderWithoutSnapshot(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 629, 0)
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 630)
	legacy := &SubscriptionOrder{
		UserId:          user.Id,
		PlanId:          plan.Id,
		Money:           10,
		TradeNo:         "epay-legacy-subscription-pending",
		PaymentMethod:   "alipay",
		PaymentProvider: PaymentProviderEpay,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Add(-time.Hour).Unix(),
	}
	require.NoError(t, DB.Create(legacy).Error)
	t.Setenv("EPAY_LEGACY_SUCCESS_ACK_TRADE_NOS", legacy.TradeNo)

	err := CompleteSubscriptionOrderEpay(snapshotEpayCallback(legacy.TradeNo))
	require.ErrorIs(t, err, ErrEpayOrderSnapshotMissing)
	assert.Zero(t, countUserSubscriptionsForPaymentGuardTest(t, user.Id))

	var eventCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).Count(&eventCount).Error)
	assert.Zero(t, eventCount)
}

func TestRechargeEpayVerifiedRejectsPreviousKeyForNewKeyOrder(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 631, 0)
	order := insertSnapshotEpayTopUp(t, "epay-previous-key-new-order", user.Id)
	order.ProviderKeyFingerprint = EpayKeyFingerprint("new-key")
	require.NoError(t, DB.Save(order).Error)

	callback := snapshotEpayCallback(order.TradeNo)
	callback.SignatureKeyFingerprint = EpayKeyFingerprint("old-key")
	callback.SignatureUsedPreviousKey = true

	alreadyDone, err := RechargeEpayVerified(callback, "127.0.0.1")
	require.ErrorIs(t, err, ErrEpayCallbackMismatch)
	assert.False(t, alreadyDone)
	assert.Equal(t, 0, loadGuardUserQuota(t, user.Id))
	assert.Equal(t, common.TopUpStatusPending, getTopUpStatusForPaymentGuardTest(t, order.TradeNo))
}

func TestRechargeEpayVerifiedAcceptsCurrentKeyForPreRotationSnapshot(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 632, 0)
	order := insertSnapshotEpayTopUp(t, "epay-current-key-old-order", user.Id)
	order.ProviderKeyFingerprint = EpayKeyFingerprint("old-key")
	require.NoError(t, DB.Save(order).Error)

	callback := snapshotEpayCallback(order.TradeNo)
	callback.SignatureKeyFingerprint = EpayKeyFingerprint("new-key")

	alreadyDone, err := RechargeEpayVerified(callback, "127.0.0.1")
	require.NoError(t, err)
	assert.False(t, alreadyDone)
	assert.Equal(t, 100, loadGuardUserQuota(t, user.Id))
}

func TestEpayProviderTradeCannotBeReusedAcrossTopUps(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 630, 0)
	first := insertSnapshotEpayTopUp(t, "epay-provider-first", user.Id)
	second := insertSnapshotEpayTopUp(t, "epay-provider-second", user.Id)

	callback := snapshotEpayCallback(first.TradeNo)
	_, err := RechargeEpayVerified(callback, "127.0.0.1")
	require.NoError(t, err)

	callback.ServiceTradeNo = second.TradeNo
	_, err = RechargeEpayVerified(callback, "127.0.0.1")
	require.ErrorIs(t, err, ErrEpayProviderTradeConflict)
	assert.Equal(t, common.TopUpStatusPending, getTopUpStatusForPaymentGuardTest(t, second.TradeNo))
	assert.Equal(t, 100, loadGuardUserQuota(t, user.Id))
}

func TestRechargeEpayVerifiedRejectsDifferentCallbackForSuccessfulOrder(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 640, 0)
	order := insertSnapshotEpayTopUp(t, "epay-success-mismatch", user.Id)
	callback := snapshotEpayCallback(order.TradeNo)
	_, err := RechargeEpayVerified(callback, "127.0.0.1")
	require.NoError(t, err)

	callback.ProviderTradeNo = "provider-trade-other"
	_, err = RechargeEpayVerified(callback, "127.0.0.1")
	require.ErrorIs(t, err, ErrEpayProviderTradeConflict)
	callback.ProviderTradeNo = "provider-trade-1"
	callback.Money = "11.00"
	_, err = RechargeEpayVerified(callback, "127.0.0.1")
	require.ErrorIs(t, err, ErrEpayCallbackMismatch)
	assert.Equal(t, 100, loadGuardUserQuota(t, user.Id))
}

func TestBindPaymentEventUsesExistingBindingAcrossOrderKinds(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Create(&PaymentEvent{
		Provider:        PaymentProviderEpay,
		ProviderTradeNo: "provider-cross-kind",
		OrderTradeNo:    "topup-order",
		OrderKind:       PaymentEventOrderTopUp,
		CreateTime:      common.GetTimestamp(),
	}).Error)

	err := DB.Transaction(func(tx *gorm.DB) error {
		return bindPaymentEventTx(tx, PaymentProviderEpay, "provider-cross-kind", "subscription-order", PaymentEventOrderSubscription, "")
	})
	require.ErrorIs(t, err, ErrEpayProviderTradeConflict)
}

func TestEpayCallbackValidationRejectsMissingProviderTradeNumber(t *testing.T) {
	callback := snapshotEpayCallback("order")
	callback.ProviderTradeNo = ""
	require.ErrorIs(t, callback.ValidateForController(), ErrEpayCallbackInvalid)
	assert.True(t, errors.Is(callback.ValidateForController(), ErrEpayCallbackInvalid))
}

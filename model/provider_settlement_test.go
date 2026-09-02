package model

import (
	"fmt"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func providerSettlementScopeFingerprint() string {
	fingerprint, _ := ProviderPaymentScopeFingerprint(PaymentProviderStripe, "acct_test", "test")
	return fingerprint
}

func providerSettlementTestOrder(t *testing.T, tradeNo string, userID int) *TopUp {
	t.Helper()
	user := &User{Id: userID, Username: fmt.Sprintf("provider-settlement-user-%d", userID), AffCode: fmt.Sprintf("provider-settlement-%d", userID), Status: common.UserStatusEnabled, Quota: 0}
	require.NoError(t, DB.Create(user).Error)
	order := &TopUp{
		UserId:                 userID,
		Amount:                 10,
		Money:                  10,
		TradeNo:                tradeNo,
		PaymentMethod:          PaymentMethodStripe,
		PaymentProvider:        PaymentProviderStripe,
		Status:                 common.TopUpStatusPending,
		CreateTime:             time.Now().Unix(),
		CreditedQuota:          500,
		ProviderMerchantID:     "acct_test",
		ProviderOrderName:      "Credits",
		ProviderAmount:         "10.00",
		ProviderProductID:      "price_credits",
		ProviderCheckoutID:     "cs_test_123",
		ProviderCurrency:       "USD",
		ProviderKeyFingerprint: providerSettlementScopeFingerprint(),
	}
	require.NoError(t, DB.Create(order).Error)
	return order
}

func validProviderSettlement(tradeNo string) ProviderSettlement {
	return ProviderSettlement{
		OrderTradeNo:           tradeNo,
		Provider:               PaymentProviderStripe,
		ProviderTradeNo:        "cs_test_123",
		ProviderEventID:        "evt_test_123",
		ProviderAccountID:      "acct_test",
		ProviderEnvironment:    "test",
		ProviderKeyFingerprint: providerSettlementScopeFingerprint(),
		ProviderSubscriptionID: "sub_test_123",
		MerchantID:             "acct_test",
		ProductID:              "price_credits",
		CheckoutID:             "cs_test_123",
		Currency:               "usd",
		Amount:                 "10.00",
		OrderName:              "Credits",
		Payload:                `{"id":"evt_test_123"}`,
		PaymentObjects: []ProviderPaymentObject{
			{ObjectType: ProviderPaymentObjectCheckoutSession, ObjectID: "cs_test_123"},
			{ObjectType: ProviderPaymentObjectPaymentIntent, ObjectID: "pi_test_123"},
		},
	}
}

// clearProviderSettlementRows isolates sub-cases immediately. The shared
// truncateTables helper registers cleanup via t.Cleanup; when called on a
// parent test, that cleanup runs only after all subtests and would make each
// case reuse the same provider transaction.
func clearProviderSettlementRows(t *testing.T) {
	t.Helper()
	require.NoError(t, DB.Where("order_trade_no LIKE ?", "provider-topup-%").Delete(&PaymentEvent{}).Error)
	require.NoError(t, DB.Where("order_trade_no LIKE ?", "provider-topup-%").Delete(&ProviderPaymentBinding{}).Error)
	require.NoError(t, DB.Where("trade_no LIKE ?", "provider-topup-%").Delete(&TopUp{}).Error)
	require.NoError(t, DB.Where("trade_no LIKE ?", "provider-topup-%").Delete(&SubscriptionOrder{}).Error)
}

func TestSettleTopUpProviderDoesNotBindLegacyStripeOrderWithoutCheckoutID(t *testing.T) {
	truncateTables(t)
	order := providerSettlementTestOrder(t, "provider-topup-missing-checkout", 731)
	require.NoError(t, DB.Model(&TopUp{}).Where("id = ?", order.Id).Update("provider_checkout_id", "").Error)

	settlement := validProviderSettlement(order.TradeNo)
	_, err := SettleTopUpProvider(settlement, "127.0.0.1", nil)
	require.ErrorIs(t, err, ErrProviderSnapshotMissing)
	assert.Equal(t, 0, loadGuardUserQuota(t, order.UserId))
	assert.Equal(t, common.TopUpStatusPending, GetTopUpByTradeNo(order.TradeNo).Status)

	var eventCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).Where("order_trade_no = ?", order.TradeNo).Count(&eventCount).Error)
	assert.Zero(t, eventCount)
	var bindingCount int64
	require.NoError(t, DB.Model(&ProviderPaymentBinding{}).Where("order_trade_no = ?", order.TradeNo).Count(&bindingCount).Error)
	assert.Zero(t, bindingCount)
}

func TestSettleTopUpProviderChecksSnapshotAndIsIdempotent(t *testing.T) {
	truncateTables(t)
	order := providerSettlementTestOrder(t, "provider-topup-once", 710)

	result, err := SettleTopUpProvider(validProviderSettlement(order.TradeNo), "127.0.0.1", nil)
	require.NoError(t, err)
	assert.False(t, result.AlreadyCompleted)
	assert.Equal(t, 500, result.CreditedQuota)
	assert.Equal(t, 500, loadGuardUserQuota(t, order.UserId))

	result, err = SettleTopUpProvider(validProviderSettlement(order.TradeNo), "127.0.0.1", nil)
	require.NoError(t, err)
	assert.True(t, result.AlreadyCompleted)
	assert.Equal(t, 500, loadGuardUserQuota(t, order.UserId))

	stored := GetTopUpByTradeNo(order.TradeNo)
	require.NotNil(t, stored)
	require.NotNil(t, stored.ProviderTradeNo)
	assert.Equal(t, "cs_test_123", *stored.ProviderTradeNo)
	assert.Equal(t, "evt_test_123", stored.ProviderEventID)
	assert.Equal(t, "sub_test_123", stored.ProviderSubscriptionID)
	var eventCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).Where("provider = ?", PaymentProviderStripe).Count(&eventCount).Error)
	assert.Equal(t, int64(1), eventCount)
	var bindingCount int64
	require.NoError(t, DB.Model(&ProviderPaymentBinding{}).Where("order_trade_no = ?", order.TradeNo).Count(&bindingCount).Error)
	assert.EqualValues(t, 2, bindingCount)
}

func TestSettleTopUpProviderRollsBackCreditWhenPaymentAliasConflicts(t *testing.T) {
	truncateTables(t)
	order := providerSettlementTestOrder(t, "provider-topup-binding-conflict", 711)
	require.NoError(t, BindProviderPaymentBindings(ProviderPaymentBindingBatch{
		Provider:            PaymentProviderStripe,
		ProviderAccountID:   "acct_test",
		ProviderEnvironment: "test",
		OrderTradeNo:        "different-order",
		OrderKind:           PaymentEventOrderSubscription,
		Objects: []ProviderPaymentObject{{
			ObjectType: ProviderPaymentObjectCharge,
			ObjectID:   "ch_conflict",
		}},
	}))

	settlement := validProviderSettlement(order.TradeNo)
	settlement.PaymentObjects = []ProviderPaymentObject{
		{ObjectType: ProviderPaymentObjectCheckoutSession, ObjectID: settlement.CheckoutID},
		{ObjectType: ProviderPaymentObjectInvoice, ObjectID: "in_should_rollback"},
		{ObjectType: ProviderPaymentObjectCharge, ObjectID: "ch_conflict"},
	}
	_, err := SettleTopUpProvider(settlement, "127.0.0.1", nil)
	require.ErrorIs(t, err, ErrProviderPaymentBindingConflict)
	assert.Equal(t, 0, loadGuardUserQuota(t, order.UserId))
	assert.Equal(t, common.TopUpStatusPending, GetTopUpByTradeNo(order.TradeNo).Status)

	var eventCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).Where("provider_trade_no = ?", settlement.ProviderTradeNo).Count(&eventCount).Error)
	assert.Zero(t, eventCount)
	var freshAliasCount int64
	require.NoError(t, DB.Model(&ProviderPaymentBinding{}).Where("object_id = ?", "in_should_rollback").Count(&freshAliasCount).Error)
	assert.Zero(t, freshAliasCount)
}

func TestSettleTopUpProviderRejectsMismatchedReplayAfterSuccess(t *testing.T) {
	truncateTables(t)
	order := providerSettlementTestOrder(t, "provider-topup-replay-snapshot", 715)

	_, err := SettleTopUpProvider(validProviderSettlement(order.TradeNo), "127.0.0.1", nil)
	require.NoError(t, err)
	assert.Equal(t, 500, loadGuardUserQuota(t, order.UserId))

	// A replay carrying the same provider transaction but different economic
	// evidence must not be accepted by the already-successful idempotency
	// branch. This is the exact case where checking only ProviderTradeNo would
	// turn a malformed/provider-confused event into an acknowledged success.
	for name, mutate := range map[string]func(*ProviderSettlement){
		"amount":   func(s *ProviderSettlement) { s.Amount = "9.00" },
		"currency": func(s *ProviderSettlement) { s.Currency = "EUR" },
		"product":  func(s *ProviderSettlement) { s.ProductID = "price_other" },
		"merchant": func(s *ProviderSettlement) { s.MerchantID = "acct_other" },
		"environment": func(s *ProviderSettlement) {
			s.ProviderEnvironment = "live"
			s.ProviderKeyFingerprint, _ = ProviderPaymentScopeFingerprint(PaymentProviderStripe, "acct_test", "live")
		},
	} {
		t.Run(name, func(t *testing.T) {
			settlement := validProviderSettlement(order.TradeNo)
			mutate(&settlement)
			_, replayErr := SettleTopUpProvider(settlement, "127.0.0.1", nil)
			require.ErrorIs(t, replayErr, ErrProviderSnapshotMismatch)
			assert.Equal(t, 500, loadGuardUserQuota(t, order.UserId))
		})
	}
}

func TestSettleTopUpProviderRejectsSnapshotMismatchAndLegacyRows(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(*ProviderSettlement)
		want   error
	}{
		{name: "amount", mutate: func(s *ProviderSettlement) { s.Amount = "9.99" }, want: ErrProviderSnapshotMismatch},
		{name: "currency", mutate: func(s *ProviderSettlement) { s.Currency = "EUR" }, want: ErrProviderSnapshotMismatch},
		{name: "product", mutate: func(s *ProviderSettlement) { s.ProductID = "price_other" }, want: ErrProviderSnapshotMismatch},
		{name: "merchant", mutate: func(s *ProviderSettlement) { s.MerchantID = "acct_other" }, want: ErrProviderSnapshotMismatch},
		{name: "environment", mutate: func(s *ProviderSettlement) {
			s.ProviderEnvironment = "live"
			s.ProviderKeyFingerprint, _ = ProviderPaymentScopeFingerprint(PaymentProviderStripe, "acct_test", "live")
		}, want: ErrProviderSnapshotMismatch},
	}
	for i, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			clearProviderSettlementRows(t)
			order := providerSettlementTestOrder(t, "provider-topup-mismatch-"+tc.name, 720+i)
			settlement := validProviderSettlement(order.TradeNo)
			tc.mutate(&settlement)
			_, err := SettleTopUpProvider(settlement, "127.0.0.1", nil)
			require.ErrorIs(t, err, tc.want)
			assert.Equal(t, 0, loadGuardUserQuota(t, order.UserId))
			assert.Equal(t, common.TopUpStatusPending, GetTopUpByTradeNo(order.TradeNo).Status)
		})
	}
	// Once a transaction has settled an order, a different provider payment
	// object must never be accepted as an idempotent retry.
	clearProviderSettlementRows(t)
	settled := providerSettlementTestOrder(t, "provider-topup-transaction-conflict", 729)
	_, err := SettleTopUpProvider(validProviderSettlement(settled.TradeNo), "127.0.0.1", nil)
	require.NoError(t, err)
	conflicting := validProviderSettlement(settled.TradeNo)
	conflicting.ProviderTradeNo = "pi_other"
	_, err = SettleTopUpProvider(conflicting, "127.0.0.1", nil)
	require.ErrorIs(t, err, ErrProviderEventConflict)

	clearProviderSettlementRows(t)
	user := &User{Id: 730, Username: "provider-settlement-legacy", AffCode: "provider-settlement-730", Status: common.UserStatusEnabled}
	require.NoError(t, DB.Create(user).Error)
	legacy := &TopUp{UserId: user.Id, Amount: 10, Money: 10, TradeNo: "provider-topup-legacy", PaymentMethod: PaymentMethodStripe, PaymentProvider: PaymentProviderStripe, Status: common.TopUpStatusPending, CreateTime: time.Now().Unix()}
	require.NoError(t, DB.Create(legacy).Error)
	_, err = SettleTopUpProvider(validProviderSettlement(legacy.TradeNo), "127.0.0.1", nil)
	require.ErrorIs(t, err, ErrProviderSnapshotMissing)
	assert.Equal(t, 0, loadGuardUserQuota(t, user.Id))
	var legacyBindingCount int64
	require.NoError(t, DB.Model(&ProviderPaymentBinding{}).Where("order_trade_no = ?", legacy.TradeNo).Count(&legacyBindingCount).Error)
	assert.Zero(t, legacyBindingCount)
}

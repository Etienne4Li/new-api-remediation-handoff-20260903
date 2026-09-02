package model

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func providerRefundInput(deliveryID, eventType, orderTradeNo string) ProviderRefundEventInput {
	status := "succeeded"
	if eventType == "refund.failed" {
		status = "failed"
	}
	return ProviderRefundEventInput{
		Provider:                       PaymentProviderWaffoPancake,
		DeliveryID:                     deliveryID,
		BusinessEventID:                "payment-" + deliveryID,
		RefundTicketMerchantExternalID: "refund-ticket-" + deliveryID,
		OrderTradeNo:                   orderTradeNo,
		EventType:                      eventType,
		RefundStatus:                   status,
		Reason:                         "customer requested refund",
		Payload:                        "{\"delivery\":\"" + deliveryID + "\"}",
	}
}

func TestProviderRefundEventScopedKeyNormalizesProviderCase(t *testing.T) {
	assert.Equal(t,
		ProviderRefundEventScopedKey("stripe", "acct_1", "test", "evt_1"),
		ProviderRefundEventScopedKey(" STRIPE ", "acct_1", "test", "evt_1"),
	)
}

func TestProcessProviderRefundEventRejectsOversizedPersistedIdentifiers(t *testing.T) {
	truncateTables(t)
	tests := []struct {
		name   string
		mutate func(*ProviderRefundEventInput)
	}{
		{name: "business event id", mutate: func(input *ProviderRefundEventInput) {
			input.BusinessEventID = strings.Repeat("b", 256)
		}},
		{name: "refund ticket id", mutate: func(input *ProviderRefundEventInput) {
			input.RefundTicketMerchantExternalID = strings.Repeat("r", 129)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := providerRefundInput("delivery-oversized-"+tt.name, "refund.failed", "order-unused")
			tt.mutate(&input)
			_, err := ProcessProviderRefundEvent(input)
			require.ErrorIs(t, err, ErrProviderRefundInvalid)
		})
	}
	var deliveries int64
	require.NoError(t, DB.Model(&ProviderRefundEvent{}).Count(&deliveries).Error)
	assert.Zero(t, deliveries)
}

func amountAwareProviderRefundInput(deliveryID, effectID, eventType, orderTradeNo, orderKind, providerTradeNo, amount string) ProviderRefundEventInput {
	input := providerRefundInput(deliveryID, eventType, orderTradeNo)
	input.Provider = PaymentProviderWaffo
	input.ProviderAccountID = "merchant-test"
	input.ProviderEnvironment = "test"
	input.EffectID = effectID
	input.OrderKind = orderKind
	input.ProviderTradeNo = providerTradeNo
	input.Amount = amount
	input.Currency = "USD"
	return input
}

func createRefundableTopUp(t *testing.T, userID, quota int, tradeNo, providerTradeNo, providerAmount string) (*User, *TopUp) {
	t.Helper()
	user := &User{Id: userID, Username: "user-" + tradeNo, Quota: quota, AffCode: "aff-" + tradeNo}
	require.NoError(t, DB.Create(user).Error)
	topup := &TopUp{
		UserId: user.Id, TradeNo: tradeNo, PaymentProvider: PaymentProviderWaffo,
		PaymentMethod: PaymentMethodWaffo, Status: common.TopUpStatusSuccess, CreditedQuota: quota,
		ProviderTradeNo: &providerTradeNo, ProviderMerchantID: "merchant-test",
		ProviderAmount: providerAmount, ProviderCurrency: "USD",
	}
	require.NoError(t, DB.Create(topup).Error)
	return user, topup
}

func TestProcessProviderRefundEventFailedDoesNotMutateLedgers(t *testing.T) {
	truncateTables(t)
	topup := &TopUp{
		UserId: 8001, TradeNo: "refund-failed-topup", PaymentProvider: PaymentProviderWaffoPancake,
		PaymentMethod: PaymentMethodWaffoPancake, Status: common.TopUpStatusSuccess, CreditedQuota: 900,
	}
	require.NoError(t, DB.Create(topup).Error)
	input := providerRefundInput("delivery-failed", "refund.failed", topup.TradeNo)

	result, err := ProcessProviderRefundEvent(input)
	require.NoError(t, err)
	assert.Equal(t, ProviderRefundEventRefundRequired, result.Status)
	assert.False(t, result.AlreadyProcessed)

	var got TopUp
	require.NoError(t, DB.First(&got, topup.Id).Error)
	assert.Equal(t, common.TopUpStatusSuccess, got.Status)
	assert.EqualValues(t, 900, got.CreditedQuota)
	var paymentEvents int64
	require.NoError(t, DB.Model(&PaymentEvent{}).Count(&paymentEvents).Error)
	assert.Zero(t, paymentEvents, "refund deliveries must not be recorded as payment settlements")
	var ledger ProviderRefundEvent
	require.NoError(t, DB.Where("event_key = ?", ProviderRefundEventKey(input.Provider, input.DeliveryID)).First(&ledger).Error)
	assert.Equal(t, ProviderRefundEventRefundRequired, ledger.Status)
}

func TestProcessProviderRefundEventConcurrentDeliveryReplayUsesOneLedgerRow(t *testing.T) {
	previousDB := DB
	dsn := filepath.Join(t.TempDir(), "provider-refund-delivery.sqlite") + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ProviderRefundEvent{}))
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)

	input := providerRefundInput("delivery-concurrent-replay", "refund.failed", "order-unused")
	type outcome struct {
		result ProviderRefundEventResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			result, processErr := ProcessProviderRefundEvent(input)
			outcomes <- outcome{result: result, err: processErr}
		}()
	}
	close(start)

	alreadyProcessed := 0
	for i := 0; i < 2; i++ {
		got := <-outcomes
		require.NoError(t, got.err)
		assert.Equal(t, ProviderRefundEventRefundRequired, got.result.Status)
		if got.result.AlreadyProcessed {
			alreadyProcessed++
		}
	}
	assert.Equal(t, 1, alreadyProcessed)
	var count int64
	require.NoError(t, db.Model(&ProviderRefundEvent{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestProcessProviderRefundEventTopUpMissingAmountOrCurrencyRequiresManualReconciliation(t *testing.T) {
	truncateTables(t)
	user := &User{Id: 8002, Quota: 1234}
	require.NoError(t, DB.Create(user).Error)
	topup := &TopUp{
		UserId: user.Id, TradeNo: "refund-topup", PaymentProvider: PaymentProviderWaffoPancake,
		PaymentMethod: PaymentMethodWaffoPancake, Status: common.TopUpStatusSuccess, CreditedQuota: 700,
	}
	require.NoError(t, DB.Create(topup).Error)

	result, err := ProcessProviderRefundEvent(providerRefundInput("delivery-topup", "refund.succeeded", topup.TradeNo))
	require.NoError(t, err)
	assert.Equal(t, ProviderRefundEventManualReconciliation, result.Status)

	missingCurrency := amountAwareProviderRefundInput(
		"delivery-topup-missing-currency", "refund-topup-missing-currency", "refund.succeeded",
		topup.TradeNo, PaymentEventOrderTopUp, "provider-refund-topup", "7.00",
	)
	missingCurrency.Currency = ""
	result, err = ProcessProviderRefundEvent(missingCurrency)
	require.NoError(t, err)
	assert.Equal(t, ProviderRefundEventManualReconciliation, result.Status)

	var gotUser User
	require.NoError(t, DB.First(&gotUser, user.Id).Error)
	assert.EqualValues(t, user.Quota, gotUser.Quota, "successful wallet refunds must not auto-debit spendable quota")
	var got TopUp
	require.NoError(t, DB.First(&got, topup.Id).Error)
	assert.Equal(t, common.TopUpStatusSuccess, got.Status)
}

func TestProcessProviderRefundEventForcedManualPreservesEvidenceWithoutApplyingLedger(t *testing.T) {
	truncateTables(t)
	user, topup := createRefundableTopUp(t, 8025, 700, "refund-forced-manual", "waffo-forced-manual", "10.00")
	input := amountAwareProviderRefundInput(
		"delivery-forced-manual", "refund-forced-manual-effect", "refund.succeeded", topup.TradeNo,
		PaymentEventOrderTopUp, "waffo-forced-manual", "4.00",
	)
	input.ForceManualReason = " PROVIDER_PARTIAL_REFUND_UNSUPPORTED "

	result, err := ProcessProviderRefundEvent(input)
	require.NoError(t, err)
	assert.Equal(t, ProviderRefundEventManualReconciliation, result.Status)
	assert.Zero(t, result.WalletDelta)

	var gotUser User
	require.NoError(t, DB.First(&gotUser, user.Id).Error)
	assert.Equal(t, 700, gotUser.Quota)
	var delivery ProviderRefundEvent
	require.NoError(t, DB.Where("delivery_id = ?", input.DeliveryID).First(&delivery).Error)
	assert.Equal(t, input.OrderTradeNo, delivery.OrderTradeNo)
	assert.Equal(t, input.OrderKind, delivery.OrderKind)
	assert.Equal(t, input.ProviderTradeNo, delivery.ProviderTradeNo)
	assert.Equal(t, "4.00", delivery.Amount)
	assert.Equal(t, "USD", delivery.Currency)
	assert.Equal(t, ProviderRefundManualReasonPartialRefundUnsupported, delivery.DecisionReason)
	assert.Empty(t, delivery.EffectKey)
	assert.Zero(t, delivery.WalletDelta)
	var effects int64
	require.NoError(t, DB.Model(&ProviderReversalEffect{}).Count(&effects).Error)
	assert.Zero(t, effects)
	var operations int64
	require.NoError(t, DB.Model(&BillingOperation{}).Count(&operations).Error)
	assert.Zero(t, operations)

	replayed, err := ProcessProviderRefundEvent(input)
	require.NoError(t, err)
	assert.True(t, replayed.AlreadyProcessed)
	assert.Equal(t, ProviderRefundEventManualReconciliation, replayed.Status)

	invalid := input
	invalid.DeliveryID = "delivery-forced-manual-free-text"
	invalid.EffectID = "refund-forced-manual-free-text"
	invalid.ForceManualReason = "customer asked nicely"
	_, err = ProcessProviderRefundEvent(invalid)
	require.ErrorIs(t, err, ErrProviderRefundInvalid)
	var deliveries int64
	require.NoError(t, DB.Model(&ProviderRefundEvent{}).Count(&deliveries).Error)
	assert.EqualValues(t, 1, deliveries)
}

func TestProcessProviderRefundEventFullSubscriptionRefundRevokesUniqueEntitlement(t *testing.T) {
	truncateTables(t)
	user := &User{Id: 8003, Group: "vip"}
	require.NoError(t, DB.Create(user).Error)
	providerTradeNo := "waffo-subscription-payment"
	order := &SubscriptionOrder{
		UserId: user.Id, PlanId: 1, TradeNo: "refund-subscription", PaymentProvider: PaymentProviderWaffoPancake,
		PaymentMethod: PaymentMethodWaffoPancake, Status: common.TopUpStatusSuccess,
		ProviderTradeNo: &providerTradeNo, ProviderMerchantID: "merchant-test",
		ProviderAmount: "10.00", ProviderCurrency: "USD",
	}
	order.PaymentProvider = PaymentProviderWaffo
	order.PaymentMethod = PaymentMethodWaffo
	require.NoError(t, DB.Create(order).Error)
	sub := &UserSubscription{
		UserId: order.UserId, PlanId: 1, AmountTotal: 1000, AmountUsed: 40,
		StartTime: time.Now().Add(-time.Hour).Unix(), EndTime: time.Now().Add(time.Hour).Unix(),
		Status: "active", SubscriptionOrderTradeNo: order.TradeNo,
		UpgradeGroup: "vip", PrevUserGroup: "default",
	}
	require.NoError(t, DB.Create(sub).Error)

	result, err := ProcessProviderRefundEvent(amountAwareProviderRefundInput(
		"delivery-subscription", "refund-subscription-full", "refund.succeeded", order.TradeNo,
		PaymentEventOrderSubscription, providerTradeNo, "10.00",
	))
	require.NoError(t, err)
	assert.Equal(t, ProviderRefundEventEntitlementRevoked, result.Status)

	var got UserSubscription
	require.NoError(t, DB.First(&got, sub.Id).Error)
	assert.Equal(t, "cancelled", got.Status)
	assert.LessOrEqual(t, got.EndTime, time.Now().Unix())
	assert.Equal(t, int64(40), got.AmountUsed)
	var gotUser User
	require.NoError(t, DB.First(&gotUser, user.Id).Error)
	assert.Equal(t, "default", gotUser.Group)
}

func TestProcessProviderRefundEventPartialSubscriptionRefundsRevokeAtCumulativeFullAmount(t *testing.T) {
	truncateTables(t)
	user := &User{Id: 8022, Username: "subscription-cumulative", AffCode: "aff-subscription-cumulative", Group: "vip"}
	require.NoError(t, DB.Create(user).Error)
	providerTradeNo := "waffo-subscription-cumulative"
	order := &SubscriptionOrder{
		UserId: user.Id, PlanId: 7, TradeNo: "refund-subscription-cumulative", PaymentProvider: PaymentProviderWaffo,
		PaymentMethod: PaymentMethodWaffo, Status: common.TopUpStatusSuccess,
		ProviderTradeNo: &providerTradeNo, ProviderMerchantID: "merchant-test",
		ProviderAmount: "10.00", ProviderCurrency: "USD",
	}
	require.NoError(t, DB.Create(order).Error)
	sub := &UserSubscription{
		UserId: order.UserId, PlanId: order.PlanId, AmountTotal: 1000, AmountUsed: 40,
		StartTime: time.Now().Add(-time.Hour).Unix(), EndTime: time.Now().Add(time.Hour).Unix(),
		Status: "active", SubscriptionOrderTradeNo: order.TradeNo,
		UpgradeGroup: "vip", PrevUserGroup: "default",
	}
	require.NoError(t, DB.Create(sub).Error)

	first, err := ProcessProviderRefundEvent(amountAwareProviderRefundInput(
		"delivery-subscription-partial-four", "subscription-partial-four", "refund.succeeded", order.TradeNo,
		PaymentEventOrderSubscription, providerTradeNo, "4.00",
	))
	require.NoError(t, err)
	assert.Equal(t, ProviderRefundEventManualReconciliation, first.Status)
	assert.Zero(t, first.WalletDelta)
	var afterFirst UserSubscription
	require.NoError(t, DB.First(&afterFirst, sub.Id).Error)
	assert.Equal(t, "active", afterFirst.Status)
	var userAfterFirst User
	require.NoError(t, DB.First(&userAfterFirst, user.Id).Error)
	assert.Equal(t, "vip", userAfterFirst.Group)

	second, err := ProcessProviderRefundEvent(amountAwareProviderRefundInput(
		"delivery-subscription-partial-six", "subscription-partial-six", "refund.succeeded", order.TradeNo,
		PaymentEventOrderSubscription, providerTradeNo, "6.00",
	))
	require.NoError(t, err)
	assert.Equal(t, ProviderRefundEventEntitlementRevoked, second.Status)
	assert.Zero(t, second.WalletDelta)

	var gotSub UserSubscription
	require.NoError(t, DB.First(&gotSub, sub.Id).Error)
	assert.Equal(t, "cancelled", gotSub.Status)
	assert.LessOrEqual(t, gotSub.EndTime, time.Now().Unix())
	var gotUser User
	require.NoError(t, DB.First(&gotUser, user.Id).Error)
	assert.Equal(t, "default", gotUser.Group)
	var effects []ProviderReversalEffect
	require.NoError(t, DB.Where("order_trade_no = ?", order.TradeNo).Order("id asc").Find(&effects).Error)
	require.Len(t, effects, 2)
	assert.Equal(t, ProviderReversalEffectObserved, effects[0].Status)
	assert.Equal(t, "4", effects[0].CumulativeAmount)
	assert.Equal(t, ProviderReversalEffectRevoked, effects[1].Status)
	assert.Equal(t, "10", effects[1].CumulativeAmount)
}

func TestProcessProviderRefundEventAmbiguousSubscriptionBindingNeedsManualReconciliation(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Create(&User{Id: 8004, Group: "default"}).Error)
	order := &SubscriptionOrder{
		UserId: 8004, PlanId: 1, TradeNo: "refund-ambiguous", PaymentProvider: PaymentProviderWaffo,
		PaymentMethod: PaymentMethodWaffo, Status: common.TopUpStatusSuccess,
		ProviderMerchantID: "merchant-test", ProviderAmount: "10.00", ProviderCurrency: "USD",
	}
	providerTradeNo := "waffo-ambiguous-payment"
	order.ProviderTradeNo = &providerTradeNo
	require.NoError(t, DB.Create(order).Error)
	for i := 0; i < 2; i++ {
		require.NoError(t, DB.Create(&UserSubscription{
			UserId: order.UserId, PlanId: i + 1, AmountTotal: 1000, AmountUsed: 10,
			StartTime: time.Now().Add(-time.Hour).Unix(), EndTime: time.Now().Add(time.Hour).Unix(),
			Status: "active", SubscriptionOrderTradeNo: order.TradeNo,
		}).Error)
	}

	result, err := ProcessProviderRefundEvent(amountAwareProviderRefundInput(
		"delivery-ambiguous", "refund-ambiguous-full", "refund.succeeded", order.TradeNo,
		PaymentEventOrderSubscription, providerTradeNo, "10.00",
	))
	require.NoError(t, err)
	assert.Equal(t, ProviderRefundEventManualReconciliation, result.Status)
	var active int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("subscription_order_trade_no = ? AND status = ?", order.TradeNo, "active").Count(&active).Error)
	assert.EqualValues(t, 2, active)
}

func TestProcessProviderRefundEventFullTopUpRefundDebitsExactCreditedQuota(t *testing.T) {
	truncateTables(t)
	user, topup := createRefundableTopUp(t, 8010, 700, "refund-full-topup", "waffo-payment-full", "10.00")

	result, err := ProcessProviderRefundEvent(amountAwareProviderRefundInput(
		"delivery-full-topup", "refund-full-topup-effect", "refund.succeeded", topup.TradeNo,
		PaymentEventOrderTopUp, "waffo-payment-full", "10.00",
	))
	require.NoError(t, err)
	assert.Equal(t, ProviderRefundEventApplied, result.Status)
	assert.EqualValues(t, -700, result.WalletDelta)

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Zero(t, got.Quota)
	var effect ProviderReversalEffect
	require.NoError(t, DB.Where("provider = ? AND effect_id = ?", PaymentProviderWaffo, "refund-full-topup-effect").First(&effect).Error)
	assert.EqualValues(t, -700, effect.WalletDelta)
	var operation BillingOperation
	require.NoError(t, DB.Where("request_id = ?", effect.EffectKey).First(&operation).Error)
	assert.Equal(t, BillingOperationApplied, operation.Status)
}

func TestProcessProviderRefundEventCommitsWhenRedisClientIsUnavailable(t *testing.T) {
	truncateTables(t)
	previousRedisEnabled, previousRDB := common.RedisEnabled, common.RDB
	common.RedisEnabled = true
	common.RDB = nil
	t.Cleanup(func() {
		common.RedisEnabled = previousRedisEnabled
		common.RDB = previousRDB
	})

	user, topup := createRefundableTopUp(t, 8026, 700, "refund-redis-unavailable", "waffo-payment-redis-unavailable", "10.00")
	result, err := ProcessProviderRefundEvent(amountAwareProviderRefundInput(
		"delivery-redis-unavailable", "refund-redis-unavailable-effect", "refund.succeeded", topup.TradeNo,
		PaymentEventOrderTopUp, "waffo-payment-redis-unavailable", "10.00",
	))
	require.NoError(t, err)
	assert.Equal(t, ProviderRefundEventApplied, result.Status)
	assert.EqualValues(t, -700, result.WalletDelta)

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Zero(t, got.Quota)

	var repair QuotaCacheRepair
	require.NoError(t, DB.Where("entity_type = ? AND entity_id = ?", QuotaCacheRepairEntityUser, user.Id).First(&repair).Error)
	assert.Equal(t, quotaCacheRepairPending, repair.Status)
	assert.Equal(t, getUserCacheKey(user.Id), repair.CacheKey)
}

func TestProcessProviderRefundEventRequiresExactPaymentBinding(t *testing.T) {
	tests := []struct {
		name           string
		bindingOrder   string
		expectedStatus ProviderRefundEventStatus
		expectedReason string
		expectedQuota  int
	}{
		{
			name:           "missing legacy binding stays manual",
			expectedStatus: ProviderRefundEventManualReconciliation,
			expectedReason: ProviderRefundManualReasonPaymentBindingMissing,
			expectedQuota:  700,
		},
		{
			name:           "binding owned by another order stays manual",
			bindingOrder:   "another-order",
			expectedStatus: ProviderRefundEventManualReconciliation,
			expectedReason: ProviderRefundManualReasonPaymentBindingConflict,
			expectedQuota:  700,
		},
		{
			name:           "exact scoped binding permits reversal",
			bindingOrder:   "refund-bound-topup",
			expectedStatus: ProviderRefundEventApplied,
			expectedReason: "wallet_quota_reversed",
			expectedQuota:  0,
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			truncateTables(t)
			user, topup := createRefundableTopUp(t, 8030+index, 700, "refund-bound-topup", "waffo-payment-bound", "10.00")
			if test.bindingOrder != "" {
				require.NoError(t, BindProviderPaymentBindings(ProviderPaymentBindingBatch{
					Provider:            PaymentProviderWaffo,
					ProviderAccountID:   "merchant-test",
					ProviderEnvironment: "test",
					OrderTradeNo:        test.bindingOrder,
					OrderKind:           PaymentEventOrderTopUp,
					Objects: []ProviderPaymentObject{{
						ObjectType: ProviderPaymentObjectAcquiringOrder,
						ObjectID:   "waffo-payment-bound",
					}},
				}))
			}

			input := amountAwareProviderRefundInput(
				fmt.Sprintf("delivery-bound-%d", index), fmt.Sprintf("refund-bound-effect-%d", index),
				"refund.succeeded", topup.TradeNo, PaymentEventOrderTopUp, "waffo-payment-bound", "10.00",
			)
			input.ProviderObjectType = ProviderPaymentObjectAcquiringOrder
			result, err := ProcessProviderRefundEvent(input)
			require.NoError(t, err)
			assert.Equal(t, test.expectedStatus, result.Status)

			var got User
			require.NoError(t, DB.First(&got, user.Id).Error)
			assert.Equal(t, test.expectedQuota, got.Quota)
			var delivery ProviderRefundEvent
			require.NoError(t, DB.Where("event_key = ?", ProviderRefundEventScopedKey(
				input.Provider, input.ProviderAccountID, input.ProviderEnvironment, input.DeliveryID,
			)).First(&delivery).Error)
			assert.Equal(t, test.expectedReason, delivery.DecisionReason)
			assert.Equal(t, ProviderPaymentObjectAcquiringOrder, delivery.ProviderObjectType)
		})
	}
}

func TestProcessProviderRefundEventFinalEffectSaveFailureRollsBackTransaction(t *testing.T) {
	truncateTables(t)
	user, topup := createRefundableTopUp(t, 8016, 700, "refund-effect-save-failure", "waffo-payment-save-failure", "10.00")
	input := amountAwareProviderRefundInput(
		"delivery-effect-save-failure", "refund-effect-save-failure", "refund.succeeded", topup.TradeNo,
		PaymentEventOrderTopUp, "waffo-payment-save-failure", "10.00",
	)

	const triggerName = "fail_provider_reversal_effect_applied_update"
	require.NoError(t, DB.Exec(`CREATE TRIGGER `+triggerName+`
		BEFORE UPDATE OF status ON provider_reversal_effects
		WHEN NEW.status = 'applied'
		BEGIN
			SELECT RAISE(ABORT, 'forced final effect save failure');
		END`).Error)
	t.Cleanup(func() { DB.Exec(`DROP TRIGGER IF EXISTS ` + triggerName) })

	_, err := ProcessProviderRefundEvent(input)
	require.ErrorContains(t, err, "persist provider reversal effect")

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, 700, got.Quota)
	for name, model := range map[string]interface{}{
		"billing operation": &BillingOperation{},
		"payment event":     &PaymentEvent{},
		"reversal effect":   &ProviderReversalEffect{},
		"refund delivery":   &ProviderRefundEvent{},
	} {
		var count int64
		require.NoError(t, DB.Model(model).Count(&count).Error)
		assert.Zero(t, count, "%s must roll back", name)
	}

	require.NoError(t, DB.Exec(`DROP TRIGGER `+triggerName).Error)
	result, err := ProcessProviderRefundEvent(input)
	require.NoError(t, err)
	assert.Equal(t, ProviderRefundEventApplied, result.Status)
	assert.EqualValues(t, -700, result.WalletDelta)
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Zero(t, got.Quota)
}

func TestProcessProviderRefundEventPartialRefundsUseCumulativeQuotaTarget(t *testing.T) {
	truncateTables(t)
	user, topup := createRefundableTopUp(t, 8011, 100, "refund-partial-topup", "waffo-payment-partial", "3.00")

	first, err := ProcessProviderRefundEvent(amountAwareProviderRefundInput(
		"delivery-partial-one", "refund-partial-one", "refund.succeeded", topup.TradeNo,
		PaymentEventOrderTopUp, "waffo-payment-partial", "1.00",
	))
	require.NoError(t, err)
	assert.EqualValues(t, -33, first.WalletDelta)
	var afterFirst User
	require.NoError(t, DB.First(&afterFirst, user.Id).Error)
	assert.Equal(t, 67, afterFirst.Quota)

	second, err := ProcessProviderRefundEvent(amountAwareProviderRefundInput(
		"delivery-partial-two", "refund-partial-two", "refund.succeeded", topup.TradeNo,
		PaymentEventOrderTopUp, "waffo-payment-partial", "2.00",
	))
	require.NoError(t, err)
	assert.EqualValues(t, -67, second.WalletDelta)
	var afterSecond User
	require.NoError(t, DB.First(&afterSecond, user.Id).Error)
	assert.Zero(t, afterSecond.Quota, "cumulative full refund must exactly reverse CreditedQuota despite per-event rounding")
}

func TestProcessProviderRefundEventConcurrentPartialRefundsReachExactCumulativeTotal(t *testing.T) {
	truncateTables(t)
	user, topup := createRefundableTopUp(t, 8021, 100, "refund-concurrent-partials", "waffo-concurrent-partials", "3.00")
	inputs := []ProviderRefundEventInput{
		amountAwareProviderRefundInput(
			"delivery-concurrent-partial-one", "concurrent-partial-one", "refund.succeeded", topup.TradeNo,
			PaymentEventOrderTopUp, "waffo-concurrent-partials", "1.00",
		),
		amountAwareProviderRefundInput(
			"delivery-concurrent-partial-two", "concurrent-partial-two", "refund.succeeded", topup.TradeNo,
			PaymentEventOrderTopUp, "waffo-concurrent-partials", "2.00",
		),
	}
	type outcome struct {
		result ProviderRefundEventResult
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, len(inputs))
	for _, input := range inputs {
		input := input
		go func() {
			<-start
			result, err := ProcessProviderRefundEvent(input)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)

	var walletDelta int64
	for range inputs {
		got := <-outcomes
		require.NoError(t, got.err)
		assert.Equal(t, ProviderRefundEventApplied, got.result.Status)
		walletDelta += got.result.WalletDelta
	}
	assert.EqualValues(t, -100, walletDelta)

	var gotUser User
	require.NoError(t, DB.First(&gotUser, user.Id).Error)
	assert.Zero(t, gotUser.Quota)
	var effects []ProviderReversalEffect
	require.NoError(t, DB.Where("order_trade_no = ? AND status = ?", topup.TradeNo, ProviderReversalEffectApplied).Find(&effects).Error)
	require.Len(t, effects, 2)
	var persistedDelta int64
	for i := range effects {
		persistedDelta += effects[i].WalletDelta
	}
	assert.EqualValues(t, -100, persistedDelta)
}

func TestProcessProviderRefundEventLogicalEffectReplayAcrossDeliveriesIsIdempotent(t *testing.T) {
	truncateTables(t)
	user, topup := createRefundableTopUp(t, 8012, 500, "refund-logical-replay", "waffo-payment-replay", "5.00")
	input := amountAwareProviderRefundInput(
		"delivery-replay-one", "refund-stable-effect", "refund.succeeded", topup.TradeNo,
		PaymentEventOrderTopUp, "waffo-payment-replay", "5.00",
	)

	first, err := ProcessProviderRefundEvent(input)
	require.NoError(t, err)
	assert.False(t, first.AlreadyProcessed)
	input.DeliveryID = "delivery-replay-two"
	input.BusinessEventID = "business-delivery-replay-two"
	input.RefundTicketMerchantExternalID = "refund-ticket-delivery-replay-two"
	second, err := ProcessProviderRefundEvent(input)
	require.NoError(t, err)
	assert.True(t, second.AlreadyProcessed)
	assert.Zero(t, second.WalletDelta)

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Zero(t, got.Quota)
	var effects int64
	require.NoError(t, DB.Model(&ProviderReversalEffect{}).Count(&effects).Error)
	assert.EqualValues(t, 1, effects)
	var deliveries int64
	require.NoError(t, DB.Model(&ProviderRefundEvent{}).Count(&deliveries).Error)
	assert.EqualValues(t, 2, deliveries)
}

func TestProcessProviderRefundEventRejectsCumulativeAmountAbovePayment(t *testing.T) {
	truncateTables(t)
	user, topup := createRefundableTopUp(t, 8013, 100, "refund-overpayment", "waffo-payment-over", "10.00")
	_, err := ProcessProviderRefundEvent(amountAwareProviderRefundInput(
		"delivery-refund-eight", "refund-eight", "refund.succeeded", topup.TradeNo,
		PaymentEventOrderTopUp, "waffo-payment-over", "8.00",
	))
	require.NoError(t, err)

	result, err := ProcessProviderRefundEvent(amountAwareProviderRefundInput(
		"delivery-refund-three", "refund-three", "refund.succeeded", topup.TradeNo,
		PaymentEventOrderTopUp, "waffo-payment-over", "3.00",
	))
	require.NoError(t, err)
	assert.Equal(t, ProviderRefundEventManualReconciliation, result.Status)
	assert.Zero(t, result.WalletDelta)
	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, 20, got.Quota)
	var effects int64
	require.NoError(t, DB.Model(&ProviderReversalEffect{}).
		Where("status IN ?", []ProviderReversalEffectStatus{
			ProviderReversalEffectApplied,
			ProviderReversalEffectObserved,
			ProviderReversalEffectRevoked,
		}).Count(&effects).Error)
	assert.EqualValues(t, 1, effects, "an over-refund must not enter the applied effect ledger")
}

func TestProcessProviderRefundEventAllowsDebtWhenRefundedQuotaWasSpent(t *testing.T) {
	truncateTables(t)
	user, topup := createRefundableTopUp(t, 8014, 10, "refund-spent-topup", "waffo-payment-spent", "10.00")
	topup.CreditedQuota = 700
	require.NoError(t, DB.Model(topup).Update("credited_quota", 700).Error)

	result, err := ProcessProviderRefundEvent(amountAwareProviderRefundInput(
		"delivery-refund-spent", "refund-spent", "refund.succeeded", topup.TradeNo,
		PaymentEventOrderTopUp, "waffo-payment-spent", "10.00",
	))
	require.NoError(t, err)
	assert.EqualValues(t, -700, result.WalletDelta)
	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, -690, got.Quota)
}

func TestProcessProviderRefundEventEnforcesNegativeWalletQuotaLimit(t *testing.T) {
	t.Run("rejects delta below lower bound", func(t *testing.T) {
		truncateTables(t)
		startingQuota := -common.MaxWalletQuota + 50
		user, topup := createRefundableTopUp(t, 8023, startingQuota, "refund-wallet-below-limit", "waffo-wallet-below-limit", "1.00")
		topup.CreditedQuota = 100
		require.NoError(t, DB.Model(topup).Update("credited_quota", topup.CreditedQuota).Error)

		result, err := ProcessProviderRefundEvent(amountAwareProviderRefundInput(
			"delivery-wallet-below-limit", "wallet-below-limit", "refund.succeeded", topup.TradeNo,
			PaymentEventOrderTopUp, "waffo-wallet-below-limit", "1.00",
		))
		require.NoError(t, err)
		assert.Equal(t, ProviderRefundEventManualReconciliation, result.Status)
		assert.Zero(t, result.WalletDelta)

		var got User
		require.NoError(t, DB.First(&got, user.Id).Error)
		assert.Equal(t, startingQuota, got.Quota)
		var effect ProviderReversalEffect
		require.NoError(t, DB.Where("effect_id = ?", "wallet-below-limit").First(&effect).Error)
		assert.Equal(t, ProviderReversalEffectRejected, effect.Status)
		assert.Equal(t, "wallet_quota_limit", effect.DecisionReason)
		var operations int64
		require.NoError(t, DB.Model(&BillingOperation{}).Count(&operations).Error)
		assert.Zero(t, operations)
	})

	t.Run("allows delta exactly to lower bound", func(t *testing.T) {
		truncateTables(t)
		startingQuota := -common.MaxWalletQuota + 100
		user, topup := createRefundableTopUp(t, 8024, startingQuota, "refund-wallet-at-limit", "waffo-wallet-at-limit", "1.00")
		topup.CreditedQuota = 100
		require.NoError(t, DB.Model(topup).Update("credited_quota", topup.CreditedQuota).Error)

		result, err := ProcessProviderRefundEvent(amountAwareProviderRefundInput(
			"delivery-wallet-at-limit", "wallet-at-limit", "refund.succeeded", topup.TradeNo,
			PaymentEventOrderTopUp, "waffo-wallet-at-limit", "1.00",
		))
		require.NoError(t, err)
		assert.Equal(t, ProviderRefundEventApplied, result.Status)
		assert.EqualValues(t, -100, result.WalletDelta)

		var got User
		require.NoError(t, DB.First(&got, user.Id).Error)
		assert.Equal(t, -common.MaxWalletQuota, got.Quota)
		var operation BillingOperation
		require.NoError(t, DB.Where("component = ?", "provider_reversal").First(&operation).Error)
		assert.Equal(t, BillingOperationApplied, operation.Status)
	})
}

func TestProcessProviderRefundEventDisputeReinstatementExactlyReversesWithdrawal(t *testing.T) {
	truncateTables(t)
	user, topup := createRefundableTopUp(t, 8015, 700, "refund-dispute", "waffo-payment-dispute", "10.00")
	withdrawal := amountAwareProviderRefundInput(
		"delivery-dispute-withdrawn", "dispute-one", "dispute.funds_withdrawn", topup.TradeNo,
		PaymentEventOrderTopUp, "waffo-payment-dispute", "10.00",
	)
	withdrawal.RefundStatus = ""
	withdrawn, err := ProcessProviderRefundEvent(withdrawal)
	require.NoError(t, err)
	assert.EqualValues(t, -700, withdrawn.WalletDelta)

	reinstatement := amountAwareProviderRefundInput(
		"delivery-dispute-reinstated", "dispute-one", "dispute.funds_reinstated", topup.TradeNo,
		PaymentEventOrderTopUp, "waffo-payment-dispute", "10.00",
	)
	reinstatement.RelatedEffectID = "dispute-one"
	reinstatement.RefundStatus = ""
	reinstated, err := ProcessProviderRefundEvent(reinstatement)
	require.NoError(t, err)
	assert.EqualValues(t, 700, reinstated.WalletDelta)

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, 700, got.Quota)
}

func TestProcessProviderRefundEventRejectsReinstatementRelatedToAnotherOrder(t *testing.T) {
	truncateTables(t)
	firstUser, firstTopUp := createRefundableTopUp(t, 8017, 700, "dispute-related-first", "waffo-related-first", "10.00")
	secondUser, secondTopUp := createRefundableTopUp(t, 8018, 500, "dispute-related-second", "waffo-related-second", "5.00")

	for _, input := range []ProviderRefundEventInput{
		amountAwareProviderRefundInput(
			"delivery-related-first-withdrawal", "related-first-withdrawal", "dispute.funds_withdrawn",
			firstTopUp.TradeNo, PaymentEventOrderTopUp, "waffo-related-first", "10.00",
		),
		amountAwareProviderRefundInput(
			"delivery-related-second-withdrawal", "related-second-withdrawal", "dispute.funds_withdrawn",
			secondTopUp.TradeNo, PaymentEventOrderTopUp, "waffo-related-second", "5.00",
		),
	} {
		input.RefundStatus = ""
		result, err := ProcessProviderRefundEvent(input)
		require.NoError(t, err)
		assert.Equal(t, ProviderRefundEventApplied, result.Status)
	}

	reinstatement := amountAwareProviderRefundInput(
		"delivery-wrong-related-reinstatement", "wrong-related-reinstatement", "dispute.funds_reinstated",
		firstTopUp.TradeNo, PaymentEventOrderTopUp, "waffo-related-first", "10.00",
	)
	reinstatement.RelatedEffectID = "related-second-withdrawal"
	reinstatement.RefundStatus = ""
	result, err := ProcessProviderRefundEvent(reinstatement)
	require.NoError(t, err)
	assert.Equal(t, ProviderRefundEventManualReconciliation, result.Status)
	assert.Zero(t, result.WalletDelta)

	for _, user := range []*User{firstUser, secondUser} {
		var got User
		require.NoError(t, DB.First(&got, user.Id).Error)
		assert.Zero(t, got.Quota)
	}
	var effect ProviderReversalEffect
	require.NoError(t, DB.Where("effect_id = ?", reinstatement.EffectID).First(&effect).Error)
	assert.Equal(t, ProviderReversalEffectRejected, effect.Status)
	assert.Equal(t, "dispute_reinstatement_mismatch", effect.DecisionReason)
}

func TestProcessProviderRefundEventRejectsSecondReinstatementOfWithdrawal(t *testing.T) {
	truncateTables(t)
	user, topup := createRefundableTopUp(t, 8019, 700, "dispute-double-reinstatement", "waffo-double-reinstatement", "10.00")

	withdrawal := amountAwareProviderRefundInput(
		"delivery-double-withdrawal", "double-withdrawal", "dispute.funds_withdrawn", topup.TradeNo,
		PaymentEventOrderTopUp, "waffo-double-reinstatement", "10.00",
	)
	withdrawal.RefundStatus = ""
	withdrawn, err := ProcessProviderRefundEvent(withdrawal)
	require.NoError(t, err)
	assert.EqualValues(t, -700, withdrawn.WalletDelta)

	firstReinstatement := amountAwareProviderRefundInput(
		"delivery-first-reinstatement", "first-reinstatement", "dispute.funds_reinstated", topup.TradeNo,
		PaymentEventOrderTopUp, "waffo-double-reinstatement", "10.00",
	)
	firstReinstatement.RelatedEffectID = withdrawal.EffectID
	firstReinstatement.RefundStatus = ""
	first, err := ProcessProviderRefundEvent(firstReinstatement)
	require.NoError(t, err)
	assert.EqualValues(t, 700, first.WalletDelta)

	secondReinstatement := amountAwareProviderRefundInput(
		"delivery-second-reinstatement", "second-reinstatement", "dispute.funds_reinstated", topup.TradeNo,
		PaymentEventOrderTopUp, "waffo-double-reinstatement", "10.00",
	)
	secondReinstatement.RelatedEffectID = withdrawal.EffectID
	secondReinstatement.RefundStatus = ""
	second, err := ProcessProviderRefundEvent(secondReinstatement)
	require.NoError(t, err)
	assert.Equal(t, ProviderRefundEventManualReconciliation, second.Status)
	assert.Zero(t, second.WalletDelta)

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, 700, got.Quota)
	var effect ProviderReversalEffect
	require.NoError(t, DB.Where("effect_id = ?", secondReinstatement.EffectID).First(&effect).Error)
	assert.Equal(t, ProviderReversalEffectRejected, effect.Status)
	assert.Equal(t, "dispute_already_reinstated", effect.DecisionReason)
	assert.Nil(t, effect.RelatedEffectKey)
	var operations int64
	require.NoError(t, DB.Model(&BillingOperation{}).Count(&operations).Error)
	assert.EqualValues(t, 2, operations)
}

func TestProcessProviderRefundEventRejectsNonExactReinstatementAfterInterleavedRefund(t *testing.T) {
	truncateTables(t)
	user, topup := createRefundableTopUp(t, 8020, 100, "dispute-interleaved-refund", "waffo-interleaved-refund", "3.00")

	withdrawal := amountAwareProviderRefundInput(
		"delivery-interleaved-withdrawal", "interleaved-withdrawal", "dispute.funds_withdrawn", topup.TradeNo,
		PaymentEventOrderTopUp, "waffo-interleaved-refund", "1.00",
	)
	withdrawal.RefundStatus = ""
	withdrawn, err := ProcessProviderRefundEvent(withdrawal)
	require.NoError(t, err)
	assert.EqualValues(t, -33, withdrawn.WalletDelta)

	refunded, err := ProcessProviderRefundEvent(amountAwareProviderRefundInput(
		"delivery-interleaved-refund", "interleaved-refund", "refund.succeeded", topup.TradeNo,
		PaymentEventOrderTopUp, "waffo-interleaved-refund", "1.00",
	))
	require.NoError(t, err)
	assert.EqualValues(t, -34, refunded.WalletDelta)

	reinstatement := amountAwareProviderRefundInput(
		"delivery-interleaved-reinstatement", "interleaved-reinstatement", "dispute.funds_reinstated", topup.TradeNo,
		PaymentEventOrderTopUp, "waffo-interleaved-refund", "1.00",
	)
	reinstatement.RelatedEffectID = withdrawal.EffectID
	reinstatement.RefundStatus = ""
	reinstated, err := ProcessProviderRefundEvent(reinstatement)
	require.NoError(t, err)
	assert.Equal(t, ProviderRefundEventManualReconciliation, reinstated.Status)
	assert.Zero(t, reinstated.WalletDelta)

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, 33, got.Quota)
	var effect ProviderReversalEffect
	require.NoError(t, DB.Where("effect_id = ?", reinstatement.EffectID).First(&effect).Error)
	assert.Equal(t, ProviderReversalEffectRejected, effect.Status)
	assert.Equal(t, "dispute_reinstatement_not_exact_inverse", effect.DecisionReason)
	assert.Nil(t, effect.RelatedEffectKey, "a rejected reinstatement must not consume the withdrawal's unique restoration key")
}

func TestProcessProviderRefundEventDeliveryIsIdempotentAndConflictsOnIdentityChange(t *testing.T) {
	truncateTables(t)
	topup := &TopUp{
		UserId: 8005, TradeNo: "refund-idempotent", PaymentProvider: PaymentProviderWaffoPancake,
		PaymentMethod: PaymentMethodWaffoPancake, Status: common.TopUpStatusSuccess, CreditedQuota: 200,
	}
	require.NoError(t, DB.Create(topup).Error)
	input := providerRefundInput("delivery-repeat", "refund.succeeded", topup.TradeNo)

	first, err := ProcessProviderRefundEvent(input)
	require.NoError(t, err)
	assert.False(t, first.AlreadyProcessed)
	second, err := ProcessProviderRefundEvent(input)
	require.NoError(t, err)
	assert.True(t, second.AlreadyProcessed)
	assert.Equal(t, first.Status, second.Status)

	conflict := input
	conflict.RefundTicketMerchantExternalID = "different-ticket"
	_, err = ProcessProviderRefundEvent(conflict)
	require.ErrorIs(t, err, ErrProviderRefundConflict)

	conflict = input
	conflict.EventType = "refund.failed"
	conflict.RefundStatus = "failed"
	_, err = ProcessProviderRefundEvent(conflict)
	require.ErrorIs(t, err, ErrProviderRefundConflict)
}

func TestProcessProviderRefundEventDatabaseFailureIsRetryable(t *testing.T) {
	previousDB := DB
	brokenDB, err := gorm.Open(sqlite.Open("file:provider-refund-closed?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := brokenDB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	DB = brokenDB
	t.Cleanup(func() { DB = previousDB })

	_, err = ProcessProviderRefundEvent(providerRefundInput("delivery-db-error", "refund.succeeded", "missing-order"))
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrProviderRefundInvalid))
	assert.False(t, errors.Is(err, ErrProviderRefundConflict))
}

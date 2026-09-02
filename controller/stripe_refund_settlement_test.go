package controller

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v81"
	"gorm.io/gorm"
)

func configureStripeReversalTest(t *testing.T) string {
	t.Helper()
	previousSecret, previousAccountID := setting.StripeApiSecret, setting.StripeAccountId
	setting.StripeApiSecret = "sk_test_reversal"
	setting.StripeAccountId = ""
	t.Cleanup(func() {
		setting.StripeApiSecret = previousSecret
		setting.StripeAccountId = previousAccountID
	})
	return stripeStandardAccountScope(setting.GetStripeConfig())
}

func stripeRefundEventForTest(t *testing.T, eventID string, eventType stripe.EventType, status stripe.RefundStatus) stripe.Event {
	t.Helper()
	return stripeRefundEventWithDetailsForTest(t, eventID, eventType, "re_refund_1", 800, status)
}

func stripeRefundEventWithDetailsForTest(t *testing.T, eventID string, eventType stripe.EventType, refundID string, amount int64, status stripe.RefundStatus) stripe.Event {
	t.Helper()
	raw, err := common.Marshal(map[string]interface{}{
		"id":             refundID,
		"object":         "refund",
		"amount":         amount,
		"currency":       "usd",
		"status":         string(status),
		"payment_intent": "pi_payment_1",
		"charge":         "ch_payment_1",
		"reason":         "requested_by_customer",
	})
	require.NoError(t, err)
	return stripe.Event{
		ID:       eventID,
		Livemode: false,
		Type:     eventType,
		Data:     &stripe.EventData{Raw: raw},
	}
}

type stripeRefundFixture struct {
	ID              string
	Amount          int64
	Status          stripe.RefundStatus
	ChargeID        string
	PaymentIntentID string
}

func stripeChargeRefundedEventForTest(t *testing.T, eventID string, refunds ...stripeRefundFixture) stripe.Event {
	t.Helper()
	refundData := make([]map[string]interface{}, 0, len(refunds))
	amountRefunded := int64(0)
	for _, refund := range refunds {
		chargeID := refund.ChargeID
		if chargeID == "" {
			chargeID = "ch_payment_1"
		}
		paymentIntentID := refund.PaymentIntentID
		if paymentIntentID == "" {
			paymentIntentID = "pi_payment_1"
		}
		refundData = append(refundData, map[string]interface{}{
			"id":             refund.ID,
			"object":         "refund",
			"amount":         refund.Amount,
			"currency":       "usd",
			"status":         string(refund.Status),
			"charge":         chargeID,
			"payment_intent": paymentIntentID,
			"reason":         "requested_by_customer",
		})
		if refund.Status == stripe.RefundStatusSucceeded {
			amountRefunded += refund.Amount
		}
	}
	raw, err := common.Marshal(map[string]interface{}{
		"id":              "ch_payment_1",
		"object":          "charge",
		"amount":          int64(800),
		"amount_captured": int64(800),
		"amount_refunded": amountRefunded,
		"currency":        "usd",
		"livemode":        false,
		"payment_intent":  "pi_payment_1",
		"refunded":        amountRefunded == 800,
		"refunds": map[string]interface{}{
			"object":   "list",
			"data":     refundData,
			"has_more": false,
			"url":      "/v1/charges/ch_payment_1/refunds",
		},
	})
	require.NoError(t, err)
	return stripe.Event{
		ID:       eventID,
		Livemode: false,
		Type:     stripe.EventTypeChargeRefunded,
		Data:     &stripe.EventData{Raw: raw},
	}
}

func stripeDisputeEventForTest(t *testing.T, eventID string, eventType stripe.EventType) stripe.Event {
	t.Helper()
	raw, err := common.Marshal(map[string]interface{}{
		"id":             "dp_dispute_1",
		"object":         "dispute",
		"amount":         int64(800),
		"currency":       "usd",
		"livemode":       false,
		"status":         "needs_response",
		"payment_intent": "pi_payment_1",
		"charge":         "ch_payment_1",
		"reason":         "fraudulent",
	})
	require.NoError(t, err)
	return stripe.Event{
		ID:       eventID,
		Livemode: false,
		Type:     eventType,
		Data:     &stripe.EventData{Raw: raw},
	}
}

func setupStripeReversalTestDB(t *testing.T) string {
	t.Helper()
	providerAccountID := configureStripeReversalTest(t)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousRedisEnabled, previousRDB := common.RedisEnabled, common.RDB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "stripe-reversal.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.TopUp{},
		&model.SubscriptionOrder{},
		&model.UserSubscription{},
		&model.PaymentEvent{},
		&model.ProviderPaymentBinding{},
		&model.ProviderRefundEvent{},
		&model.ProviderReversalEffect{},
		&model.BillingOperation{},
		&model.QuotaCacheRepair{},
	))
	model.DB, model.LOG_DB = db, db
	common.RedisEnabled, common.RDB = true, nil
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.RedisEnabled, common.RDB = previousRedisEnabled, previousRDB
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	return providerAccountID
}

func createStripeReversalTopUp(t *testing.T, providerAccountID string, bindPaymentIntent, bindCharge bool) (*model.User, *model.TopUp) {
	t.Helper()
	user := &model.User{Id: 9101, Username: "stripe-reversal-user", Quota: 800, Group: "default"}
	require.NoError(t, model.DB.Create(user).Error)
	checkoutID := "cs_checkout_1"
	topUp := &model.TopUp{
		UserId: user.Id, TradeNo: "stripe-refund-order", PaymentMethod: model.PaymentMethodStripe,
		PaymentProvider: model.PaymentProviderStripe, Status: common.TopUpStatusSuccess,
		CreditedQuota: 800, ProviderTradeNo: &checkoutID, ProviderCheckoutID: checkoutID,
		ProviderMerchantID: providerAccountID, ProviderAmount: "8.00", ProviderCurrency: "USD",
	}
	require.NoError(t, model.DB.Create(topUp).Error)
	objects := make([]model.ProviderPaymentObject, 0, 2)
	if bindPaymentIntent {
		objects = append(objects, model.ProviderPaymentObject{ObjectType: model.ProviderPaymentObjectPaymentIntent, ObjectID: "pi_payment_1"})
	}
	if bindCharge {
		objects = append(objects, model.ProviderPaymentObject{ObjectType: model.ProviderPaymentObjectCharge, ObjectID: "ch_payment_1"})
	}
	if len(objects) > 0 {
		require.NoError(t, model.BindProviderPaymentBindings(model.ProviderPaymentBindingBatch{
			Provider:            model.PaymentProviderStripe,
			ProviderAccountID:   providerAccountID,
			ProviderEnvironment: "test",
			OrderTradeNo:        topUp.TradeNo,
			OrderKind:           model.PaymentEventOrderTopUp,
			Objects:             objects,
		}))
	}
	return user, topUp
}

func TestBuildStripeReversalEventInputUsesChargeBeforePaymentIntent(t *testing.T) {
	providerAccountID := configureStripeReversalTest(t)
	event := stripeRefundEventForTest(t, "evt_refund_1", stripe.EventTypeRefundUpdated, stripe.RefundStatusSucceeded)
	input, objects, actionable, err := buildStripeReversalEventInput(event, `{"id":"evt_refund_1"}`)
	require.NoError(t, err)
	assert.True(t, actionable)
	assert.Equal(t, providerAccountID, input.ProviderAccountID)
	assert.Equal(t, "test", input.ProviderEnvironment)
	assert.Equal(t, "refund.succeeded", input.EventType)
	assert.Equal(t, "8.00", input.Amount)
	assert.Equal(t, "USD", input.Currency)
	assert.Equal(t, model.ProviderPaymentObjectCharge, input.ProviderObjectType)
	assert.Equal(t, "ch_payment_1", input.ProviderTradeNo)
	assert.Equal(t, []model.ProviderPaymentObject{
		{ObjectType: model.ProviderPaymentObjectCharge, ObjectID: "ch_payment_1"},
		{ObjectType: model.ProviderPaymentObjectPaymentIntent, ObjectID: "pi_payment_1"},
	}, objects)
}

func TestBuildStripeReversalEventInputDefersPendingRefund(t *testing.T) {
	configureStripeReversalTest(t)
	event := stripeRefundEventForTest(t, "evt_refund_pending", stripe.EventTypeRefundCreated, stripe.RefundStatusPending)
	_, _, actionable, err := buildStripeReversalEventInput(event, `{"id":"evt_refund_pending"}`)
	require.NoError(t, err)
	assert.False(t, actionable)

	event.Livemode = true
	_, _, _, err = buildStripeReversalEventInput(event, `{"id":"evt_refund_pending"}`)
	require.ErrorIs(t, err, errStripeCheckoutMode)
}

func TestHandleStripeReversalEventAppliesBoundRefundIdempotently(t *testing.T) {
	providerAccountID := setupStripeReversalTestDB(t)
	user, _ := createStripeReversalTopUp(t, providerAccountID, true, true)
	event := stripeRefundEventForTest(t, "evt_refund_apply", stripe.EventTypeRefundUpdated, stripe.RefundStatusSucceeded)

	require.NoError(t, handleStripeReversalEvent(context.Background(), event, `{"id":"evt_refund_apply"}`))
	require.NoError(t, handleStripeReversalEvent(context.Background(), event, `{"id":"evt_refund_apply"}`))

	var got model.User
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Zero(t, got.Quota)
	var delivery model.ProviderRefundEvent
	require.NoError(t, model.DB.Where("delivery_id = ?", event.ID).First(&delivery).Error)
	assert.Equal(t, model.ProviderRefundEventApplied, delivery.Status)
	assert.Equal(t, model.ProviderPaymentObjectCharge, delivery.ProviderObjectType)
	assert.Equal(t, "ch_payment_1", delivery.ProviderTradeNo)
	var effects int64
	require.NoError(t, model.DB.Model(&model.ProviderReversalEffect{}).Count(&effects).Error)
	assert.EqualValues(t, 1, effects)
	var paymentEvents int64
	require.NoError(t, model.DB.Model(&model.PaymentEvent{}).Count(&paymentEvents).Error)
	assert.Zero(t, paymentEvents, "refund processing must not create a positive payment settlement")
}

func TestHandleStripeReversalEventFallsBackToPaymentIntentBinding(t *testing.T) {
	providerAccountID := setupStripeReversalTestDB(t)
	user, _ := createStripeReversalTopUp(t, providerAccountID, true, false)
	event := stripeRefundEventForTest(t, "evt_refund_payment_intent_fallback", stripe.EventTypeRefundUpdated, stripe.RefundStatusSucceeded)

	require.NoError(t, handleStripeReversalEvent(context.Background(), event, `{"id":"evt_refund_payment_intent_fallback"}`))
	var got model.User
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Zero(t, got.Quota)
	var delivery model.ProviderRefundEvent
	require.NoError(t, model.DB.Where("delivery_id = ?", event.ID).First(&delivery).Error)
	assert.Equal(t, model.ProviderRefundEventApplied, delivery.Status)
	assert.Equal(t, model.ProviderPaymentObjectPaymentIntent, delivery.ProviderObjectType)
	assert.Equal(t, "pi_payment_1", delivery.ProviderTradeNo)
}

func TestHandleStripeReversalEventKeepsMissingOrConflictingBindingsManual(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		providerAccountID := setupStripeReversalTestDB(t)
		user, _ := createStripeReversalTopUp(t, providerAccountID, false, false)
		event := stripeRefundEventForTest(t, "evt_refund_missing", stripe.EventTypeRefundUpdated, stripe.RefundStatusSucceeded)

		require.NoError(t, handleStripeReversalEvent(context.Background(), event, `{"id":"evt_refund_missing"}`))
		var got model.User
		require.NoError(t, model.DB.First(&got, user.Id).Error)
		assert.Equal(t, 800, got.Quota)
		var delivery model.ProviderRefundEvent
		require.NoError(t, model.DB.Where("delivery_id = ?", event.ID).First(&delivery).Error)
		assert.Equal(t, model.ProviderRefundEventManualReconciliation, delivery.Status)
		assert.Equal(t, model.ProviderRefundManualReasonPaymentBindingMissing, delivery.DecisionReason)
	})

	t.Run("conflict", func(t *testing.T) {
		providerAccountID := setupStripeReversalTestDB(t)
		user, _ := createStripeReversalTopUp(t, providerAccountID, true, false)
		require.NoError(t, model.BindProviderPaymentBindings(model.ProviderPaymentBindingBatch{
			Provider:            model.PaymentProviderStripe,
			ProviderAccountID:   providerAccountID,
			ProviderEnvironment: "test",
			OrderTradeNo:        "another-order",
			OrderKind:           model.PaymentEventOrderTopUp,
			Objects: []model.ProviderPaymentObject{{
				ObjectType: model.ProviderPaymentObjectCharge,
				ObjectID:   "ch_payment_1",
			}},
		}))
		event := stripeRefundEventForTest(t, "evt_refund_conflict", stripe.EventTypeRefundUpdated, stripe.RefundStatusSucceeded)

		require.NoError(t, handleStripeReversalEvent(context.Background(), event, `{"id":"evt_refund_conflict"}`))
		var got model.User
		require.NoError(t, model.DB.First(&got, user.Id).Error)
		assert.Equal(t, 800, got.Quota)
		var delivery model.ProviderRefundEvent
		require.NoError(t, model.DB.Where("delivery_id = ?", event.ID).First(&delivery).Error)
		assert.Equal(t, model.ProviderRefundEventManualReconciliation, delivery.Status)
		assert.Equal(t, model.ProviderRefundManualReasonPaymentBindingConflict, delivery.DecisionReason)
	})
}

func TestHandleStripeReversalEventExactlyReinstatesDisputeWithdrawal(t *testing.T) {
	providerAccountID := setupStripeReversalTestDB(t)
	user, _ := createStripeReversalTopUp(t, providerAccountID, true, true)
	withdrawn := stripeDisputeEventForTest(t, "evt_dispute_withdrawn", stripe.EventTypeChargeDisputeFundsWithdrawn)
	reinstated := stripeDisputeEventForTest(t, "evt_dispute_reinstated", stripe.EventTypeChargeDisputeFundsReinstated)

	require.NoError(t, handleStripeReversalEvent(context.Background(), withdrawn, `{"id":"evt_dispute_withdrawn"}`))
	var got model.User
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Zero(t, got.Quota)

	require.NoError(t, handleStripeReversalEvent(context.Background(), reinstated, `{"id":"evt_dispute_reinstated"}`))
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Equal(t, 800, got.Quota)
	var reinstatement model.ProviderRefundEvent
	require.NoError(t, model.DB.Where("delivery_id = ?", reinstated.ID).First(&reinstatement).Error)
	assert.Equal(t, model.ProviderRefundEventApplied, reinstatement.Status)
}

func TestHandleStripeReversalEventRecordsFailedRefundWithoutDebiting(t *testing.T) {
	providerAccountID := setupStripeReversalTestDB(t)
	user, _ := createStripeReversalTopUp(t, providerAccountID, false, false)
	event := stripeRefundEventForTest(t, "evt_refund_failed", stripe.EventTypeRefundFailed, stripe.RefundStatusFailed)

	require.NoError(t, handleStripeReversalEvent(context.Background(), event, `{"id":"evt_refund_failed"}`))
	var got model.User
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Equal(t, 800, got.Quota)
	var delivery model.ProviderRefundEvent
	require.NoError(t, model.DB.Where("delivery_id = ?", event.ID).First(&delivery).Error)
	assert.Equal(t, model.ProviderRefundEventRefundRequired, delivery.Status)
}

func TestHandleStripeChargeRefundedAppliesEachPartialRefundOnce(t *testing.T) {
	providerAccountID := setupStripeReversalTestDB(t)
	user, _ := createStripeReversalTopUp(t, providerAccountID, true, true)
	event := stripeChargeRefundedEventForTest(t, "evt_charge_refunded_multi",
		stripeRefundFixture{ID: "re_aggregate_1", Amount: 200, Status: stripe.RefundStatusSucceeded},
		stripeRefundFixture{ID: "re_aggregate_2", Amount: 300, Status: stripe.RefundStatusSucceeded},
	)

	require.NoError(t, handleStripeReversalEvent(context.Background(), event, ""))
	require.NoError(t, handleStripeReversalEvent(context.Background(), event, ""))

	var got model.User
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Equal(t, 300, got.Quota)
	for _, refundID := range []string{"re_aggregate_1", "re_aggregate_2"} {
		var delivery model.ProviderRefundEvent
		require.NoError(t, model.DB.Where("delivery_id = ?", event.ID+":"+refundID).First(&delivery).Error)
		assert.Equal(t, model.ProviderRefundEventApplied, delivery.Status)
		assert.Equal(t, refundID, delivery.EffectID)
		assert.Equal(t, model.ProviderPaymentObjectCharge, delivery.ProviderObjectType)
	}
	var deliveryCount int64
	require.NoError(t, model.DB.Model(&model.ProviderRefundEvent{}).Count(&deliveryCount).Error)
	assert.EqualValues(t, 2, deliveryCount)
	var effectCount int64
	require.NoError(t, model.DB.Model(&model.ProviderReversalEffect{}).Count(&effectCount).Error)
	assert.EqualValues(t, 2, effectCount)
}

func TestHandleStripeChargeRefundedUsesVerifiedConfigSnapshot(t *testing.T) {
	providerAccountID := setupStripeReversalTestDB(t)
	user, _ := createStripeReversalTopUp(t, providerAccountID, true, true)
	verifiedConfig := setting.GetStripeConfig()
	setting.StripeApiSecret = "sk_live_rotated_after_verification"
	event := stripeChargeRefundedEventForTest(t, "evt_charge_refunded_snapshot",
		stripeRefundFixture{ID: "re_aggregate_snapshot", Amount: 200, Status: stripe.RefundStatusSucceeded},
	)

	require.NoError(t, handleStripeReversalEventWithConfig(context.Background(), event, "", verifiedConfig))

	var got model.User
	require.NoError(t, model.DB.First(&got, user.Id).Error)
	assert.Equal(t, 600, got.Quota)
	var delivery model.ProviderRefundEvent
	require.NoError(t, model.DB.Where("delivery_id = ?", event.ID+":re_aggregate_snapshot").First(&delivery).Error)
	assert.Equal(t, providerAccountID, delivery.ProviderAccountID)
	assert.Equal(t, "test", delivery.ProviderEnvironment)
}

func TestHandleStripeChargeRefundedDeduplicatesIndependentRefundDeliveriesInEitherOrder(t *testing.T) {
	for _, independentFirst := range []bool{true, false} {
		name := "aggregate_first"
		if independentFirst {
			name = "independent_first"
		}
		t.Run(name, func(t *testing.T) {
			providerAccountID := setupStripeReversalTestDB(t)
			user, _ := createStripeReversalTopUp(t, providerAccountID, true, true)
			aggregate := stripeChargeRefundedEventForTest(t, "evt_charge_refunded_ordering",
				stripeRefundFixture{ID: "re_ordering_shared", Amount: 200, Status: stripe.RefundStatusSucceeded},
				stripeRefundFixture{ID: "re_ordering_second", Amount: 300, Status: stripe.RefundStatusSucceeded},
			)
			independent := stripeRefundEventWithDetailsForTest(
				t,
				"evt_refund_ordering_shared",
				stripe.EventTypeRefundUpdated,
				"re_ordering_shared",
				200,
				stripe.RefundStatusSucceeded,
			)

			if independentFirst {
				require.NoError(t, handleStripeReversalEvent(context.Background(), independent, ""))
				require.NoError(t, handleStripeReversalEvent(context.Background(), aggregate, ""))
			} else {
				require.NoError(t, handleStripeReversalEvent(context.Background(), aggregate, ""))
				require.NoError(t, handleStripeReversalEvent(context.Background(), independent, ""))
			}
			require.NoError(t, handleStripeReversalEvent(context.Background(), aggregate, ""))
			require.NoError(t, handleStripeReversalEvent(context.Background(), independent, ""))

			var got model.User
			require.NoError(t, model.DB.First(&got, user.Id).Error)
			assert.Equal(t, 300, got.Quota)
			var deliveryCount int64
			require.NoError(t, model.DB.Model(&model.ProviderRefundEvent{}).Count(&deliveryCount).Error)
			assert.EqualValues(t, 3, deliveryCount)
			var effectCount int64
			require.NoError(t, model.DB.Model(&model.ProviderReversalEffect{}).Count(&effectCount).Error)
			assert.EqualValues(t, 2, effectCount)
		})
	}
}

func TestHandleStripeChargeRefundedKeepsBindingFailuresDurablyManual(t *testing.T) {
	t.Run("missing aliases", func(t *testing.T) {
		providerAccountID := setupStripeReversalTestDB(t)
		user, _ := createStripeReversalTopUp(t, providerAccountID, false, false)
		event := stripeChargeRefundedEventForTest(t, "evt_charge_refunded_missing",
			stripeRefundFixture{ID: "re_aggregate_missing", Amount: 200, Status: stripe.RefundStatusSucceeded},
		)

		require.NoError(t, handleStripeReversalEvent(context.Background(), event, ""))
		var got model.User
		require.NoError(t, model.DB.First(&got, user.Id).Error)
		assert.Equal(t, 800, got.Quota)
		var delivery model.ProviderRefundEvent
		require.NoError(t, model.DB.Where("delivery_id = ?", event.ID+":re_aggregate_missing").First(&delivery).Error)
		assert.Equal(t, model.ProviderRefundEventManualReconciliation, delivery.Status)
		assert.Equal(t, model.ProviderRefundManualReasonPaymentBindingMissing, delivery.DecisionReason)
	})

	t.Run("conflicting aliases", func(t *testing.T) {
		providerAccountID := setupStripeReversalTestDB(t)
		user, _ := createStripeReversalTopUp(t, providerAccountID, true, false)
		require.NoError(t, model.BindProviderPaymentBindings(model.ProviderPaymentBindingBatch{
			Provider:            model.PaymentProviderStripe,
			ProviderAccountID:   providerAccountID,
			ProviderEnvironment: "test",
			OrderTradeNo:        "another-order",
			OrderKind:           model.PaymentEventOrderTopUp,
			Objects: []model.ProviderPaymentObject{{
				ObjectType: model.ProviderPaymentObjectCharge,
				ObjectID:   "ch_payment_1",
			}},
		}))
		event := stripeChargeRefundedEventForTest(t, "evt_charge_refunded_conflict",
			stripeRefundFixture{ID: "re_aggregate_conflict", Amount: 200, Status: stripe.RefundStatusSucceeded},
		)

		require.NoError(t, handleStripeReversalEvent(context.Background(), event, ""))
		var got model.User
		require.NoError(t, model.DB.First(&got, user.Id).Error)
		assert.Equal(t, 800, got.Quota)
		var delivery model.ProviderRefundEvent
		require.NoError(t, model.DB.Where("delivery_id = ?", event.ID+":re_aggregate_conflict").First(&delivery).Error)
		assert.Equal(t, model.ProviderRefundEventManualReconciliation, delivery.Status)
		assert.Equal(t, model.ProviderRefundManualReasonPaymentBindingConflict, delivery.DecisionReason)
	})
}

func TestHandleStripeChargeRefundedHandlesNoActionableRefundsExplicitly(t *testing.T) {
	t.Run("empty refund list is invalid", func(t *testing.T) {
		providerAccountID := setupStripeReversalTestDB(t)
		user, _ := createStripeReversalTopUp(t, providerAccountID, true, true)
		event := stripeChargeRefundedEventForTest(t, "evt_charge_refunded_empty")

		err := handleStripeReversalEvent(context.Background(), event, "")
		require.ErrorIs(t, err, errStripeCheckoutInvalid)
		var got model.User
		require.NoError(t, model.DB.First(&got, user.Id).Error)
		assert.Equal(t, 800, got.Quota)
		var deliveryCount int64
		require.NoError(t, model.DB.Model(&model.ProviderRefundEvent{}).Count(&deliveryCount).Error)
		assert.Zero(t, deliveryCount)
	})

	t.Run("pending refunds are acknowledged without a ledger effect", func(t *testing.T) {
		providerAccountID := setupStripeReversalTestDB(t)
		user, _ := createStripeReversalTopUp(t, providerAccountID, true, true)
		event := stripeChargeRefundedEventForTest(t, "evt_charge_refunded_pending",
			stripeRefundFixture{ID: "re_aggregate_pending", Amount: 200, Status: stripe.RefundStatusPending},
		)

		require.NoError(t, handleStripeReversalEvent(context.Background(), event, ""))
		var got model.User
		require.NoError(t, model.DB.First(&got, user.Id).Error)
		assert.Equal(t, 800, got.Quota)
		var deliveryCount int64
		require.NoError(t, model.DB.Model(&model.ProviderRefundEvent{}).Count(&deliveryCount).Error)
		assert.Zero(t, deliveryCount)
		var effectCount int64
		require.NoError(t, model.DB.Model(&model.ProviderReversalEffect{}).Count(&effectCount).Error)
		assert.Zero(t, effectCount)
	})
}

func TestBuildStripeChargeRefundedRejectsNestedParentMismatch(t *testing.T) {
	configureStripeReversalTest(t)
	event := stripeChargeRefundedEventForTest(t, "evt_charge_refunded_parent_mismatch",
		stripeRefundFixture{
			ID: "re_aggregate_parent_mismatch", Amount: 200, Status: stripe.RefundStatusSucceeded,
			ChargeID: "ch_other",
		},
	)
	_, err := buildStripeChargeRefundedDeliveries(event, "")
	require.ErrorIs(t, err, errStripeCheckoutInvalid)
}

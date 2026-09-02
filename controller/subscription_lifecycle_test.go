package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/webhook"
)

func stripeLifecycleTestConfig(live bool) setting.StripeConfig {
	prefix := "sk_test_"
	if live {
		prefix = "sk_live_"
	}
	return setting.StripeConfig{
		ApiSecret: prefix + "lifecycle",
		AccountID: "acct_lifecycle_platform",
	}
}

type stripeConfigMutationReader struct {
	reader  *bytes.Reader
	mutate  func()
	mutated bool
}

func (reader *stripeConfigMutationReader) Read(payload []byte) (int, error) {
	if !reader.mutated {
		reader.mutated = true
		reader.mutate()
	}
	return reader.reader.Read(payload)
}

func TestHandleStripeSubscriptionLifecycleRejectsMissingIdentity(t *testing.T) {
	raw, err := json.Marshal(map[string]interface{}{
		"id":     "in_123",
		"status": "paid",
	})
	require.NoError(t, err)
	event := stripe.Event{
		ID:      "evt_missing_subscription",
		Created: 100,
		Type:    stripe.EventType("invoice.payment_succeeded"),
		Data:    &stripe.EventData{Raw: raw},
	}
	require.Error(t, handleStripeSubscriptionLifecycle(context.Background(), event, stripeLifecycleTestConfig(event.Livemode)))
}

func TestHandleStripeSubscriptionLifecycleExtractsInvoicePeriodEnd(t *testing.T) {
	raw, err := json.Marshal(map[string]interface{}{
		"id":           "in_456",
		"subscription": "sub_456",
		"currency":     "usd",
		"amount_due":   1000,
		"amount_paid":  1000,
		"lines": map[string]interface{}{
			"data": []interface{}{map[string]interface{}{
				"price":    map[string]interface{}{"id": "price_456", "unit_amount": 1000, "currency": "usd"},
				"quantity": 1,
				"amount":   1000,
				"period":   map[string]interface{}{"end": 900},
			}},
		},
	})
	require.NoError(t, err)
	event := stripe.Event{
		ID:      "evt_period_end",
		Created: 100,
		Type:    stripe.EventType("invoice.payment_succeeded"),
		Data:    &stripe.EventData{Raw: raw},
	}
	subscriptionID, status, periodEnd, err := parseStripeSubscriptionLifecyclePayload(event, stripeLifecycleTestConfig(event.Livemode))
	require.NoError(t, err)
	require.Equal(t, "sub_456", subscriptionID)
	require.Equal(t, "active", status)
	require.Equal(t, int64(900), periodEnd)
}

func TestParseStripeInvoiceLifecycleDoesNotUseInvoiceIDAsSubscriptionID(t *testing.T) {
	raw, err := json.Marshal(map[string]interface{}{
		"id":          "in_looks_like_sub",
		"status":      "paid",
		"currency":    "usd",
		"amount_due":  1000,
		"amount_paid": 1000,
	})
	require.NoError(t, err)
	event := stripe.Event{
		ID:      "evt_invoice_without_subscription",
		Created: 100,
		Type:    stripe.EventType("invoice.payment_succeeded"),
		Data:    &stripe.EventData{Raw: raw},
	}
	_, _, _, err = parseStripeSubscriptionLifecyclePayload(event, stripeLifecycleTestConfig(event.Livemode))
	require.Error(t, err)
}

func TestParseStripeInvoiceLifecycleExtractsPriceEvidence(t *testing.T) {
	raw, err := json.Marshal(map[string]interface{}{
		"id":           "in_evidence",
		"subscription": "sub_evidence",
		"status":       "paid",
		"currency":     "usd",
		"amount_due":   1210,
		"amount_paid":  1210,
		"lines": map[string]interface{}{
			"data": []interface{}{map[string]interface{}{
				"price":    map[string]interface{}{"id": "price_evidence", "unit_amount": 1000, "currency": "usd"},
				"quantity": 1,
				"amount":   1000,
			}},
		},
	})
	require.NoError(t, err)
	event := stripe.Event{
		ID:      "evt_invoice_evidence",
		Created: 100,
		Type:    stripe.EventType("invoice.payment_succeeded"),
		Data:    &stripe.EventData{Raw: raw},
	}
	_, _, _, evidence, err := parseStripeSubscriptionLifecyclePayloadWithEvidence(event, stripeLifecycleTestConfig(event.Livemode))
	require.NoError(t, err)
	require.NotNil(t, evidence)
	require.Equal(t, "10.00", evidence.Amount)
	require.Equal(t, "USD", evidence.Currency)
	require.Equal(t, "price_evidence", evidence.ProductID)
}

func TestParseStripeInvoiceLifecycleCollectsScopedPositivePaymentObjects(t *testing.T) {
	raw, err := json.Marshal(map[string]interface{}{
		"id":           "in_binding",
		"subscription": "sub_binding",
		"status":       "paid",
		"currency":     "usd",
		"amount_due":   1000,
		"amount_paid":  1000,
		"charge":       "ch_binding",
		"payment_intent": map[string]interface{}{
			"id":            "pi_binding",
			"latest_charge": "ch_binding",
		},
		"lines": map[string]interface{}{
			"data": []interface{}{map[string]interface{}{
				"price":    map[string]interface{}{"id": "price_binding", "unit_amount": 1000, "currency": "usd"},
				"quantity": 1,
				"amount":   1000,
			}},
		},
	})
	require.NoError(t, err)
	event := stripe.Event{
		ID:      "evt_invoice_binding",
		Account: "acct_connected",
		Created: 100,
		Type:    stripe.EventType("invoice.payment_succeeded"),
		Data:    &stripe.EventData{Raw: raw},
	}

	_, _, _, _, options, err := parseStripeSubscriptionLifecyclePayloadWithDetails(event, stripeLifecycleTestConfig(event.Livemode))
	require.NoError(t, err)
	require.NotNil(t, options.PaymentBinding)
	require.Equal(t, "acct_connected", options.PaymentBinding.ProviderAccountID)
	require.Equal(t, "test", options.PaymentBinding.ProviderEnvironment)
	require.NotNil(t, options.ProviderScope)
	require.Equal(t, "acct_connected", options.ProviderScope.ProviderAccountID)
	require.Equal(t, "test", options.ProviderScope.ProviderEnvironment)
	require.ElementsMatch(t, []model.ProviderPaymentObject{
		{ObjectType: model.ProviderPaymentObjectInvoice, ObjectID: "in_binding"},
		{ObjectType: model.ProviderPaymentObjectPaymentIntent, ObjectID: "pi_binding"},
		{ObjectType: model.ProviderPaymentObjectCharge, ObjectID: "ch_binding"},
		{ObjectType: model.ProviderPaymentObjectSubscription, ObjectID: "sub_binding"},
	}, options.PaymentBinding.Objects)
}

func TestParseStripeScheduledCancellationPreservesPeriodBoundary(t *testing.T) {
	raw, err := json.Marshal(map[string]interface{}{
		"id":                   "sub_schedule",
		"status":               "canceled",
		"cancel_at_period_end": true,
		"cancel_at":            int64(2000),
		"current_period_end":   1900,
	})
	require.NoError(t, err)
	event := stripe.Event{
		ID:      "evt_schedule",
		Account: "acct_lifecycle",
		Created: 1000,
		Type:    stripe.EventType("customer.subscription.updated"),
		Data:    &stripe.EventData{Raw: raw},
	}
	_, status, periodEnd, _, options, err := parseStripeSubscriptionLifecyclePayloadWithDetails(event, stripeLifecycleTestConfig(event.Livemode))
	require.NoError(t, err)
	require.Equal(t, "canceled", status)
	require.EqualValues(t, 2000, periodEnd)
	require.True(t, options.CancellationAtPeriodEnd)
	require.False(t, options.ImmediateRevoke)
	require.NotNil(t, options.ProviderScope)
	require.Equal(t, "acct_lifecycle", options.ProviderScope.ProviderAccountID)
	require.Equal(t, "test", options.ProviderScope.ProviderEnvironment)
}

func TestParseStripeDeletedSubscriptionImmediatelyRevokes(t *testing.T) {
	raw, err := json.Marshal(map[string]interface{}{"id": "sub_deleted", "status": "canceled", "current_period_end": 999999})
	require.NoError(t, err)
	event := stripe.Event{
		ID:      "evt_deleted",
		Account: "acct_lifecycle",
		Created: time.Now().Unix(),
		Type:    stripe.EventType("customer.subscription.deleted"),
		Data:    &stripe.EventData{Raw: raw},
	}
	_, status, periodEnd, _, options, err := parseStripeSubscriptionLifecyclePayloadWithDetails(event, stripeLifecycleTestConfig(event.Livemode))
	require.NoError(t, err)
	require.Equal(t, "cancelled", status)
	require.EqualValues(t, 999999, periodEnd)
	require.True(t, options.ImmediateRevoke)
	require.False(t, options.CancellationAtPeriodEnd)
	require.NotNil(t, options.ProviderScope)
	require.Equal(t, "acct_lifecycle", options.ProviderScope.ProviderAccountID)
	require.Equal(t, "test", options.ProviderScope.ProviderEnvironment)
}

func TestParseStripeFailedInvoiceIncludesProviderScope(t *testing.T) {
	raw, err := json.Marshal(map[string]interface{}{
		"id":           "in_failed_scope",
		"subscription": "sub_failed_scope",
		"currency":     "usd",
		"amount_due":   1000,
		"amount_paid":  0,
		"lines": map[string]interface{}{
			"data": []interface{}{map[string]interface{}{
				"price":    map[string]interface{}{"id": "price_failed_scope", "unit_amount": 1000, "currency": "usd"},
				"quantity": 1,
				"amount":   1000,
			}},
		},
	})
	require.NoError(t, err)
	event := stripe.Event{
		ID:       "evt_failed_scope",
		Account:  "acct_failed_scope",
		Created:  100,
		Livemode: true,
		Type:     stripe.EventType("invoice.payment_failed"),
		Data:     &stripe.EventData{Raw: raw},
	}

	_, status, _, _, options, err := parseStripeSubscriptionLifecyclePayloadWithDetails(event, stripeLifecycleTestConfig(event.Livemode))
	require.NoError(t, err)
	require.Equal(t, "past_due", status)
	require.Nil(t, options.PaymentBinding)
	require.NotNil(t, options.ProviderScope)
	require.Equal(t, "acct_failed_scope", options.ProviderScope.ProviderAccountID)
	require.Equal(t, "live", options.ProviderScope.ProviderEnvironment)
}

func createStripeLifecycleBindingFixture(t *testing.T, accountID string) *model.UserSubscription {
	t.Helper()
	const (
		userID         = 7301
		planID         = 7302
		tradeNo        = "stripe-lifecycle-handler-order"
		subscriptionID = "sub_stripe_lifecycle_handler"
	)
	require.NoError(t, model.DB.Create(&model.User{Id: userID, Username: "stripe-lifecycle-handler-user"}).Error)
	plan := &model.SubscriptionPlan{
		Id: planID, Title: "Stripe lifecycle handler plan", PriceAmount: 10,
		Currency: "USD", DurationUnit: model.SubscriptionDurationMonth,
		DurationValue: 1, Enabled: true, TotalAmount: 1000,
		StripePriceId: "price_stripe_lifecycle_handler",
	}
	require.NoError(t, model.DB.Create(plan).Error)
	providerTradeNo := "cs_stripe_lifecycle_handler"
	order := &model.SubscriptionOrder{
		UserId: userID, PlanId: planID, Money: 10, TradeNo: tradeNo,
		PaymentMethod: model.PaymentMethodStripe, PaymentProvider: model.PaymentProviderStripe,
		Status: common.TopUpStatusSuccess, ProviderTradeNo: &providerTradeNo,
		ProviderSubscriptionID: subscriptionID, ProviderAmount: "10.00",
		ProviderCurrency: "USD", ProviderProductID: plan.StripePriceId,
		ProviderMerchantID: accountID, ProviderOrderName: plan.Title,
	}
	require.NoError(t, order.SetEntitlementSnapshot(plan))
	require.NoError(t, model.DB.Create(order).Error)
	subscription := &model.UserSubscription{
		UserId: userID, PlanId: planID, AmountTotal: 1000, AmountUsed: 25,
		Status: "active", EndTime: time.Now().Unix() + 3600,
		ProviderSubscriptionID:       subscriptionID,
		ProviderSubscriptionProvider: model.PaymentProviderStripe,
		SubscriptionOrderTradeNo:     tradeNo,
	}
	require.NoError(t, model.DB.Create(subscription).Error)
	orderTradeNo := tradeNo
	require.NoError(t, model.DB.Create(&model.SubscriptionProviderBindingRecord{
		UserSubscriptionID:     subscription.Id,
		OrderTradeNo:           &orderTradeNo,
		Provider:               model.PaymentProviderStripe,
		ProviderSubscriptionID: subscriptionID,
	}).Error)
	require.NoError(t, model.BindProviderPaymentBindings(model.ProviderPaymentBindingBatch{
		Provider:            model.PaymentProviderStripe,
		ProviderAccountID:   accountID,
		ProviderEnvironment: "test",
		OrderTradeNo:        tradeNo,
		OrderKind:           model.PaymentEventOrderSubscription,
		Objects: []model.ProviderPaymentObject{{
			ObjectType: model.ProviderPaymentObjectSubscription,
			ObjectID:   subscriptionID,
		}},
	}))
	return subscription
}

func stripeFailedInvoiceLifecycleEvent(t *testing.T, eventID, accountID string) stripe.Event {
	t.Helper()
	raw, err := json.Marshal(map[string]interface{}{
		"id":           "in_" + eventID,
		"subscription": "sub_stripe_lifecycle_handler",
		"currency":     "usd",
		"amount_due":   1000,
		"amount_paid":  0,
		"lines": map[string]interface{}{
			"data": []interface{}{map[string]interface{}{
				"price": map[string]interface{}{
					"id": "price_stripe_lifecycle_handler", "unit_amount": 1000, "currency": "usd",
				},
				"quantity": 1,
				"amount":   1000,
			}},
		},
	})
	require.NoError(t, err)
	return stripe.Event{
		ID: eventID, Account: accountID, Created: time.Now().Unix(),
		Type: stripe.EventType("invoice.payment_failed"),
		Data: &stripe.EventData{Raw: raw},
	}
}

func TestHandleStripeSubscriptionLifecycleUsesVerifiedConfigSnapshot(t *testing.T) {
	setupStripeLifecycleTestDB(t)
	verifiedConfig := setting.StripeConfig{ApiSecret: "sk_test_snapshot"}
	snapshotAccountID := stripeStandardAccountScope(verifiedConfig)
	require.True(t, isStripeCredentialAccountScope(snapshotAccountID))
	subscription := createStripeLifecycleBindingFixture(t, snapshotAccountID)

	previousConfig := setting.GetStripeConfig()
	setting.UpdateStripeConfig(func(config *setting.StripeConfig) {
		config.ApiSecret = "sk_live_rotated"
		config.AccountID = "acct_rotated"
	})
	t.Cleanup(func() {
		setting.UpdateStripeConfig(func(config *setting.StripeConfig) { *config = previousConfig })
	})
	event := stripeFailedInvoiceLifecycleEvent(t, "evt_snapshot_scope", "")

	require.NoError(t, handleStripeSubscriptionLifecycle(context.Background(), event, verifiedConfig))
	var got model.UserSubscription
	require.NoError(t, model.DB.First(&got, subscription.Id).Error)
	require.Equal(t, "past_due", got.Status)
	var eventCount int64
	require.NoError(t, model.DB.Model(&model.PaymentEvent{}).Where("provider_trade_no = ?", event.ID).Count(&eventCount).Error)
	require.EqualValues(t, 1, eventCount)
}

func TestStripeWebhookUsesSingleConfigSnapshotForVerificationAndLifecycle(t *testing.T) {
	setupStripeLifecycleTestDB(t)
	verifiedConfig := setting.StripeConfig{
		ApiSecret:     "sk_test_webhook_snapshot",
		WebhookSecret: "whsec_webhook_snapshot",
	}
	snapshotAccountID := stripeStandardAccountScope(verifiedConfig)
	subscription := createStripeLifecycleBindingFixture(t, snapshotAccountID)

	previousConfig := setting.GetStripeConfig()
	setting.UpdateStripeConfig(func(config *setting.StripeConfig) { *config = verifiedConfig })
	t.Cleanup(func() {
		setting.UpdateStripeConfig(func(config *setting.StripeConfig) { *config = previousConfig })
	})
	payload, err := json.Marshal(map[string]interface{}{
		"id":       "evt_webhook_snapshot",
		"object":   "event",
		"created":  time.Now().Unix(),
		"livemode": false,
		"type":     "invoice.payment_failed",
		"data": map[string]interface{}{
			"object": map[string]interface{}{
				"id":           "in_evt_webhook_snapshot",
				"subscription": "sub_stripe_lifecycle_handler",
				"currency":     "usd",
				"amount_due":   1000,
				"amount_paid":  0,
				"lines": map[string]interface{}{
					"data": []interface{}{map[string]interface{}{
						"price": map[string]interface{}{
							"id": "price_stripe_lifecycle_handler", "unit_amount": 1000, "currency": "usd",
						},
						"quantity": 1,
						"amount":   1000,
					}},
				},
			},
		},
	})
	require.NoError(t, err)
	signedPayload := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload: payload,
		Secret:  verifiedConfig.WebhookSecret,
	})
	body := &stripeConfigMutationReader{
		reader: bytes.NewReader(payload),
		mutate: func() {
			setting.UpdateStripeConfig(func(config *setting.StripeConfig) {
				config.ApiSecret = "sk_live_webhook_rotated"
				config.WebhookSecret = "whsec_webhook_rotated"
				config.AccountID = "acct_webhook_rotated"
			})
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/stripe/webhook", io.NopCloser(body))
	request.Header.Set("Stripe-Signature", signedPayload.Header)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = request

	StripeWebhook(ginContext)

	require.Equal(t, http.StatusOK, recorder.Code)
	var got model.UserSubscription
	require.NoError(t, model.DB.First(&got, subscription.Id).Error)
	require.Equal(t, "past_due", got.Status)
	var eventCount int64
	require.NoError(t, model.DB.Model(&model.PaymentEvent{}).Where("provider_trade_no = ?", "evt_webhook_snapshot").Count(&eventCount).Error)
	require.EqualValues(t, 1, eventCount)
}

func TestStripeSubscriptionLifecycleScopeUsesSnapshotAndConnectAccount(t *testing.T) {
	stripeConfig := setting.StripeConfig{ApiSecret: "sk_test_scope_snapshot"}

	standardScope, err := stripeSubscriptionLifecycleScope(stripe.Event{}, stripeConfig)
	require.NoError(t, err)
	require.Equal(t, "key_"+stripeKeyFingerprint(stripeConfig.ApiSecret), standardScope.ProviderAccountID)
	require.Equal(t, "test", standardScope.ProviderEnvironment)

	connectScope, err := stripeSubscriptionLifecycleScope(stripe.Event{Account: " acct_connected_snapshot "}, stripeConfig)
	require.NoError(t, err)
	require.Equal(t, "acct_connected_snapshot", connectScope.ProviderAccountID)
	require.Equal(t, "test", connectScope.ProviderEnvironment)
}

func TestHandleStripeSubscriptionLifecycleRejectsForeignAccountBeforeMutation(t *testing.T) {
	setupStripeLifecycleTestDB(t)
	subscription := createStripeLifecycleBindingFixture(t, "acct_platform")
	stripeConfig := setting.StripeConfig{ApiSecret: "sk_test_platform", AccountID: "acct_platform"}

	failedInvoice := stripeFailedInvoiceLifecycleEvent(t, "evt_foreign_failed", "acct_foreign")
	scheduledRaw, err := json.Marshal(map[string]interface{}{
		"id": "sub_stripe_lifecycle_handler", "status": "canceled",
		"cancel_at_period_end": true, "current_period_end": time.Now().Unix() + 1800,
	})
	require.NoError(t, err)
	deletedRaw, err := json.Marshal(map[string]interface{}{
		"id": "sub_stripe_lifecycle_handler", "status": "canceled",
		"current_period_end": time.Now().Unix() + 1800,
	})
	require.NoError(t, err)
	tests := []stripe.Event{
		failedInvoice,
		{
			ID: "evt_foreign_scheduled", Account: "acct_foreign", Created: time.Now().Unix(),
			Type: stripe.EventType("customer.subscription.updated"), Data: &stripe.EventData{Raw: scheduledRaw},
		},
		{
			ID: "evt_foreign_deleted", Account: "acct_foreign", Created: time.Now().Unix(),
			Type: stripe.EventType("customer.subscription.deleted"), Data: &stripe.EventData{Raw: deletedRaw},
		},
	}

	for _, event := range tests {
		err := handleStripeSubscriptionLifecycle(context.Background(), event, stripeConfig)
		require.ErrorIs(t, err, model.ErrProviderPaymentBindingNotFound)
		var got model.UserSubscription
		require.NoError(t, model.DB.First(&got, subscription.Id).Error)
		require.Equal(t, "active", got.Status)
		require.EqualValues(t, 25, got.AmountUsed)
		require.Zero(t, got.ProviderLifecycleEventTime)
		var eventCount int64
		require.NoError(t, model.DB.Model(&model.PaymentEvent{}).Where("provider_trade_no = ?", event.ID).Count(&eventCount).Error)
		require.Zero(t, eventCount)
	}
}

func TestHandleStripeSubscriptionLifecycleRejectsInvalidScopeBeforeMutation(t *testing.T) {
	tests := []struct {
		name         string
		stripeConfig setting.StripeConfig
		accountID    string
		wantErr      error
	}{
		{
			name:         "missing standard account scope",
			stripeConfig: setting.StripeConfig{},
			wantErr:      errStripeCheckoutMode,
		},
		{
			name:         "invalid connect account",
			stripeConfig: setting.StripeConfig{ApiSecret: "sk_test_invalid_connect"},
			accountID:    "connected-account",
			wantErr:      errStripeCheckoutInvalid,
		},
		{
			name:         "oversized connect account",
			stripeConfig: setting.StripeConfig{ApiSecret: "sk_test_oversized_connect"},
			accountID:    "acct_" + strings.Repeat("x", 256),
			wantErr:      errStripeCheckoutInvalid,
		},
		{
			name:         "event mode differs from snapshot",
			stripeConfig: setting.StripeConfig{ApiSecret: "sk_live_mode_mismatch", AccountID: "acct_platform"},
			accountID:    "acct_platform",
			wantErr:      errStripeCheckoutMode,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setupStripeLifecycleTestDB(t)
			subscription := createStripeLifecycleBindingFixture(t, "acct_platform")
			event := stripeFailedInvoiceLifecycleEvent(t, "evt_invalid_scope", test.accountID)
			var initialEventCount, initialBindingCount int64
			require.NoError(t, model.DB.Model(&model.PaymentEvent{}).Count(&initialEventCount).Error)
			require.NoError(t, model.DB.Model(&model.ProviderPaymentBinding{}).Count(&initialBindingCount).Error)

			err := handleStripeSubscriptionLifecycle(context.Background(), event, test.stripeConfig)
			require.ErrorIs(t, err, test.wantErr)

			var got model.UserSubscription
			require.NoError(t, model.DB.First(&got, subscription.Id).Error)
			require.Equal(t, "active", got.Status)
			require.EqualValues(t, 25, got.AmountUsed)
			require.Zero(t, got.ProviderLifecycleEventTime)
			var eventCount, bindingCount int64
			require.NoError(t, model.DB.Model(&model.PaymentEvent{}).Count(&eventCount).Error)
			require.NoError(t, model.DB.Model(&model.ProviderPaymentBinding{}).Count(&bindingCount).Error)
			require.Equal(t, initialEventCount, eventCount)
			require.Equal(t, initialBindingCount, bindingCount)
		})
	}
}

func TestParseStripeSubscriptionLifecycleRejectsNegativeTimestamps(t *testing.T) {
	tests := []struct {
		name  string
		type_ string
		body  map[string]interface{}
	}{
		{
			name:  "current period end",
			type_: "customer.subscription.updated",
			body: map[string]interface{}{
				"id":                 "sub_negative_period",
				"status":             "active",
				"current_period_end": -1,
			},
		},
		{
			name:  "cancel at",
			type_: "customer.subscription.updated",
			body: map[string]interface{}{
				"id":        "sub_negative_cancel_at",
				"status":    "canceled",
				"cancel_at": -1,
			},
		},
		{
			name:  "canceled at",
			type_: "customer.subscription.updated",
			body: map[string]interface{}{
				"id":          "sub_negative_canceled_at",
				"status":      "canceled",
				"canceled_at": -1,
			},
		},
		{
			name:  "ended at",
			type_: "customer.subscription.updated",
			body: map[string]interface{}{
				"id":       "sub_negative_ended_at",
				"status":   "canceled",
				"ended_at": -1,
			},
		},
		{
			name:  "invoice line period end",
			type_: "invoice.payment_succeeded",
			body: map[string]interface{}{
				"id":           "in_negative_line_period",
				"subscription": "sub_negative_line_period",
				"currency":     "usd",
				"amount_due":   1000,
				"amount_paid":  1000,
				"lines": map[string]interface{}{
					"data": []interface{}{map[string]interface{}{
						"price":    map[string]interface{}{"id": "price_negative_period", "unit_amount": 1000, "currency": "usd"},
						"quantity": 1,
						"amount":   1000,
						"period":   map[string]interface{}{"end": -1},
					}},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.body)
			require.NoError(t, err)
			event := stripe.Event{
				ID:      "evt_" + tc.name,
				Created: 100,
				Type:    stripe.EventType(tc.type_),
				Data:    &stripe.EventData{Raw: raw},
			}
			_, _, _, _, _, err = parseStripeSubscriptionLifecyclePayloadWithDetails(event, stripeLifecycleTestConfig(event.Livemode))
			require.ErrorIs(t, err, errStripeCheckoutInvalid)
		})
	}
}

func TestParseStripeInvoiceLifecycleRejectsUnderpaidInvoice(t *testing.T) {
	raw, err := json.Marshal(map[string]interface{}{
		"id":           "in_underpaid",
		"subscription": "sub_underpaid",
		"status":       "paid",
		"currency":     "usd",
		"amount_due":   1000,
		"amount_paid":  999,
		"lines": map[string]interface{}{
			"data": []interface{}{map[string]interface{}{
				"price":    map[string]interface{}{"id": "price_underpaid", "unit_amount": 1000, "currency": "usd"},
				"quantity": 1,
				"amount":   1000,
			}},
		},
	})
	require.NoError(t, err)
	event := stripe.Event{
		ID:      "evt_invoice_underpaid",
		Created: 100,
		Type:    stripe.EventType("invoice.payment_succeeded"),
		Data:    &stripe.EventData{Raw: raw},
	}
	_, _, _, _, err = parseStripeSubscriptionLifecyclePayloadWithEvidence(event, stripeLifecycleTestConfig(event.Livemode))
	require.ErrorIs(t, err, errStripeCheckoutMismatch)
}

func TestParseStripeInvoiceLifecycleRejectsNominalPriceWithOneUnitPaid(t *testing.T) {
	raw, err := json.Marshal(map[string]interface{}{
		"id":           "in_one_unit_paid",
		"subscription": "sub_one_unit_paid",
		"status":       "paid",
		"currency":     "usd",
		"amount_due":   1,
		"amount_paid":  1,
		"lines": map[string]interface{}{
			"data": []interface{}{map[string]interface{}{
				"price":    map[string]interface{}{"id": "price_nominal", "unit_amount": 1000, "currency": "usd"},
				"quantity": 1,
				"amount":   1000,
			}},
		},
	})
	require.NoError(t, err)
	event := stripe.Event{
		ID:      "evt_invoice_one_unit_paid",
		Created: 100,
		Type:    stripe.EventType("invoice.payment_succeeded"),
		Data:    &stripe.EventData{Raw: raw},
	}
	_, _, _, _, err = parseStripeSubscriptionLifecyclePayloadWithEvidence(event, stripeLifecycleTestConfig(event.Livemode))
	require.ErrorIs(t, err, errStripeCheckoutMismatch)
}

func TestParseStripeInvoiceLifecycleRejectsLineTotalMismatch(t *testing.T) {
	raw, err := json.Marshal(map[string]interface{}{
		"id":           "in_line_total_mismatch",
		"subscription": "sub_line_total_mismatch",
		"status":       "paid",
		"currency":     "usd",
		"amount_due":   1000,
		"amount_paid":  1000,
		"lines": map[string]interface{}{
			"data": []interface{}{map[string]interface{}{
				"price":    map[string]interface{}{"id": "price_line_total", "unit_amount": 1000, "currency": "usd"},
				"quantity": 1,
				"amount":   999,
			}},
		},
	})
	require.NoError(t, err)
	event := stripe.Event{
		ID:      "evt_invoice_line_total_mismatch",
		Created: 100,
		Type:    stripe.EventType("invoice.payment_succeeded"),
		Data:    &stripe.EventData{Raw: raw},
	}
	_, _, _, _, err = parseStripeSubscriptionLifecyclePayloadWithEvidence(event, stripeLifecycleTestConfig(event.Livemode))
	require.Error(t, err)
}

func TestParseStripeInvoiceLifecycleRejectsInvalidQuantity(t *testing.T) {
	for _, quantity := range []interface{}{0, 2, -1, 1.5, "1", nil} {
		t.Run(fmt.Sprintf("quantity_%v", quantity), func(t *testing.T) {
			body := map[string]interface{}{
				"id":           "in_bad_quantity",
				"subscription": "sub_bad_quantity",
				"status":       "paid",
				"currency":     "usd",
				"amount_due":   1000,
				"amount_paid":  1000,
				"lines": map[string]interface{}{
					"data": []interface{}{map[string]interface{}{
						"price":    map[string]interface{}{"id": "price_bad_quantity", "unit_amount": 1000, "currency": "usd"},
						"quantity": quantity,
						"amount":   1000,
					}},
				},
			}
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			event := stripe.Event{
				ID:      "evt_invoice_bad_quantity",
				Created: 100,
				Type:    stripe.EventType("invoice.payment_succeeded"),
				Data:    &stripe.EventData{Raw: raw},
			}
			_, _, _, _, err = parseStripeSubscriptionLifecyclePayloadWithEvidence(event, stripeLifecycleTestConfig(event.Livemode))
			require.Error(t, err)
		})
	}
}

func TestParseStripeInvoiceLifecycleRejectsDuplicatePriceLines(t *testing.T) {
	raw, err := json.Marshal(map[string]interface{}{
		"id":           "in_duplicate_lines",
		"subscription": "sub_duplicate_lines",
		"status":       "paid",
		"currency":     "usd",
		"amount_due":   2000,
		"amount_paid":  2000,
		"lines": map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{"price": map[string]interface{}{"id": "price_duplicate", "unit_amount": 1000, "currency": "usd"}, "quantity": 1, "amount": 1000},
				map[string]interface{}{"price": map[string]interface{}{"id": "price_duplicate", "unit_amount": 1000, "currency": "usd"}, "quantity": 1, "amount": 1000},
			},
		},
	})
	require.NoError(t, err)
	event := stripe.Event{
		ID:      "evt_invoice_duplicate_lines",
		Created: 100,
		Type:    stripe.EventType("invoice.payment_succeeded"),
		Data:    &stripe.EventData{Raw: raw},
	}
	_, _, _, _, err = parseStripeSubscriptionLifecyclePayloadWithEvidence(event, stripeLifecycleTestConfig(event.Livemode))
	require.ErrorIs(t, err, errStripeCheckoutMismatch)
}

func TestParseStripeInvoiceLifecycleRejectsCurrencyMismatchOrMalformedCurrency(t *testing.T) {
	for name, currency := range map[string]string{
		"line mismatch":     "eur",
		"invoice malformed": "US",
	} {
		t.Run(name, func(t *testing.T) {
			lineCurrency := "usd"
			raw, err := json.Marshal(map[string]interface{}{
				"id":           "in_currency",
				"subscription": "sub_currency",
				"status":       "paid",
				"currency":     currency,
				"amount_due":   1000,
				"amount_paid":  1000,
				"lines": map[string]interface{}{
					"data": []interface{}{map[string]interface{}{
						"price":    map[string]interface{}{"id": "price_currency", "unit_amount": 1000, "currency": lineCurrency},
						"quantity": 1,
						"amount":   1000,
					}},
				},
			})
			require.NoError(t, err)
			event := stripe.Event{
				ID:      "evt_invoice_currency",
				Created: 100,
				Type:    stripe.EventType("invoice.payment_succeeded"),
				Data:    &stripe.EventData{Raw: raw},
			}
			_, _, _, _, err = parseStripeSubscriptionLifecyclePayloadWithEvidence(event, stripeLifecycleTestConfig(event.Livemode))
			require.Error(t, err)
		})
	}
}

func TestCreemSubscriptionPeriodEndParsesRFC3339AndMilliseconds(t *testing.T) {
	event := &CreemWebhookEvent{}
	event.Object.CurrentPeriodEndDate = "2026-08-29T00:00:00Z"
	want := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC).Unix()
	require.Equal(t, want, creemSubscriptionPeriodEnd(event))

	event.Object.CurrentPeriodEndDate = ""
	event.Object.CurrentPeriodEnd = 1_754_400_000_000
	require.Equal(t, int64(1_754_400_000), creemSubscriptionPeriodEnd(event))
}

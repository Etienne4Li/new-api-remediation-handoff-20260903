package controller

import (
	"encoding/json"
	"fmt"
	"strings"
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

func setupStripeLifecycleTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMainType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	model.DB, model.LOG_DB = db, db
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.TopUp{},
		&model.SubscriptionPlan{},
		&model.SubscriptionOrder{},
		&model.UserSubscription{},
		&model.SubscriptionProviderBindingRecord{},
		&model.ProviderPaymentBinding{},
		&model.PaymentEvent{},
	))
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func mutateStripeCheckoutEvent(t *testing.T, event stripe.Event, mutate func(map[string]interface{})) stripe.Event {
	t.Helper()
	// EventData is pointer-backed; clone the raw payload so each subtest can
	// mutate an independent checkout instead of contaminating the base event.
	event.Data = &stripe.EventData{Raw: append([]byte(nil), event.Data.Raw...)}
	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(event.Data.Raw, &raw))
	mutate(raw)
	data, err := json.Marshal(raw)
	require.NoError(t, err)
	var object map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &object))
	event.Data.Raw = data
	event.Data.Object = object
	return event
}

func stripePendingTopUpForLifecycleTest() model.TopUp {
	return model.TopUp{
		UserId:             1,
		Amount:             10,
		Money:              8,
		TradeNo:            "ref_test_123",
		PaymentMethod:      model.PaymentMethodStripe,
		PaymentProvider:    model.PaymentProviderStripe,
		Status:             common.TopUpStatusPending,
		CreditedQuota:      500,
		ProviderMerchantID: stripeStandardAccountScope(setting.GetStripeConfig()),
		ProviderOrderName:  "New API credits",
		ProviderAmount:     "8.00",
		ProviderProductID:  "price_test_123",
		ProviderCheckoutID: "cs_test_123",
		ProviderCurrency:   "USD",
	}
}

func withStripeTestMode(t *testing.T) {
	t.Helper()
	previous := setting.StripeApiSecret
	previousAccountID := setting.StripeAccountId
	setting.StripeApiSecret = "sk_test_unit"
	setting.StripeAccountId = "acct_test_platform"
	t.Cleanup(func() {
		setting.StripeApiSecret = previous
		setting.StripeAccountId = previousAccountID
	})
}

func TestStripeAsyncPaymentFailedBindsExactCheckoutBeforeFailingOrder(t *testing.T) {
	db := setupStripeLifecycleTestDB(t)
	withStripeTestMode(t)
	topUp := stripePendingTopUpForLifecycleTest()
	require.NoError(t, db.Create(&topUp).Error)

	base := stripeCheckoutEventForTest(t, string(stripe.CheckoutSessionModePayment), 800, "topup")
	base.Type = stripe.EventTypeCheckoutSessionAsyncPaymentFailed
	base = mutateStripeCheckoutEvent(t, base, func(raw map[string]interface{}) {
		raw["payment_status"] = "unpaid"
	})

	for name, mutate := range map[string]func(map[string]interface{}){
		"different checkout with same reference": func(raw map[string]interface{}) {
			raw["id"] = "cs_other_checkout"
		},
		"subscription mode": func(raw map[string]interface{}) {
			raw["mode"] = string(stripe.CheckoutSessionModeSubscription)
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := mutateStripeCheckoutEvent(t, base, mutate)
			assert.Error(t, sessionAsyncPaymentFailed(t.Context(), candidate, "127.0.0.1"))
			var current model.TopUp
			require.NoError(t, db.First(&current, topUp.Id).Error)
			assert.Equal(t, common.TopUpStatusPending, current.Status)
		})
	}

	require.NoError(t, sessionAsyncPaymentFailed(t.Context(), base, "127.0.0.1"))
	var failed model.TopUp
	require.NoError(t, db.First(&failed, topUp.Id).Error)
	assert.Equal(t, common.TopUpStatusFailed, failed.Status)
}

func TestStripeAsyncPaymentFailedUnknownOrderIsRetryableError(t *testing.T) {
	setupStripeLifecycleTestDB(t)
	withStripeTestMode(t)
	event := stripeCheckoutEventForTest(t, string(stripe.CheckoutSessionModePayment), 800, "topup")
	event.Type = stripe.EventTypeCheckoutSessionAsyncPaymentFailed
	event = mutateStripeCheckoutEvent(t, event, func(raw map[string]interface{}) {
		raw["client_reference_id"] = "ref_unknown"
		raw["metadata"].(map[string]interface{})["new_api_order_id"] = "ref_unknown"
		raw["payment_status"] = "unpaid"
	})
	err := sessionAsyncPaymentFailed(t.Context(), event, "127.0.0.1")
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrTopUpNotFound)
}

func TestStripeAsyncPaymentFailedExpiresSubscriptionCheckout(t *testing.T) {
	db := setupStripeLifecycleTestDB(t)
	withStripeTestMode(t)
	order := model.SubscriptionOrder{
		UserId:             1,
		PlanId:             1,
		Money:              8,
		TradeNo:            "sub_ref_async_failed",
		PaymentMethod:      model.PaymentMethodStripe,
		PaymentProvider:    model.PaymentProviderStripe,
		Status:             common.TopUpStatusPending,
		ProviderMerchantID: stripeStandardAccountScope(setting.GetStripeConfig()),
		ProviderOrderName:  "New API credits",
		ProviderAmount:     "8.00",
		ProviderProductID:  "price_test_123",
		ProviderCheckoutID: "cs_test_123",
		ProviderCurrency:   "USD",
	}
	require.NoError(t, db.Create(&order).Error)

	event := stripeCheckoutEventForTest(t, string(stripe.CheckoutSessionModeSubscription), 800, "subscription")
	event.Type = stripe.EventTypeCheckoutSessionAsyncPaymentFailed
	event = mutateStripeCheckoutEvent(t, event, func(raw map[string]interface{}) {
		raw["client_reference_id"] = order.TradeNo
		raw["payment_status"] = "unpaid"
		raw["metadata"].(map[string]interface{})["new_api_order_id"] = order.TradeNo
	})

	require.NoError(t, sessionAsyncPaymentFailed(t.Context(), event, "127.0.0.1"))
	var expired model.SubscriptionOrder
	require.NoError(t, db.First(&expired, order.Id).Error)
	assert.Equal(t, common.TopUpStatusExpired, expired.Status)
}

func TestStripeSessionExpiredBindsExactCheckoutAndDoesNotAckUnknownOrder(t *testing.T) {
	db := setupStripeLifecycleTestDB(t)
	withStripeTestMode(t)
	topUp := stripePendingTopUpForLifecycleTest()
	require.NoError(t, db.Create(&topUp).Error)

	base := stripeCheckoutEventForTest(t, string(stripe.CheckoutSessionModePayment), 800, "topup")
	base.Type = stripe.EventTypeCheckoutSessionExpired
	base = mutateStripeCheckoutEvent(t, base, func(raw map[string]interface{}) {
		raw["status"] = "expired"
		raw["payment_status"] = "unpaid"
	})

	wrongCheckout := mutateStripeCheckoutEvent(t, base, func(raw map[string]interface{}) {
		raw["id"] = "cs_other_checkout"
	})
	assert.Error(t, sessionExpired(t.Context(), wrongCheckout))
	var stillPending model.TopUp
	require.NoError(t, db.First(&stillPending, topUp.Id).Error)
	assert.Equal(t, common.TopUpStatusPending, stillPending.Status)

	require.NoError(t, sessionExpired(t.Context(), base))
	var expired model.TopUp
	require.NoError(t, db.First(&expired, topUp.Id).Error)
	assert.Equal(t, common.TopUpStatusExpired, expired.Status)

	unknown := mutateStripeCheckoutEvent(t, base, func(raw map[string]interface{}) {
		raw["client_reference_id"] = "ref_unknown_expired"
		raw["metadata"].(map[string]interface{})["new_api_order_id"] = "ref_unknown_expired"
	})
	err := sessionExpired(t.Context(), unknown)
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrTopUpNotFound)
}

func TestStripeSessionExpiredValidatesSubscriptionCheckoutMode(t *testing.T) {
	db := setupStripeLifecycleTestDB(t)
	withStripeTestMode(t)
	order := model.SubscriptionOrder{
		UserId:             1,
		PlanId:             1,
		Money:              8,
		TradeNo:            "sub_ref_test_123",
		PaymentMethod:      model.PaymentMethodStripe,
		PaymentProvider:    model.PaymentProviderStripe,
		Status:             common.TopUpStatusPending,
		ProviderMerchantID: stripeStandardAccountScope(setting.GetStripeConfig()),
		ProviderOrderName:  "New API credits",
		ProviderAmount:     "8.00",
		ProviderProductID:  "price_test_123",
		ProviderCheckoutID: "cs_test_123",
		ProviderCurrency:   "USD",
	}
	require.NoError(t, db.Create(&order).Error)

	event := stripeCheckoutEventForTest(t, string(stripe.CheckoutSessionModeSubscription), 800, "subscription")
	event.Type = stripe.EventTypeCheckoutSessionExpired
	event = mutateStripeCheckoutEvent(t, event, func(raw map[string]interface{}) {
		raw["client_reference_id"] = order.TradeNo
		raw["status"] = "expired"
		raw["payment_status"] = "unpaid"
		raw["metadata"].(map[string]interface{})["new_api_order_id"] = order.TradeNo
	})

	wrongMode := mutateStripeCheckoutEvent(t, event, func(raw map[string]interface{}) {
		raw["mode"] = string(stripe.CheckoutSessionModePayment)
	})
	assert.Error(t, sessionExpired(t.Context(), wrongMode))
	var current model.SubscriptionOrder
	require.NoError(t, db.First(&current, order.Id).Error)
	assert.Equal(t, common.TopUpStatusPending, current.Status)

	require.NoError(t, sessionExpired(t.Context(), event))
	require.NoError(t, db.First(&current, order.Id).Error)
	assert.Equal(t, common.TopUpStatusExpired, current.Status)
}

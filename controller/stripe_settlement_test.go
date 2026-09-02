package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v81"
)

func stripeCheckoutEventForTest(t *testing.T, mode string, amount int64, kind string) stripe.Event {
	t.Helper()
	raw := map[string]interface{}{
		"id":                  "cs_test_123",
		"object":              "checkout.session",
		"client_reference_id": "ref_test_123",
		"status":              "complete",
		"payment_status":      "paid",
		"mode":                mode,
		"livemode":            false,
		"amount_total":        amount,
		"currency":            "usd",
		"metadata": map[string]string{
			"new_api_order_id":        "ref_test_123",
			"new_api_order_kind":      kind,
			"new_api_product_id":      "price_test_123",
			"new_api_order_name":      "New API credits",
			"new_api_merchant_id":     stripeStandardAccountScope(setting.GetStripeConfig()),
			"new_api_credited_quota":  "500",
			"new_api_currency":        "USD",
			"new_api_provider_amount": "8.00",
		},
	}
	data, err := json.Marshal(raw)
	require.NoError(t, err)
	object := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(data, &object))
	return stripe.Event{
		ID:       "evt_test_123",
		Livemode: false,
		Type:     stripe.EventTypeCheckoutSessionCompleted,
		Data: &stripe.EventData{
			Raw:    data,
			Object: object,
		},
	}
}

func TestBuildStripeProviderSettlementRequiresVerifiedSnapshotInputs(t *testing.T) {
	originalKey := setting.StripeApiSecret
	t.Cleanup(func() { setting.StripeApiSecret = originalKey })
	setting.StripeApiSecret = "sk_test_unit"

	event := stripeCheckoutEventForTest(t, string(stripe.CheckoutSessionModePayment), 800, "topup")
	settlement, err := buildStripeProviderSettlement(event, "ref_test_123", "topup")
	require.NoError(t, err)
	assert.Equal(t, "cs_test_123", settlement.ProviderTradeNo)
	assert.Equal(t, "evt_test_123", settlement.ProviderEventID)
	assert.Equal(t, "8.00", settlement.Amount)
	assert.Equal(t, "USD", settlement.Currency)
	assert.Equal(t, stripeStandardAccountScope(setting.GetStripeConfig()), settlement.ProviderAccountID)
	assert.Equal(t, "test", settlement.ProviderEnvironment)
	assert.Equal(t, []model.ProviderPaymentObject{{
		ObjectType: model.ProviderPaymentObjectCheckoutSession,
		ObjectID:   "cs_test_123",
	}}, settlement.PaymentObjects)

	for name, mutate := range map[string]func(*stripe.Event){
		"wrong mode": func(e *stripe.Event) {
			var raw map[string]interface{}
			require.NoError(t, json.Unmarshal(e.Data.Raw, &raw))
			raw["mode"] = string(stripe.CheckoutSessionModeSubscription)
			e.Data.Raw, _ = json.Marshal(raw)
		},
		"wrong amount": func(e *stripe.Event) {
			var raw map[string]interface{}
			require.NoError(t, json.Unmarshal(e.Data.Raw, &raw))
			raw["amount_total"] = float64(799)
			e.Data.Raw, _ = json.Marshal(raw)
		},
		"wrong kind": func(e *stripe.Event) {
			var raw map[string]interface{}
			require.NoError(t, json.Unmarshal(e.Data.Raw, &raw))
			metadata := raw["metadata"].(map[string]interface{})
			metadata["new_api_order_kind"] = "subscription"
			e.Data.Raw, _ = json.Marshal(raw)
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := stripeCheckoutEventForTest(t, string(stripe.CheckoutSessionModePayment), 800, "topup")
			mutate(&candidate)
			_, err := buildStripeProviderSettlement(candidate, "ref_test_123", "topup")
			assert.Error(t, err)
		})
	}
}

func TestBuildStripeProviderSettlementScopesStandardAccountByCredential(t *testing.T) {
	originalKey := setting.StripeApiSecret
	originalAccountID := setting.StripeAccountId
	t.Cleanup(func() {
		setting.StripeApiSecret = originalKey
		setting.StripeAccountId = originalAccountID
	})

	setting.StripeAccountId = ""
	setting.StripeApiSecret = "sk_test_account_one"
	event := stripeCheckoutEventForTest(t, string(stripe.CheckoutSessionModePayment), 800, "topup")
	firstScope := stripeStandardAccountScope(setting.GetStripeConfig())
	settlement, err := buildStripeProviderSettlement(event, "ref_test_123", "topup")
	require.NoError(t, err)
	require.Equal(t, firstScope, settlement.ProviderAccountID)
	require.NotEqual(t, "stripe", firstScope)

	setting.StripeApiSecret = "sk_test_account_two"
	settlement, err = buildStripeProviderSettlement(event, "ref_test_123", "topup")
	require.NoError(t, err)
	require.Equal(t, firstScope, settlement.ProviderAccountID)

	secondEvent := stripeCheckoutEventForTest(t, string(stripe.CheckoutSessionModePayment), 800, "topup")
	secondSettlement, err := buildStripeProviderSettlement(secondEvent, "ref_test_123", "topup")
	require.NoError(t, err)
	require.NotEqual(t, firstScope, secondSettlement.ProviderAccountID)
}

func TestBuildStripeProviderSettlementUsesStableConfiguredAccountID(t *testing.T) {
	originalKey := setting.StripeApiSecret
	originalAccountID := setting.StripeAccountId
	t.Cleanup(func() {
		setting.StripeApiSecret = originalKey
		setting.StripeAccountId = originalAccountID
	})

	setting.StripeApiSecret = "sk_test_account_one"
	setting.StripeAccountId = "acct_standard"
	event := stripeCheckoutEventForTest(t, string(stripe.CheckoutSessionModePayment), 800, "topup")
	settlement, err := buildStripeProviderSettlement(event, "ref_test_123", "topup")
	require.NoError(t, err)
	require.Equal(t, "acct_standard", settlement.ProviderAccountID)

	setting.StripeApiSecret = "sk_test_rotated_key"
	settlement, err = buildStripeProviderSettlement(event, "ref_test_123", "topup")
	require.NoError(t, err)
	require.Equal(t, "acct_standard", settlement.ProviderAccountID)
}

func TestBuildStripeProviderSettlementPersistsSubscriptionIDWhenExpanded(t *testing.T) {
	originalKey := setting.StripeApiSecret
	t.Cleanup(func() { setting.StripeApiSecret = originalKey })
	setting.StripeApiSecret = "sk_test_unit"

	event := stripeCheckoutEventForTest(t, string(stripe.CheckoutSessionModeSubscription), 800, "subscription")
	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(event.Data.Raw, &raw))
	raw["subscription"] = "sub_test_456"
	event.Data.Raw, _ = json.Marshal(raw)

	settlement, err := buildStripeProviderSettlement(event, "ref_test_123", "subscription")
	require.NoError(t, err)
	assert.Equal(t, "sub_test_456", settlement.ProviderSubscriptionID)
}

func TestBuildStripeProviderSettlementAllowsMissingSubscriptionIDForCompatibility(t *testing.T) {
	originalKey := setting.StripeApiSecret
	t.Cleanup(func() { setting.StripeApiSecret = originalKey })
	setting.StripeApiSecret = "sk_test_unit"

	event := stripeCheckoutEventForTest(t, string(stripe.CheckoutSessionModeSubscription), 800, "subscription")
	settlement, err := buildStripeProviderSettlement(event, "ref_test_123", "subscription")
	require.NoError(t, err)
	assert.Empty(t, settlement.ProviderSubscriptionID)
}

func TestBuildStripeProviderSettlementCollectsExpandedPaymentObjectsInAccountScope(t *testing.T) {
	originalKey := setting.StripeApiSecret
	t.Cleanup(func() { setting.StripeApiSecret = originalKey })
	setting.StripeApiSecret = "sk_test_unit"

	event := stripeCheckoutEventForTest(t, string(stripe.CheckoutSessionModeSubscription), 800, "subscription")
	event.Account = "acct_connected"
	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(event.Data.Raw, &raw))
	raw["metadata"].(map[string]interface{})["new_api_merchant_id"] = "acct_connected"
	raw["payment_intent"] = map[string]interface{}{
		"id":            "pi_expanded",
		"latest_charge": "ch_expanded",
	}
	raw["invoice"] = map[string]interface{}{
		"id":             "in_expanded",
		"payment_intent": "pi_expanded",
		"charge":         "ch_expanded",
		"subscription":   "sub_expanded",
	}
	raw["subscription"] = "sub_expanded"
	event.Data.Raw, _ = json.Marshal(raw)

	settlement, err := buildStripeProviderSettlement(event, "ref_test_123", "subscription")
	require.NoError(t, err)
	assert.Equal(t, "acct_connected", settlement.ProviderAccountID)
	assert.Equal(t, "test", settlement.ProviderEnvironment)
	assert.ElementsMatch(t, []model.ProviderPaymentObject{
		{ObjectType: model.ProviderPaymentObjectCheckoutSession, ObjectID: "cs_test_123"},
		{ObjectType: model.ProviderPaymentObjectPaymentIntent, ObjectID: "pi_expanded"},
		{ObjectType: model.ProviderPaymentObjectCharge, ObjectID: "ch_expanded"},
		{ObjectType: model.ProviderPaymentObjectInvoice, ObjectID: "in_expanded"},
		{ObjectType: model.ProviderPaymentObjectSubscription, ObjectID: "sub_expanded"},
	}, settlement.PaymentObjects)
}

func TestBuildStripeProviderSettlementRejectsConnectedAccountMismatch(t *testing.T) {
	originalKey := setting.StripeApiSecret
	t.Cleanup(func() { setting.StripeApiSecret = originalKey })
	setting.StripeApiSecret = "sk_test_unit"

	event := stripeCheckoutEventForTest(t, string(stripe.CheckoutSessionModePayment), 800, "topup")
	event.Account = "acct_other"
	_, err := buildStripeProviderSettlement(event, "ref_test_123", "topup")
	require.ErrorIs(t, err, errStripeCheckoutMismatch)
}

func TestStripeSettlementHTTPStatusSeparatesPermanentAndRetryableErrors(t *testing.T) {
	permanent := []error{
		errStripeCheckoutInvalid,
		errStripeCheckoutMismatch,
		errStripeCheckoutMode,
		errStripeOrderConflict,
		model.ErrProviderPaymentBindingInvalid,
		model.ErrProviderPaymentBindingConflict,
		model.ErrProviderPaymentBindingNotFound,
		model.ErrProviderRefundInvalid,
		model.ErrProviderRefundConflict,
		model.ErrProviderSettlementInvalid,
		model.ErrProviderSnapshotMissing,
		model.ErrProviderSnapshotMismatch,
		model.ErrProviderEventConflict,
		model.ErrPaymentMethodMismatch,
		model.ErrTopUpNotFound,
		model.ErrTopUpStatusInvalid,
		model.ErrSubscriptionOrderNotFound,
		model.ErrSubscriptionOrderStatusInvalid,
		model.ErrSubscriptionOrderConflict,
	}
	for _, err := range permanent {
		assert.Equalf(t, http.StatusBadRequest, stripeSettlementHTTPStatus(err), "error=%v", err)
	}

	assert.Equal(t, http.StatusBadRequest, stripeSettlementHTTPStatus(fmt.Errorf("wrapped: %w", errStripeCheckoutMismatch)))
	assert.Equal(t, http.StatusServiceUnavailable, stripeSettlementHTTPStatus(model.ErrDatabase))
	assert.Equal(t, http.StatusServiceUnavailable, stripeSettlementHTTPStatus(errors.New("database temporarily unavailable")))
}

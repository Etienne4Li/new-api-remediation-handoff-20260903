package controller

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// validCreemSettlementEvent mirrors the provider's documented webhook shape:
// order/product amounts are integer minor units, while amount_paid may include
// tax. The local checkout snapshot is EUR 10.00.
func newCreemAmountSettlementEventForTest() *CreemWebhookEvent {
	event := &CreemWebhookEvent{
		Id:        "evt_creem_1",
		EventType: "checkout.completed",
	}
	event.Object.Id = "ch_creem_1"
	event.Object.RequestId = "ref_creem_1"
	event.Object.Status = "completed"
	event.Object.Mode = "local"
	event.Object.Order.Id = "ord_creem_1"
	event.Object.Order.Amount = 1000 // EUR 10.00, minor units
	event.Object.Order.AmountDue = 1210
	event.Object.Order.AmountPaid = 1210 // includes 21% VAT
	event.Object.Order.Currency = "EUR"
	event.Object.Order.Status = "paid"
	event.Object.Order.Type = "onetime"
	event.Object.Order.Mode = "local"
	event.Object.Product.Id = "prod_creem_1"
	event.Object.Product.Price = 1000
	event.Object.Product.Currency = "EUR"
	event.Object.Product.Name = "Credits"
	event.Object.Product.Mode = "local"
	event.Object.Customer.Mode = "local"
	event.Object.Metadata = map[string]string{
		"reference_id": "ref_creem_1",
		"order_kind":   "topup",
		"product_id":   "prod_creem_1",
		"currency":     "EUR",
	}
	return event
}

func creemSettlementConfigForTest() setting.CreemConfig {
	return setting.CreemConfig{TestMode: false, WebhookSecret: "creem-settlement-test-secret"}
}

func TestBuildCreemProviderSettlementUsesOrderAmountAsMinorUnitSnapshot(t *testing.T) {
	settlement, err := buildCreemProviderSettlementWithConfig(newCreemAmountSettlementEventForTest(), "ref_creem_1", "topup", "10.00", creemSettlementConfigForTest())
	require.NoError(t, err)
	require.Equal(t, "10", settlement.Amount)
	require.Equal(t, creemMerchantSnapshotWithConfig(creemSettlementConfigForTest()), settlement.ProviderAccountID)
	require.Equal(t, "live", settlement.ProviderEnvironment)
	require.ElementsMatch(t, []model.ProviderPaymentObject{
		{ObjectType: model.ProviderPaymentObjectCheckoutSession, ObjectID: "ch_creem_1"},
		{ObjectType: model.ProviderPaymentObjectOrder, ObjectID: "ord_creem_1"},
	}, settlement.PaymentObjects)
}

func TestBuildCreemProviderSettlementRejectsUnderpaidOrderDespiteMatchingListedAmount(t *testing.T) {
	event := newCreemAmountSettlementEventForTest()
	// A forged/malformed payload could retain order.amount=1000 while claiming
	// only EUR 5.00 was paid. The old any-candidate matcher accepted this because
	// order.amount itself matched the snapshot.
	event.Object.Order.AmountPaid = 500
	event.Object.Order.AmountDue = 500
	_, err := buildCreemProviderSettlementWithConfig(event, "ref_creem_1", "topup", "10.00", creemSettlementConfigForTest())
	require.ErrorIs(t, err, errCreemEventMismatch)
}

func TestBuildCreemProviderSettlementRejectsProductPriceAndUnitMismatches(t *testing.T) {
	t.Run("product price", func(t *testing.T) {
		event := newCreemAmountSettlementEventForTest()
		event.Object.Product.Price = 500
		_, err := buildCreemProviderSettlementWithConfig(event, "ref_creem_1", "topup", "10.00", creemSettlementConfigForTest())
		require.ErrorIs(t, err, errCreemEventMismatch)
	})

	t.Run("multiple units", func(t *testing.T) {
		event := newCreemAmountSettlementEventForTest()
		event.Object.Units = 2
		_, err := buildCreemProviderSettlementWithConfig(event, "ref_creem_1", "topup", "10.00", creemSettlementConfigForTest())
		require.ErrorIs(t, err, errCreemEventMismatch)
	})
}

func TestBuildCreemProviderSettlementRejectsRawMajorUnitAmbiguity(t *testing.T) {
	event := newCreemAmountSettlementEventForTest()
	// Creem's integer amount is minor units. Value 10 means EUR 0.10, not
	// EUR 10.00; accepting raw equality would grant a 100x underpayment.
	event.Object.Order.Amount = 10
	event.Object.Order.AmountDue = 10
	event.Object.Order.AmountPaid = 10
	event.Object.Product.Price = 10
	_, err := buildCreemProviderSettlementWithConfig(event, "ref_creem_1", "topup", "10.00", creemSettlementConfigForTest())
	require.ErrorIs(t, err, errCreemEventMismatch)
}

func TestBuildCreemProviderSettlementExtractsExpandedSubscriptionID(t *testing.T) {
	event := newCreemAmountSettlementEventForTest()
	event.Object.Order.Type = "recurring"
	event.Object.Metadata["order_kind"] = "subscription"
	event.Object.Subscription = []byte(`{"id":"sub_creem_123"}`)
	settlement, err := buildCreemProviderSettlementWithConfig(event, "ref_creem_1", "subscription", "10.00", creemSettlementConfigForTest())
	require.NoError(t, err)
	require.Equal(t, "sub_creem_123", settlement.ProviderSubscriptionID)
	require.Contains(t, settlement.PaymentObjects, model.ProviderPaymentObject{ObjectType: model.ProviderPaymentObjectSubscription, ObjectID: "sub_creem_123"})
}

func TestBuildCreemProviderSettlementKeepsMissingSubscriptionIDCompatible(t *testing.T) {
	event := newCreemAmountSettlementEventForTest()
	event.Object.Order.Type = "recurring"
	event.Object.Metadata["order_kind"] = "subscription"
	settlement, err := buildCreemProviderSettlementWithConfig(event, "ref_creem_1", "subscription", "10.00", creemSettlementConfigForTest())
	require.NoError(t, err)
	assert.Empty(t, settlement.ProviderSubscriptionID)
}

func TestValidateCreemEventModeBindsLifecycleToConfiguredEnvironment(t *testing.T) {
	event := newCreemAmountSettlementEventForTest()
	// The fixture uses Creem's legacy "local" spelling for production.
	require.NoError(t, validateCreemEventMode(event, setting.CreemConfig{TestMode: false}))
	require.ErrorIs(t, validateCreemEventMode(event, setting.CreemConfig{TestMode: true}), errCreemEventMismatch)

	event.Object.Order.Mode = "test"
	require.ErrorIs(t, validateCreemEventMode(event, setting.CreemConfig{TestMode: false}), errCreemEventMismatch)
}

func TestValidateCreemEventModeRejectsMissingOrMixedRelatedModes(t *testing.T) {
	cfg := setting.CreemConfig{TestMode: true}
	event := newCreemAmountSettlementEventForTest()
	event.Object.Mode = "test"
	event.Object.Order.Mode = "test"
	event.Object.Product.Mode = "test"
	event.Object.Customer.Mode = "test"
	require.NoError(t, validateCreemEventMode(event, cfg))

	event.Object.Customer.Mode = "local"
	require.ErrorIs(t, validateCreemEventMode(event, cfg), errCreemEventMismatch)

	event.Object.Customer.Mode = ""
	event.Object.Order.Mode = ""
	event.Object.Mode = ""
	require.ErrorIs(t, validateCreemEventMode(event, cfg), errCreemEventMismatch, "lifecycle events without an environment binding must fail closed")
}

func TestValidateCreemEventModeUsesExpandedSubscriptionModeWhenOrderModeOmitted(t *testing.T) {
	event := newCreemAmountSettlementEventForTest()
	event.Object.Mode = ""
	event.Object.Order.Mode = ""
	event.Object.Product.Mode = ""
	event.Object.Customer.Mode = ""
	event.Object.Subscription = []byte(`{"id":"sub_mode_fallback","mode":"live"}`)
	require.NoError(t, validateCreemEventMode(event, setting.CreemConfig{TestMode: false}))
	event.Object.Subscription = []byte(`{"id":"sub_mode_fallback","mode":"test"}`)
	require.ErrorIs(t, validateCreemEventMode(event, setting.CreemConfig{TestMode: false}), errCreemEventMismatch)
}

func TestValidateCreemEventModeRejectsMalformedSubscriptionObject(t *testing.T) {
	event := newCreemAmountSettlementEventForTest()
	event.Object.Subscription = []byte(`[]`)
	require.ErrorIs(t, validateCreemEventMode(event, setting.CreemConfig{TestMode: false}), errCreemEventInvalid)

	event.Object.Subscription = []byte(`{"id":"sub_bad"`)
	require.ErrorIs(t, validateCreemEventMode(event, setting.CreemConfig{TestMode: false}), errCreemEventInvalid)
}

func TestCreemSubscriptionLifecycleEvidenceExtractsAndValidatesSnapshot(t *testing.T) {
	event := newCreemAmountSettlementEventForTest()
	event.Object.Order.Type = "recurring"
	event.Object.Product.BillingType = "recurring"
	evidence, err := creemSubscriptionLifecycleEvidence(event)
	require.NoError(t, err)
	require.Equal(t, &model.SubscriptionLifecycleEvidence{
		Amount:    "10.00",
		Currency:  "EUR",
		ProductID: "prod_creem_1",
	}, evidence)
}

func TestCreemSubscriptionLifecycleEvidenceRequiresCompletePaymentProof(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CreemWebhookEvent)
	}{
		{name: "missing currency", mutate: func(event *CreemWebhookEvent) {
			event.Object.Order.Currency = ""
		}},
		{name: "missing product", mutate: func(event *CreemWebhookEvent) {
			event.Object.Product.Id = ""
			event.Object.Order.Product = ""
		}},
		{name: "missing amount", mutate: func(event *CreemWebhookEvent) {
			event.Object.Order.Amount = 0
		}},
		{name: "missing amount paid", mutate: func(event *CreemWebhookEvent) {
			event.Object.Order.AmountPaid = 0
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := newCreemAmountSettlementEventForTest()
			tt.mutate(event)
			_, err := creemSubscriptionLifecycleEvidence(event)
			require.ErrorIs(t, err, errCreemLifecycleEvidenceMissing)
		})
	}
}

func TestCreemSubscriptionLifecycleEvidenceRejectsContradictions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CreemWebhookEvent)
	}{
		{name: "underpaid", mutate: func(event *CreemWebhookEvent) {
			event.Object.Order.AmountPaid = 999
		}},
		{name: "underpaid tax", mutate: func(event *CreemWebhookEvent) {
			event.Object.Order.AmountDue = 1210
			event.Object.Order.AmountPaid = 1209
		}},
		{name: "product price mismatch", mutate: func(event *CreemWebhookEvent) {
			event.Object.Product.Price = 999
		}},
		{name: "currency mismatch", mutate: func(event *CreemWebhookEvent) {
			event.Object.Product.Currency = "USD"
		}},
		{name: "product id mismatch", mutate: func(event *CreemWebhookEvent) {
			event.Object.Order.Product = "prod_other"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := newCreemAmountSettlementEventForTest()
			tt.mutate(event)
			_, err := creemSubscriptionLifecycleEvidence(event)
			require.ErrorIs(t, err, errCreemEventMismatch)
		})
	}
}

func TestCreemSubscriptionIDDoesNotTrustArbitraryMetadataReference(t *testing.T) {
	event := &CreemWebhookEvent{EventType: "refund.created"}
	event.Object.Metadata = map[string]string{"subscription_id": "user-supplied-row-id"}
	require.Empty(t, creemSubscriptionID(event))
	event.Object.Metadata["subscription_id"] = "sub_provider_123"
	require.Empty(t, creemSubscriptionID(event), "a provider-shaped id in customer metadata is still not provider-owned evidence")

	event.Object.Metadata = nil
	event.Object.Subscription = []byte(`{"id":"refund_123"}`)
	require.Empty(t, creemSubscriptionID(event))
	event.Object.Subscription = []byte(`{"id":"sub_provider_123"}`)
	require.Equal(t, "sub_provider_123", creemSubscriptionID(event))
	event.Object.Subscription = nil
	event.Object.Transaction.Subscription = "sub_transaction_123"
	require.Equal(t, "sub_transaction_123", creemSubscriptionID(event))
}

func TestValidateCreemProductConfigRejectsInvalidCurrencyAndNonFinitePrice(t *testing.T) {
	base := &CreemProduct{ProductId: "prod_valid", Name: "Valid", Price: 10, Currency: "USD", Quota: 1}
	require.NoError(t, validateCreemProductConfig(base))
	for _, product := range []*CreemProduct{
		{ProductId: "prod_bad_currency", Price: 10, Currency: "US"},
		{ProductId: "prod_bad_currency_chars", Price: 10, Currency: "US$"},
		{ProductId: "prod_nan", Price: math.NaN(), Currency: "USD"},
		{ProductId: "prod_inf", Price: math.Inf(1), Currency: "USD"},
	} {
		require.Error(t, validateCreemProductConfig(product))
	}
}

func TestCreemSettlementHTTPStatusClassifiesPermanentReversalErrors(t *testing.T) {
	for _, err := range []error{
		errCreemEventInvalid,
		errCreemEventMismatch,
		model.ErrProviderRefundInvalid,
		model.ErrProviderRefundConflict,
		model.ErrProviderPaymentBindingInvalid,
		model.ErrProviderPaymentBindingConflict,
		model.ErrProviderPaymentBindingNotFound,
	} {
		assert.Equal(t, http.StatusBadRequest, creemSettlementHTTPStatus(fmt.Errorf("wrapped: %w", err)))
	}
	assert.Equal(t, http.StatusInternalServerError, creemSettlementHTTPStatus(fmt.Errorf("database unavailable")))
}

func TestCreemRenewalWithoutEvidenceDoesNotTouchDatabase(t *testing.T) {
	previousDB := model.DB
	model.DB = nil
	t.Cleanup(func() { model.DB = previousDB })

	event := &CreemWebhookEvent{Id: "evt_creem_renewal_no_evidence", EventType: "subscription.renewed", CreatedAt: 100}
	event.Object.Mode = "live"
	event.Object.Subscription = []byte(`{"id":"sub_creem_renewal_no_evidence","mode":"live"}`)
	event.Object.CurrentPeriodEnd = 200
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("POST", "/webhook/creem", nil)

	err := handleCreemSubscriptionLifecycleWithConfig(ctx, event, creemSettlementConfigForTest())
	require.NoError(t, err)
	require.Equal(t, 200, recorder.Code)
}

func TestCreemTrialingAndActiveUpdateDoNotTouchDatabase(t *testing.T) {
	previousDB := model.DB
	model.DB = nil
	t.Cleanup(func() { model.DB = previousDB })

	for _, eventType := range []string{"subscription.trialing", "subscription.update"} {
		t.Run(eventType, func(t *testing.T) {
			event := &CreemWebhookEvent{Id: "evt_creem_" + eventType, EventType: eventType, CreatedAt: 100}
			event.Object.Mode = "live"
			event.Object.Subscription = []byte(`{"id":"sub_creem_nonrenewal","mode":"live"}`)
			event.Object.CurrentPeriodEnd = 200
			event.Object.Status = "active"
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest("POST", "/webhook/creem", nil)

			err := handleCreemSubscriptionLifecycleWithConfig(ctx, event, creemSettlementConfigForTest())
			require.NoError(t, err)
			require.Equal(t, 200, recorder.Code)
		})
	}
}

func TestCreemSubscriptionUpdateTrialingOrPaidDoesNotTouchDatabase(t *testing.T) {
	previousDB := model.DB
	model.DB = nil
	t.Cleanup(func() { model.DB = previousDB })

	for _, status := range []string{"trialing", "paid"} {
		t.Run(status, func(t *testing.T) {
			event := &CreemWebhookEvent{Id: "evt_creem_update_" + status, EventType: "subscription.update", CreatedAt: 100}
			event.Object.Mode = "live"
			event.Object.Subscription = []byte(fmt.Sprintf(`{"id":"sub_creem_update_%s","mode":"live"}`, status))
			event.Object.CurrentPeriodEnd = 200
			event.Object.Status = status
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest("POST", "/webhook/creem", nil)

			err := handleCreemSubscriptionLifecycleWithConfig(ctx, event, creemSettlementConfigForTest())
			require.NoError(t, err)
			require.Equal(t, 200, recorder.Code)
		})
	}
}

func TestCreemLifecycleRejectsUnknownEventType(t *testing.T) {
	previousDB := model.DB
	model.DB = nil
	t.Cleanup(func() { model.DB = previousDB })

	event := &CreemWebhookEvent{Id: "evt_creem_unknown", EventType: "subscription.unknown", CreatedAt: 100}
	event.Object.Mode = "live"
	event.Object.Subscription = []byte(`{"id":"sub_creem_unknown","mode":"live"}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest("POST", "/webhook/creem", nil)

	err := handleCreemSubscriptionLifecycleWithConfig(ctx, event, creemSettlementConfigForTest())
	require.ErrorIs(t, err, errCreemEventInvalid)
}

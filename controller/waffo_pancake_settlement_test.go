package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"github.com/waffo-com/waffo-go/config"
	"github.com/waffo-com/waffo-go/core"
	"gorm.io/gorm"
)

func TestIsPermanentPancakeSettlementErrorClassifiesNonRetryableFailures(t *testing.T) {
	permanent := []error{
		model.ErrProviderSettlementInvalid,
		model.ErrProviderSnapshotMissing,
		model.ErrProviderSnapshotMismatch,
		model.ErrProviderEventConflict,
		model.ErrTopUpNotFound,
		model.ErrTopUpStatusInvalid,
		model.ErrInvalidTopUpQuota,
		model.ErrTopUpQuotaLimitExceeded,
		model.ErrWalletQuotaLimitExceeded,
		model.ErrSubscriptionOrderNotFound,
		model.ErrSubscriptionOrderStatusInvalid,
		model.ErrSubscriptionEntitlementSnapshotMissing,
		model.ErrSubscriptionEntitlementSnapshotInvalid,
		errWaffoPancakeCallbackInvalid,
		errWaffoPancakeCallbackMismatch,
	}
	for _, err := range permanent {
		require.Truef(t, isPermanentPancakeSettlementError(err), "expected permanent: %v", err)
	}
	require.False(t, isPermanentPancakeSettlementError(errors.New("database temporarily unavailable")))
}

func validWaffoPancakeTopUpEventAndOrder() (*service.WaffoPancakeWebhookEvent, *model.TopUp) {
	tradeNo := "WAFFO_PANCAKE-42-1-abc123"
	merchantID, storeID, productID, currency, amount, name := "merchant_test", "STO_test", "PROD_test", "USD", "8.00", "new-api-charge-product"
	return &service.WaffoPancakeWebhookEvent{
		ID: "delivery-1", EventID: "PAY_provider_1", StoreID: storeID, Mode: "test", EventType: "order.completed",
		Data: service.WaffoPancakeWebhookData{
			OrderID: "ORD_provider_1", OrderMerchantExternalID: tradeNo,
			MerchantProvidedBuyerIdentity: service.WaffoPancakeBuyerIdentityFromUserID(42),
			Currency:                      currency, Amount: amount, ProductName: name, PaymentID: "PAY_provider_1", PaymentStatus: "succeeded",
			OrderMetadata: newWaffoPancakeOrderMetadata(tradeNo, "topup", productID, merchantID, storeID, currency, amount, name, 42),
		},
	}, &model.TopUp{
		UserId: 42, TradeNo: tradeNo, PaymentProvider: model.PaymentProviderWaffoPancake, PaymentMethod: model.PaymentMethodWaffoPancake,
		Status: common.TopUpStatusPending, CreditedQuota: 500, ProviderMerchantID: merchantID, ProviderStoreID: storeID,
		ProviderProductID: productID, ProviderAmount: amount, ProviderCurrency: currency, ProviderOrderName: name, ProviderCheckoutID: "sess_1",
	}
}

func TestBuildWaffoPancakeTopUpSettlementBindsSnapshot(t *testing.T) {
	event, order := validWaffoPancakeTopUpEventAndOrder()
	settlement, err := buildWaffoPancakeTopUpSettlement(event, order)
	require.NoError(t, err)
	require.Equal(t, model.PaymentProviderWaffoPancake, settlement.Provider)
	require.Equal(t, order.TradeNo, settlement.OrderTradeNo)
	require.Equal(t, "PAY_provider_1", settlement.ProviderTradeNo)
	require.Equal(t, "USD", settlement.Currency)
	// Pancake's signed order.completed payload does not echo the checkout
	// session ID. The local session remains on the order for reconciliation.
	require.Empty(t, settlement.CheckoutID)
}

func TestBuildWaffoPancakeTopUpSettlementRejectsBindingMismatches(t *testing.T) {
	mutations := map[string]func(*service.WaffoPancakeWebhookEvent){
		"external order": func(e *service.WaffoPancakeWebhookEvent) { e.Data.OrderMerchantExternalID = "other" },
		"store":          func(e *service.WaffoPancakeWebhookEvent) { e.StoreID = "STO_other" },
		"amount":         func(e *service.WaffoPancakeWebhookEvent) { e.Data.Amount = "7.99" },
		"currency":       func(e *service.WaffoPancakeWebhookEvent) { e.Data.Currency = "EUR" },
		"product metadata": func(e *service.WaffoPancakeWebhookEvent) {
			e.Data.OrderMetadata[pancakeMetadataProductID] = "PROD_other"
		},
		"kind metadata": func(e *service.WaffoPancakeWebhookEvent) {
			e.Data.OrderMetadata[pancakeMetadataOrderKind] = "subscription"
		},
		"recurring product metadata": func(e *service.WaffoPancakeWebhookEvent) {
			e.Data.OrderMetadata[pancakeMetadataProductType] = "subscription"
		},
		"missing product metadata": func(e *service.WaffoPancakeWebhookEvent) {
			delete(e.Data.OrderMetadata, pancakeMetadataProductType)
		},
		"buyer identity": func(e *service.WaffoPancakeWebhookEvent) {
			e.Data.MerchantProvidedBuyerIdentity = "new-api-user-99"
		},
		"event type": func(e *service.WaffoPancakeWebhookEvent) { e.EventType = "refund.succeeded" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			event, order := validWaffoPancakeTopUpEventAndOrder()
			mutate(event)
			_, err := buildWaffoPancakeTopUpSettlement(event, order)
			require.Error(t, err)
		})
	}
}

func TestBuildWaffoPancakeTopUpSettlementRequiresPaymentAndBusinessEventIDs(t *testing.T) {
	event, order := validWaffoPancakeTopUpEventAndOrder()
	event.Data.PaymentID = ""
	_, err := buildWaffoPancakeTopUpSettlement(event, order)
	require.ErrorIs(t, err, errWaffoPancakeCallbackInvalid)

	event, order = validWaffoPancakeTopUpEventAndOrder()
	event.EventID = "different-payment"
	_, err = buildWaffoPancakeTopUpSettlement(event, order)
	require.ErrorIs(t, err, errWaffoPancakeCallbackInvalid)

	event, order = validWaffoPancakeTopUpEventAndOrder()
	event.EventID = ""
	_, err = buildWaffoPancakeTopUpSettlement(event, order)
	require.ErrorIs(t, err, errWaffoPancakeCallbackInvalid)
}

func TestBuildWaffoPancakeTopUpSettlementDoesNotRequireCheckoutID(t *testing.T) {
	event, order := validWaffoPancakeTopUpEventAndOrder()
	order.ProviderCheckoutID = ""
	settlement, err := buildWaffoPancakeTopUpSettlement(event, order)
	require.NoError(t, err)
	require.Empty(t, settlement.CheckoutID)

	order.ProviderAmount = ""
	_, err = buildWaffoPancakeTopUpSettlement(event, order)
	require.ErrorIs(t, err, model.ErrProviderSnapshotMissing)
}

func TestWaffoPancakeMetadataContainsAllSettlementBindings(t *testing.T) {
	metadata := newWaffoPancakeOrderMetadata("order", "subscription", "product", "merchant", "store", "usd", "9.99", "Plan", 7)
	require.Equal(t, "order", metadata[pancakeMetadataOrderID])
	require.Equal(t, "subscription", metadata[pancakeMetadataOrderKind])
	require.Equal(t, "USD", metadata[pancakeMetadataCurrency])
	require.Equal(t, "7", metadata[pancakeMetadataUserID])
	require.Equal(t, service.WaffoPancakeProductTypeOneTime, metadata[pancakeMetadataProductType])
}

func TestLookupWaffoPancakeOrderDoesNotTurnDatabaseFailureIntoMissingOrder(t *testing.T) {
	originalDB := model.DB
	brokenDB, err := gorm.Open(sqlite.Open("file:controller-pancake-lookup-closed?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := brokenDB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	model.DB = brokenDB
	t.Cleanup(func() { model.DB = originalDB })

	_, _, err = lookupWaffoPancakeOrder("pancake-db-error")
	require.Error(t, err)
	require.NotErrorIs(t, err, model.ErrTopUpNotFound)
	require.NotErrorIs(t, err, model.ErrSubscriptionOrderNotFound)
}

func TestSendWaffoWebhookResponseReturnsNon2xxForFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/waffo/webhook", nil)
	wh := core.NewWebhookHandler(&config.WaffoConfig{})

	sendWaffoWebhookResponse(ctx, wh, false, "settlement failed")
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"message":"failed"`)

	recorder = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/waffo/webhook", nil)
	sendWaffoWebhookResponse(ctx, wh, true, "")
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"message":"success"`)
}

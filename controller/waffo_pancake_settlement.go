package controller

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/shopspring/decimal"
)

func isPermanentPancakeSettlementError(err error) bool {
	return errors.Is(err, model.ErrPaymentMethodMismatch) ||
		errors.Is(err, model.ErrProviderSettlementInvalid) ||
		errors.Is(err, model.ErrProviderSnapshotMissing) ||
		errors.Is(err, model.ErrProviderSnapshotMismatch) ||
		errors.Is(err, model.ErrProviderEventConflict) ||
		errors.Is(err, model.ErrTopUpNotFound) ||
		errors.Is(err, model.ErrTopUpStatusInvalid) ||
		errors.Is(err, model.ErrInvalidTopUpQuota) ||
		errors.Is(err, model.ErrTopUpQuotaLimitExceeded) ||
		errors.Is(err, model.ErrWalletQuotaLimitExceeded) ||
		errors.Is(err, model.ErrSubscriptionOrderNotFound) ||
		errors.Is(err, model.ErrSubscriptionOrderStatusInvalid) ||
		errors.Is(err, model.ErrSubscriptionPurchaseLimitExceeded) ||
		errors.Is(err, model.ErrSubscriptionEntitlementSnapshotMissing) ||
		errors.Is(err, model.ErrSubscriptionEntitlementSnapshotInvalid) ||
		errors.Is(err, errWaffoPancakeCallbackInvalid) ||
		errors.Is(err, errWaffoPancakeCallbackMismatch)
}

var (
	errWaffoPancakeCallbackInvalid  = errors.New("invalid Waffo Pancake webhook event")
	errWaffoPancakeCallbackMismatch = errors.New("Waffo Pancake webhook does not match local order")
)

func waffoPancakeProviderEnvironment(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "test":
		return "test"
	case "prod", "production", "live":
		return "prod"
	default:
		return ""
	}
}

func waffoPancakeProviderScopeFingerprint(merchantID, environment string) string {
	fingerprint, ok := model.ProviderPaymentScopeFingerprint(
		model.PaymentProviderWaffoPancake,
		strings.TrimSpace(merchantID),
		waffoPancakeProviderEnvironment(environment),
	)
	if !ok {
		return ""
	}
	return fingerprint
}

func buildWaffoPancakeRefundEventInput(event *service.WaffoPancakeWebhookEvent, payload string) (model.ProviderRefundEventInput, error) {
	if event == nil || strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.EventID) == "" {
		return model.ProviderRefundEventInput{}, model.ErrProviderRefundInvalid
	}
	eventType := event.NormalizedEventType()
	if eventType != "refund.succeeded" && eventType != "refund.failed" {
		return model.ProviderRefundEventInput{}, model.ErrProviderRefundInvalid
	}
	refundTicketID := strings.TrimSpace(event.Data.RefundTicketMerchantExternalID)
	if refundTicketID == "" {
		return model.ProviderRefundEventInput{}, model.ErrProviderRefundInvalid
	}
	refundStatus := strings.ToLower(strings.TrimSpace(event.Data.RefundStatus))
	if refundStatus == "" {
		return model.ProviderRefundEventInput{}, model.ErrProviderRefundInvalid
	}
	if eventType == "refund.succeeded" && refundStatus != "succeeded" {
		return model.ProviderRefundEventInput{}, model.ErrProviderRefundInvalid
	}
	if eventType == "refund.failed" && refundStatus != "failed" {
		return model.ProviderRefundEventInput{}, model.ErrProviderRefundInvalid
	}
	orderKind := strings.TrimSpace(event.Data.OrderMetadata[pancakeMetadataOrderKind])
	return model.ProviderRefundEventInput{
		Provider:                       model.PaymentProviderWaffoPancake,
		DeliveryID:                     event.ID,
		BusinessEventID:                event.EventID,
		RefundTicketMerchantExternalID: refundTicketID,
		OrderTradeNo:                   strings.TrimSpace(event.Data.OrderMerchantExternalID),
		OrderKind:                      orderKind,
		EventType:                      eventType,
		RefundStatus:                   refundStatus,
		Reason:                         strings.TrimSpace(event.Data.RefundReason),
		Payload:                        payload,
	}, nil
}

// lookupWaffoPancakeOrder keeps the webhook's order-type dispatch fail-closed.
// A missing row is a permanent, non-actionable event and is represented by
// nil values; any other database error is returned so the handler can ask the
// provider to retry instead of acknowledging a payment it could not inspect.
func lookupWaffoPancakeOrder(tradeNo string) (*model.TopUp, *model.SubscriptionOrder, error) {
	topUp, topUpErr := model.GetTopUpByTradeNoWithError(tradeNo)
	if topUpErr != nil && !errors.Is(topUpErr, model.ErrTopUpNotFound) {
		return nil, nil, topUpErr
	}
	if errors.Is(topUpErr, model.ErrTopUpNotFound) {
		topUp = nil
	}

	subscriptionOrder, subscriptionErr := model.GetSubscriptionOrderByTradeNoWithError(tradeNo)
	if subscriptionErr != nil && !errors.Is(subscriptionErr, model.ErrSubscriptionOrderNotFound) {
		return nil, nil, subscriptionErr
	}
	if errors.Is(subscriptionErr, model.ErrSubscriptionOrderNotFound) {
		subscriptionOrder = nil
	}
	return topUp, subscriptionOrder, nil
}

const (
	pancakeMetadataOrderID        = "new_api_order_id"
	pancakeMetadataOrderKind      = "new_api_order_kind"
	pancakeMetadataProductType    = service.WaffoPancakeProductTypeMetadataKey
	pancakeMetadataProductID      = "new_api_product_id"
	pancakeMetadataMerchantID     = "new_api_merchant_id"
	pancakeMetadataStoreID        = "new_api_store_id"
	pancakeMetadataCurrency       = "new_api_currency"
	pancakeMetadataProviderAmount = "new_api_provider_amount"
	pancakeMetadataOrderName      = "new_api_order_name"
	pancakeMetadataUserID         = "new_api_user_id"
)

func newWaffoPancakeOrderMetadata(tradeNo, kind, productID, merchantID, storeID, currency, amount, orderName string, userID int) map[string]string {
	return map[string]string{
		pancakeMetadataOrderID:        strings.TrimSpace(tradeNo),
		pancakeMetadataOrderKind:      strings.TrimSpace(kind),
		pancakeMetadataProductType:    service.WaffoPancakeProductTypeOneTime,
		pancakeMetadataProductID:      strings.TrimSpace(productID),
		pancakeMetadataMerchantID:     strings.TrimSpace(merchantID),
		pancakeMetadataStoreID:        strings.TrimSpace(storeID),
		pancakeMetadataCurrency:       strings.ToUpper(strings.TrimSpace(currency)),
		pancakeMetadataProviderAmount: strings.TrimSpace(amount),
		pancakeMetadataOrderName:      strings.TrimSpace(orderName),
		pancakeMetadataUserID:         fmt.Sprintf("%d", userID),
	}
}

// buildWaffoPancakeTopUpSettlement verifies a signed order.completed event
// against the immutable top-up checkout snapshot. Pancake does not echo the
// checkout session id in webhook data, so CheckoutID is intentionally left
// empty; the stored session id remains an audit/reconciliation field and the
// signed order metadata, external id, product, store, amount, and payment id
// provide the binding that is available from the provider.
func buildWaffoPancakeTopUpSettlement(event *service.WaffoPancakeWebhookEvent, topUp *model.TopUp) (model.ProviderSettlement, error) {
	if topUp == nil {
		return model.ProviderSettlement{}, model.ErrTopUpNotFound
	}
	if topUp.CreditedQuota <= 0 || strings.TrimSpace(topUp.ProviderMerchantID) == "" ||
		strings.TrimSpace(topUp.ProviderStoreID) == "" || strings.TrimSpace(topUp.ProviderProductID) == "" ||
		strings.TrimSpace(topUp.ProviderAmount) == "" || strings.TrimSpace(topUp.ProviderCurrency) == "" ||
		strings.TrimSpace(topUp.ProviderOrderName) == "" {
		return model.ProviderSettlement{}, model.ErrProviderSnapshotMissing
	}
	return buildWaffoPancakeSettlement(event, topUp.TradeNo, "topup", topUp.UserId,
		topUp.ProviderMerchantID, topUp.ProviderStoreID, topUp.ProviderProductID,
		topUp.ProviderAmount, topUp.ProviderCurrency, topUp.ProviderOrderName,
		topUp.ProviderCheckoutID, func() error {
			if topUp.PaymentProvider != model.PaymentProviderWaffoPancake {
				return model.ErrPaymentMethodMismatch
			}
			if topUp.CreditedQuota <= 0 {
				return model.ErrProviderSnapshotMissing
			}
			return nil
		}())
}

// buildWaffoPancakeProviderSettlement is kept as the short top-up helper
// name used by adapters and tests.
func buildWaffoPancakeProviderSettlement(event *service.WaffoPancakeWebhookEvent, topUp *model.TopUp) (model.ProviderSettlement, error) {
	return buildWaffoPancakeTopUpSettlement(event, topUp)
}

func buildWaffoPancakeSubscriptionSettlement(event *service.WaffoPancakeWebhookEvent, order *model.SubscriptionOrder) (model.ProviderSettlement, error) {
	if order == nil {
		return model.ProviderSettlement{}, model.ErrSubscriptionOrderNotFound
	}
	if strings.TrimSpace(order.ProviderMerchantID) == "" || strings.TrimSpace(order.ProviderStoreID) == "" ||
		strings.TrimSpace(order.ProviderProductID) == "" || strings.TrimSpace(order.ProviderAmount) == "" ||
		strings.TrimSpace(order.ProviderCurrency) == "" || strings.TrimSpace(order.ProviderOrderName) == "" {
		return model.ProviderSettlement{}, model.ErrProviderSnapshotMissing
	}
	return buildWaffoPancakeSettlement(event, order.TradeNo, "subscription", order.UserId,
		order.ProviderMerchantID, order.ProviderStoreID, order.ProviderProductID,
		order.ProviderAmount, order.ProviderCurrency, order.ProviderOrderName,
		order.ProviderCheckoutID, func() error {
			if order.PaymentProvider != model.PaymentProviderWaffoPancake {
				return model.ErrPaymentMethodMismatch
			}
			return nil
		}())
}

func buildWaffoPancakeSettlement(event *service.WaffoPancakeWebhookEvent, tradeNo, kind string, userID int, merchantID, storeID, productID, expectedAmount, expectedCurrency, expectedName, checkoutID string, precondition error) (model.ProviderSettlement, error) {
	if precondition != nil {
		return model.ProviderSettlement{}, precondition
	}
	if event == nil || strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.StoreID) == "" {
		return model.ProviderSettlement{}, errWaffoPancakeCallbackInvalid
	}
	if event.NormalizedEventType() != "order.completed" {
		return model.ProviderSettlement{}, errWaffoPancakeCallbackInvalid
	}
	mode := strings.ToLower(strings.TrimSpace(event.Mode))
	if mode != "test" && mode != "prod" {
		return model.ProviderSettlement{}, errWaffoPancakeCallbackInvalid
	}
	providerEnvironment := waffoPancakeProviderEnvironment(mode)
	if strings.TrimSpace(event.Data.OrderID) == "" || strings.TrimSpace(event.Data.OrderMerchantExternalID) == "" {
		return model.ProviderSettlement{}, errWaffoPancakeCallbackInvalid
	}
	if strings.TrimSpace(event.Data.OrderMerchantExternalID) != strings.TrimSpace(tradeNo) {
		return model.ProviderSettlement{}, errWaffoPancakeCallbackMismatch
	}
	if strings.TrimSpace(event.Data.MerchantProvidedBuyerIdentity) != service.WaffoPancakeBuyerIdentityFromUserID(userID) {
		return model.ProviderSettlement{}, errWaffoPancakeCallbackMismatch
	}
	if strings.TrimSpace(merchantID) == "" || strings.TrimSpace(storeID) == "" || strings.TrimSpace(productID) == "" || strings.TrimSpace(expectedAmount) == "" || strings.TrimSpace(expectedCurrency) == "" {
		return model.ProviderSettlement{}, model.ErrProviderSnapshotMissing
	}
	if strings.TrimSpace(event.StoreID) != strings.TrimSpace(storeID) {
		return model.ProviderSettlement{}, errWaffoPancakeCallbackMismatch
	}

	metadata := event.Data.OrderMetadata
	expectedMetadata := map[string]string{
		pancakeMetadataOrderID:        strings.TrimSpace(tradeNo),
		pancakeMetadataOrderKind:      kind,
		pancakeMetadataProductType:    service.WaffoPancakeProductTypeOneTime,
		pancakeMetadataProductID:      strings.TrimSpace(productID),
		pancakeMetadataMerchantID:     strings.TrimSpace(merchantID),
		pancakeMetadataStoreID:        strings.TrimSpace(storeID),
		pancakeMetadataCurrency:       strings.ToUpper(strings.TrimSpace(expectedCurrency)),
		pancakeMetadataProviderAmount: strings.TrimSpace(expectedAmount),
		pancakeMetadataOrderName:      strings.TrimSpace(expectedName),
		pancakeMetadataUserID:         fmt.Sprintf("%d", userID),
	}
	for key, expected := range expectedMetadata {
		if strings.TrimSpace(metadata[key]) != expected {
			return model.ProviderSettlement{}, errWaffoPancakeCallbackMismatch
		}
	}
	// Product metadata is provider-side configuration. If it carries any of
	// our binding keys, it must agree with the checkout metadata as well.
	for key, expected := range expectedMetadata {
		if value := strings.TrimSpace(event.Data.ProductMetadata[key]); value != "" && value != expected {
			return model.ProviderSettlement{}, errWaffoPancakeCallbackMismatch
		}
	}

	currency := strings.ToUpper(strings.TrimSpace(event.Data.Currency))
	if currency == "" || currency != strings.ToUpper(strings.TrimSpace(expectedCurrency)) {
		return model.ProviderSettlement{}, errWaffoPancakeCallbackMismatch
	}
	if !comparePancakeAmounts(expectedAmount, event.Data.Amount) || !comparePancakeAmounts(expectedAmount, metadata[pancakeMetadataProviderAmount]) {
		return model.ProviderSettlement{}, errWaffoPancakeCallbackMismatch
	}
	if strings.TrimSpace(event.Data.ProductName) == "" || strings.TrimSpace(expectedName) == "" || strings.TrimSpace(event.Data.ProductName) != strings.TrimSpace(expectedName) {
		return model.ProviderSettlement{}, errWaffoPancakeCallbackMismatch
	}
	// The SDK contract includes payment fields on order.completed. Do not
	// substitute an order/delivery ID when PaymentID is absent: that would turn
	// a non-transaction identifier into the idempotency key and could bind an
	// unrelated event. Likewise, an omitted status is not proof of payment.
	paymentID := strings.TrimSpace(event.Data.PaymentID)
	if paymentID == "" {
		return model.ProviderSettlement{}, errWaffoPancakeCallbackInvalid
	}
	if strings.TrimSpace(event.EventID) == "" || strings.TrimSpace(event.EventID) != paymentID {
		return model.ProviderSettlement{}, errWaffoPancakeCallbackInvalid
	}
	paymentStatus := strings.ToLower(strings.TrimSpace(event.Data.PaymentStatus))
	if paymentStatus != "succeeded" && paymentStatus != "paid" && paymentStatus != "complete" && paymentStatus != "completed" {
		return model.ProviderSettlement{}, errWaffoPancakeCallbackInvalid
	}
	if orderStatus := strings.ToLower(strings.TrimSpace(event.Data.OrderStatus)); orderStatus != "" && orderStatus != "completed" && orderStatus != "paid" && orderStatus != "active" {
		return model.ProviderSettlement{}, errWaffoPancakeCallbackInvalid
	}
	providerTradeNo := paymentID
	providerAccountID := strings.TrimSpace(merchantID)
	providerKeyFingerprint := waffoPancakeProviderScopeFingerprint(providerAccountID, providerEnvironment)
	if providerKeyFingerprint == "" {
		return model.ProviderSettlement{}, errWaffoPancakeCallbackInvalid
	}
	// ProviderEventID stores the delivery ID for diagnostics/reconciliation;
	// EventID is independently checked above as the business-event/payment ID.
	providerEventID := strings.TrimSpace(event.ID)
	settlement := model.ProviderSettlement{
		OrderTradeNo:           strings.TrimSpace(tradeNo),
		Provider:               model.PaymentProviderWaffoPancake,
		ProviderTradeNo:        providerTradeNo,
		ProviderEventID:        providerEventID,
		ProviderAccountID:      providerAccountID,
		ProviderEnvironment:    providerEnvironment,
		ProviderKeyFingerprint: providerKeyFingerprint,
		MerchantID:             providerAccountID,
		ProductID:              strings.TrimSpace(productID),
		StoreID:                strings.TrimSpace(storeID),
		// Pancake's webhook schema has no checkout-session field. Keep the
		// local session id in the order for reconciliation, but don't pretend
		// that it was independently echoed by this event. An empty value also
		// prevents the model validator from comparing a local-only ID to a
		// provider order ID with different semantics.
		CheckoutID: "",
		Currency:   currency,
		Amount:     strings.TrimSpace(event.Data.Amount),
		OrderName:  strings.TrimSpace(event.Data.ProductName),
	}
	if err := model.ValidateProviderSettlement(settlement); err != nil {
		return model.ProviderSettlement{}, errWaffoPancakeCallbackInvalid
	}
	return settlement, nil
}

func comparePancakeAmounts(expected, actual string) bool {
	a, errA := decimal.NewFromString(strings.TrimSpace(expected))
	b, errB := decimal.NewFromString(strings.TrimSpace(actual))
	return errA == nil && errB == nil && a.IsPositive() && b.IsPositive() && a.Equal(b)
}

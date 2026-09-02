package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
)

// creemProviderOwnedReversalSubscriptionID deliberately excludes metadata:
// checkout metadata can be supplied by a customer and therefore cannot prove
// which local order owns a refund or chargeback. Only provider-owned expanded
// payment objects are eligible aliases for an automatic reversal.
func creemProviderOwnedReversalSubscriptionID(event *CreemWebhookEvent) string {
	if event == nil {
		return ""
	}
	if len(event.Object.Subscription) > 0 {
		var object struct {
			ID string `json:"id"`
		}
		if common.Unmarshal(event.Object.Subscription, &object) == nil && isCreemSubscriptionReference(object.ID) {
			return strings.TrimSpace(object.ID)
		}
		var id string
		if common.Unmarshal(event.Object.Subscription, &id) == nil && isCreemSubscriptionReference(id) {
			return strings.TrimSpace(id)
		}
	}
	if id := strings.TrimSpace(event.Object.Transaction.Subscription); isCreemSubscriptionReference(id) {
		return id
	}
	return ""
}

func creemReversalPaymentObjects(event *CreemWebhookEvent) ([]model.ProviderPaymentObject, error) {
	if event == nil {
		return nil, errCreemEventInvalid
	}
	objects := make([]model.ProviderPaymentObject, 0, 4)
	seen := make(map[string]struct{}, 4)
	add := func(objectType, objectID string) error {
		objectID = strings.TrimSpace(objectID)
		if objectID == "" {
			return nil
		}
		if len(objectID) > 255 {
			return errCreemEventInvalid
		}
		key := objectType + "\x00" + objectID
		if _, ok := seen[key]; ok {
			return nil
		}
		seen[key] = struct{}{}
		objects = append(objects, model.ProviderPaymentObject{ObjectType: objectType, ObjectID: objectID})
		return nil
	}
	if err := add(model.ProviderPaymentObjectTransaction, event.Object.Transaction.Id); err != nil {
		return nil, err
	}
	if err := add(model.ProviderPaymentObjectOrder, event.Object.Order.Id); err != nil {
		return nil, err
	}
	if err := add(model.ProviderPaymentObjectCheckoutSession, event.Object.Checkout.Id); err != nil {
		return nil, err
	}
	if err := add(model.ProviderPaymentObjectSubscription, creemProviderOwnedReversalSubscriptionID(event)); err != nil {
		return nil, err
	}
	if len(objects) == 0 {
		return nil, errCreemEventInvalid
	}
	return objects, nil
}

func validateCreemReversalObjectLinks(event *CreemWebhookEvent) error {
	expectedObjectType := "refund"
	if event.EventType == "dispute.created" {
		expectedObjectType = "dispute"
	}
	if objectType := strings.ToLower(strings.TrimSpace(event.Object.Object)); objectType != "" && objectType != expectedObjectType {
		return errCreemEventMismatch
	}
	if objectType := strings.ToLower(strings.TrimSpace(event.Object.Transaction.Object)); objectType != "" && objectType != "transaction" {
		return errCreemEventMismatch
	}
	if objectType := strings.ToLower(strings.TrimSpace(event.Object.Order.Object)); objectType != "" && objectType != "order" {
		return errCreemEventMismatch
	}
	transactionID := strings.TrimSpace(event.Object.Transaction.Id)
	orderID := strings.TrimSpace(event.Object.Order.Id)
	if transactionID == "" || orderID == "" {
		return errCreemEventInvalid
	}
	if linked := strings.TrimSpace(event.Object.Order.Transaction); linked != "" && linked != transactionID {
		return errCreemEventMismatch
	}
	if linked := strings.TrimSpace(event.Object.Transaction.Order); linked == "" || linked != orderID {
		return errCreemEventMismatch
	}
	transactionSubscription := strings.TrimSpace(event.Object.Transaction.Subscription)
	subscriptionID := creemProviderOwnedReversalSubscriptionID(event)
	if transactionSubscription != "" && (!isCreemSubscriptionReference(transactionSubscription) || (subscriptionID != "" && subscriptionID != transactionSubscription)) {
		return errCreemEventMismatch
	}
	return nil
}

func creemReversalCurrency(event *CreemWebhookEvent) (string, error) {
	if event == nil {
		return "", errCreemEventInvalid
	}
	candidates := []string{
		event.Object.Transaction.Currency,
		event.Object.Order.Currency,
	}
	if event.EventType == "refund.created" {
		if strings.TrimSpace(event.Object.RefundCurrency) == "" {
			return "", errCreemEventInvalid
		}
		candidates = append(candidates, event.Object.RefundCurrency)
	} else {
		if strings.TrimSpace(event.Object.Currency) == "" {
			return "", errCreemEventInvalid
		}
		candidates = append(candidates, event.Object.Currency)
	}
	currency := ""
	for _, candidate := range candidates {
		candidate = strings.ToUpper(strings.TrimSpace(candidate))
		if candidate == "" {
			continue
		}
		if !isValidCreemCurrencyCode(candidate) {
			return "", errCreemEventInvalid
		}
		if currency != "" && currency != candidate {
			return "", errCreemEventMismatch
		}
		currency = candidate
	}
	if currency == "" {
		return "", errCreemEventInvalid
	}
	return currency, nil
}

func buildCreemReversalEventInput(event *CreemWebhookEvent, payload string) (model.ProviderRefundEventInput, []model.ProviderPaymentObject, error) {
	return buildCreemReversalEventInputWithConfig(event, payload, setting.GetCreemConfig())
}

func buildCreemReversalEventInputWithConfig(event *CreemWebhookEvent, payload string, cfg setting.CreemConfig) (model.ProviderRefundEventInput, []model.ProviderPaymentObject, error) {
	if event == nil || strings.TrimSpace(event.Id) == "" || event.CreatedAt <= 0 || len(strings.TrimSpace(event.Id)) > 255 {
		return model.ProviderRefundEventInput{}, nil, errCreemEventInvalid
	}
	if event.EventType != "refund.created" && event.EventType != "dispute.created" {
		return model.ProviderRefundEventInput{}, nil, errCreemEventInvalid
	}
	if err := validateCreemEventMode(event, cfg); err != nil {
		return model.ProviderRefundEventInput{}, nil, err
	}
	providerAccountID := creemMerchantSnapshotWithConfig(cfg)
	providerEnvironment := creemProviderEnvironment(cfg)
	if providerAccountID == "" || providerEnvironment == "" {
		return model.ProviderRefundEventInput{}, nil, errCreemEventInvalid
	}
	if err := validateCreemReversalObjectLinks(event); err != nil {
		return model.ProviderRefundEventInput{}, nil, err
	}
	objects, err := creemReversalPaymentObjects(event)
	if err != nil {
		return model.ProviderRefundEventInput{}, nil, err
	}
	currency, err := creemReversalCurrency(event)
	if err != nil {
		return model.ProviderRefundEventInput{}, nil, err
	}
	transactionAmount := event.Object.Transaction.Amount
	amountPaid := event.Object.Transaction.AmountPaid
	orderAmount := int64(event.Object.Order.Amount)
	orderAmountPaid := int64(event.Object.Order.AmountPaid)
	if transactionAmount <= 0 || amountPaid < transactionAmount || orderAmount <= 0 || transactionAmount != orderAmount ||
		(orderAmountPaid > 0 && orderAmountPaid != amountPaid) {
		return model.ProviderRefundEventInput{}, nil, errCreemEventMismatch
	}
	if status := strings.ToLower(strings.TrimSpace(event.Object.Order.Status)); status != "" && status != "paid" {
		return model.ProviderRefundEventInput{}, nil, errCreemEventMismatch
	}
	if strings.TrimSpace(payload) == "" {
		payload = common.GetJsonString(event)
	}
	input := model.ProviderRefundEventInput{
		Provider:            model.PaymentProviderCreem,
		ProviderAccountID:   providerAccountID,
		ProviderEnvironment: providerEnvironment,
		DeliveryID:          strings.TrimSpace(event.Id),
		BusinessEventID:     strings.TrimSpace(event.Object.Id),
		EffectID:            strings.TrimSpace(event.Object.Id),
		ProviderObjectType:  model.ProviderPaymentObjectTransaction,
		ProviderTradeNo:     strings.TrimSpace(event.Object.Transaction.Id),
		Amount:              stripeAmountFromMinorUnits(transactionAmount, currency),
		Currency:            currency,
		Reason:              strings.TrimSpace(event.Object.Reason),
		Payload:             payload,
	}
	if input.EffectID == "" || len(input.EffectID) > 255 {
		return model.ProviderRefundEventInput{}, nil, errCreemEventInvalid
	}

	switch event.EventType {
	case "refund.created":
		input.RefundTicketMerchantExternalID = input.EffectID
		refundAmount := event.Object.RefundAmount
		refundedAmount := event.Object.Transaction.RefundedAmount
		if refundAmount <= 0 || refundAmount > amountPaid || refundedAmount < 0 || refundedAmount > amountPaid {
			return model.ProviderRefundEventInput{}, nil, errCreemEventMismatch
		}
		switch strings.ToLower(strings.TrimSpace(event.Object.Status)) {
		case "succeeded":
			if refundedAmount <= 0 {
				return model.ProviderRefundEventInput{}, nil, errCreemEventMismatch
			}
			input.EventType = "refund.succeeded"
			input.RefundStatus = "succeeded"
			if refundAmount != amountPaid || refundedAmount != amountPaid {
				input.ForceManualReason = model.ProviderRefundManualReasonPartialRefundUnsupported
				input.Amount = stripeAmountFromMinorUnits(refundAmount, currency)
			}
		case "failed", "canceled", "cancelled":
			input.EventType = "refund.failed"
			input.RefundStatus = "failed"
			input.Amount = stripeAmountFromMinorUnits(refundAmount, currency)
		case "pending", "requiresaction", "requires_action":
			input.EventType = "refund.observed"
			input.RefundStatus = strings.ToLower(strings.TrimSpace(event.Object.Status))
			input.ForceManualReason = model.ProviderRefundManualReasonNonTerminal
			input.Amount = stripeAmountFromMinorUnits(refundAmount, currency)
		default:
			return model.ProviderRefundEventInput{}, nil, errCreemEventInvalid
		}
	case "dispute.created":
		// Creem exposes no later funds-withdrawn event. A dispute.created payload
		// carries the withdrawn amount and immutable payment objects, so it is the
		// only provider event from which the local reversal can be driven.
		disputeAmount := event.Object.Amount
		refundedAmount := event.Object.Transaction.RefundedAmount
		if disputeAmount <= 0 || disputeAmount > amountPaid || refundedAmount < 0 || refundedAmount > amountPaid {
			return model.ProviderRefundEventInput{}, nil, errCreemEventMismatch
		}
		input.EventType = "dispute.funds_withdrawn"
		transactionStatus := strings.ToLower(strings.TrimSpace(event.Object.Transaction.Status))
		transactionStatus = strings.NewReplacer("_", "", "-", "").Replace(transactionStatus)
		chargebackConfirmed := transactionStatus == "chargeback" || transactionStatus == "chargedback"
		if disputeAmount != amountPaid || refundedAmount != amountPaid ||
			!chargebackConfirmed {
			input.ForceManualReason = model.ProviderRefundManualReasonPartialRefundUnsupported
			input.Amount = stripeAmountFromMinorUnits(disputeAmount, currency)
		}
	}
	return input, objects, nil
}

func resolveCreemReversalBinding(input *model.ProviderRefundEventInput, objects []model.ProviderPaymentObject) (string, error) {
	if input == nil || len(objects) == 0 {
		return "", errCreemEventInvalid
	}
	var selected *model.ProviderPaymentBinding
	var selectedObject model.ProviderPaymentObject
	for _, object := range objects {
		binding, err := model.FindProviderPaymentBinding(
			input.Provider,
			input.ProviderAccountID,
			input.ProviderEnvironment,
			object.ObjectType,
			object.ObjectID,
		)
		switch {
		case errors.Is(err, model.ErrProviderPaymentBindingNotFound):
			continue
		case errors.Is(err, model.ErrProviderPaymentBindingConflict):
			return model.ProviderRefundManualReasonPaymentBindingConflict, nil
		case err != nil:
			return "", err
		}
		if binding.OrderKind != model.PaymentEventOrderTopUp && binding.OrderKind != model.PaymentEventOrderSubscription {
			return model.ProviderRefundManualReasonPaymentBindingConflict, nil
		}
		if selected != nil && (selected.OrderTradeNo != binding.OrderTradeNo || selected.OrderKind != binding.OrderKind) {
			return model.ProviderRefundManualReasonPaymentBindingConflict, nil
		}
		if selected == nil {
			selected = binding
			selectedObject = object
		}
	}
	if selected == nil {
		return model.ProviderRefundManualReasonPaymentBindingMissing, nil
	}
	// Older checkout.completed payloads did not expose the transaction ID, so a
	// later refund can legitimately contain one unbound alias. Every alias that
	// does resolve was checked above and must name this same owner; requiring all
	// aliases would make documented historical payments impossible to reverse.
	input.OrderTradeNo = selected.OrderTradeNo
	input.OrderKind = selected.OrderKind
	input.ProviderObjectType = selectedObject.ObjectType
	input.ProviderTradeNo = selectedObject.ObjectID
	return "", nil
}

func handleCreemReversalEvent(ctx context.Context, event *CreemWebhookEvent, payload string) error {
	return handleCreemReversalEventWithConfig(ctx, event, payload, setting.GetCreemConfig())
}

func handleCreemReversalEventWithConfig(ctx context.Context, event *CreemWebhookEvent, payload string, cfg setting.CreemConfig) error {
	input, objects, err := buildCreemReversalEventInputWithConfig(event, payload, cfg)
	if err != nil {
		return err
	}
	manualReason, err := resolveCreemReversalBinding(&input, objects)
	if err != nil {
		return err
	}
	if manualReason != "" && input.EventType != "refund.failed" && input.EventType != "refund.observed" {
		input.ForceManualReason = manualReason
	}
	result, err := model.ProcessProviderRefundEvent(input)
	if err != nil {
		return err
	}
	logger.LogInfo(ctx, fmt.Sprintf(
		"Creem reversal delivery processed event_id=%s effect_id=%s event_type=%s status=%s already_processed=%t",
		input.DeliveryID,
		input.EffectID,
		input.EventType,
		result.Status,
		result.AlreadyProcessed,
	))
	return nil
}

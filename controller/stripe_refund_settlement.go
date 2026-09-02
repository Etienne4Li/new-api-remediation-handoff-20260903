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
	"github.com/stripe/stripe-go/v81"
)

func stripeReversalPaymentObjects(paymentIntent *stripe.PaymentIntent, charge *stripe.Charge) []model.ProviderPaymentObject {
	objects := make([]model.ProviderPaymentObject, 0, 2)
	// Charge is the exact object whose funds were refunded or disputed. Keep the
	// PaymentIntent as a fallback and cross-check for older settlements that did
	// not persist the charge alias.
	if charge != nil && strings.TrimSpace(charge.ID) != "" {
		objects = append(objects, model.ProviderPaymentObject{
			ObjectType: model.ProviderPaymentObjectCharge,
			ObjectID:   strings.TrimSpace(charge.ID),
		})
	}
	if paymentIntent != nil && strings.TrimSpace(paymentIntent.ID) != "" {
		objects = append(objects, model.ProviderPaymentObject{
			ObjectType: model.ProviderPaymentObjectPaymentIntent,
			ObjectID:   strings.TrimSpace(paymentIntent.ID),
		})
	}
	return objects
}

type stripeReversalDelivery struct {
	input      model.ProviderRefundEventInput
	objects    []model.ProviderPaymentObject
	actionable bool
}

func buildStripeReversalBaseInput(event stripe.Event, payload string, stripeConfig setting.StripeConfig) (model.ProviderRefundEventInput, error) {
	if strings.TrimSpace(event.ID) == "" || event.Data == nil || len(event.Data.Raw) == 0 {
		return model.ProviderRefundEventInput{}, errStripeCheckoutInvalid
	}
	live, err := stripeLiveModeWithConfig(stripeConfig)
	if err != nil || event.Livemode != live {
		return model.ProviderRefundEventInput{}, errStripeCheckoutMode
	}
	providerAccountID := stripeProviderAccountID(event, stripeStandardAccountScope(stripeConfig))
	if providerAccountID == "" {
		return model.ProviderRefundEventInput{}, errStripeCheckoutInvalid
	}
	if strings.TrimSpace(payload) == "" {
		payload = string(event.Data.Raw)
	}

	return model.ProviderRefundEventInput{
		Provider:            model.PaymentProviderStripe,
		ProviderAccountID:   providerAccountID,
		ProviderEnvironment: stripeProviderEnvironment(live),
		DeliveryID:          strings.TrimSpace(event.ID),
		Payload:             payload,
	}, nil
}

func buildStripeRefundReversalDelivery(base model.ProviderRefundEventInput, sourceEventType stripe.EventType, refund *stripe.Refund, objects []model.ProviderPaymentObject) (stripeReversalDelivery, error) {
	if refund == nil {
		return stripeReversalDelivery{}, errStripeCheckoutInvalid
	}
	currency := strings.ToUpper(strings.TrimSpace(string(refund.Currency)))
	if strings.TrimSpace(refund.ID) == "" || refund.Amount <= 0 || !isValidStripeCurrencyCode(currency) {
		return stripeReversalDelivery{}, errStripeCheckoutInvalid
	}
	if len(objects) == 0 {
		objects = stripeReversalPaymentObjects(refund.PaymentIntent, refund.Charge)
	}
	if len(objects) == 0 {
		return stripeReversalDelivery{}, errStripeCheckoutInvalid
	}
	input := base
	input.BusinessEventID = strings.TrimSpace(refund.ID)
	input.RefundTicketMerchantExternalID = strings.TrimSpace(refund.ID)
	input.EffectID = strings.TrimSpace(refund.ID)
	input.Amount = stripeAmountFromMinorUnits(refund.Amount, currency)
	input.Currency = currency
	input.Reason = strings.TrimSpace(string(refund.FailureReason))
	if input.Reason == "" {
		input.Reason = strings.TrimSpace(string(refund.Reason))
	}
	actionable := true
	switch refund.Status {
	case stripe.RefundStatusSucceeded:
		if sourceEventType == stripe.EventTypeRefundFailed {
			return stripeReversalDelivery{}, errStripeCheckoutInvalid
		}
		input.EventType = "refund.succeeded"
		input.RefundStatus = "succeeded"
	case stripe.RefundStatusFailed, stripe.RefundStatusCanceled:
		input.EventType = "refund.failed"
		input.RefundStatus = "failed"
	case stripe.RefundStatusPending, stripe.RefundStatusRequiresAction:
		if sourceEventType == stripe.EventTypeRefundFailed {
			return stripeReversalDelivery{}, errStripeCheckoutInvalid
		}
		actionable = false
	default:
		return stripeReversalDelivery{}, errStripeCheckoutInvalid
	}
	input.ProviderObjectType = objects[0].ObjectType
	input.ProviderTradeNo = objects[0].ObjectID
	return stripeReversalDelivery{input: input, objects: objects, actionable: actionable}, nil
}

func buildStripeReversalEventInput(event stripe.Event, payload string) (model.ProviderRefundEventInput, []model.ProviderPaymentObject, bool, error) {
	return buildStripeReversalEventInputWithConfig(event, payload, setting.GetStripeConfig())
}

func buildStripeReversalEventInputWithConfig(event stripe.Event, payload string, stripeConfig setting.StripeConfig) (model.ProviderRefundEventInput, []model.ProviderPaymentObject, bool, error) {
	input, err := buildStripeReversalBaseInput(event, payload, stripeConfig)
	if err != nil {
		return model.ProviderRefundEventInput{}, nil, false, err
	}
	var objects []model.ProviderPaymentObject
	actionable := true

	switch event.Type {
	case stripe.EventTypeRefundCreated, stripe.EventTypeRefundUpdated,
		stripe.EventTypeRefundFailed, stripe.EventTypeChargeRefundUpdated:
		var refund stripe.Refund
		if err := common.Unmarshal(event.Data.Raw, &refund); err != nil {
			return model.ProviderRefundEventInput{}, nil, false, fmt.Errorf("%w: decode Stripe refund: %v", errStripeCheckoutInvalid, err)
		}
		delivery, deliveryErr := buildStripeRefundReversalDelivery(input, event.Type, &refund, nil)
		if deliveryErr != nil {
			return model.ProviderRefundEventInput{}, nil, false, deliveryErr
		}
		return delivery.input, delivery.objects, delivery.actionable, nil

	case stripe.EventTypeChargeDisputeFundsWithdrawn, stripe.EventTypeChargeDisputeFundsReinstated:
		var dispute stripe.Dispute
		if err := common.Unmarshal(event.Data.Raw, &dispute); err != nil {
			return model.ProviderRefundEventInput{}, nil, false, fmt.Errorf("%w: decode Stripe dispute: %v", errStripeCheckoutInvalid, err)
		}
		currency := strings.ToUpper(strings.TrimSpace(string(dispute.Currency)))
		if strings.TrimSpace(dispute.ID) == "" || dispute.Amount <= 0 || dispute.Livemode != event.Livemode || !isValidStripeCurrencyCode(currency) {
			return model.ProviderRefundEventInput{}, nil, false, errStripeCheckoutInvalid
		}
		objects = stripeReversalPaymentObjects(dispute.PaymentIntent, dispute.Charge)
		if len(objects) == 0 {
			return model.ProviderRefundEventInput{}, nil, false, errStripeCheckoutInvalid
		}
		input.BusinessEventID = strings.TrimSpace(dispute.ID)
		input.EffectID = strings.TrimSpace(dispute.ID)
		input.Amount = stripeAmountFromMinorUnits(dispute.Amount, currency)
		input.Currency = currency
		input.Reason = strings.TrimSpace(string(dispute.Reason))
		if event.Type == stripe.EventTypeChargeDisputeFundsWithdrawn {
			input.EventType = "dispute.funds_withdrawn"
		} else {
			input.EventType = "dispute.funds_reinstated"
			input.RelatedEffectID = input.EffectID
		}

	default:
		return model.ProviderRefundEventInput{}, nil, false, errStripeCheckoutInvalid
	}

	input.ProviderObjectType = objects[0].ObjectType
	input.ProviderTradeNo = objects[0].ObjectID
	return input, objects, actionable, nil
}

func buildStripeChargeRefundedDeliveries(event stripe.Event, payload string) ([]stripeReversalDelivery, error) {
	return buildStripeChargeRefundedDeliveriesWithConfig(event, payload, setting.GetStripeConfig())
}

func buildStripeChargeRefundedDeliveriesWithConfig(event stripe.Event, payload string, stripeConfig setting.StripeConfig) ([]stripeReversalDelivery, error) {
	if event.Type != stripe.EventTypeChargeRefunded {
		return nil, errStripeCheckoutInvalid
	}
	base, err := buildStripeReversalBaseInput(event, payload, stripeConfig)
	if err != nil {
		return nil, err
	}
	var charge stripe.Charge
	if err := common.Unmarshal(event.Data.Raw, &charge); err != nil {
		return nil, fmt.Errorf("%w: decode Stripe refunded charge: %v", errStripeCheckoutInvalid, err)
	}
	chargeID := strings.TrimSpace(charge.ID)
	currency := strings.ToUpper(strings.TrimSpace(string(charge.Currency)))
	if chargeID == "" || charge.Livemode != event.Livemode || !isValidStripeCurrencyCode(currency) || charge.Refunds == nil || len(charge.Refunds.Data) == 0 {
		return nil, errStripeCheckoutInvalid
	}
	parentPaymentIntentID := ""
	if charge.PaymentIntent != nil {
		parentPaymentIntentID = strings.TrimSpace(charge.PaymentIntent.ID)
	}
	deliveries := make([]stripeReversalDelivery, 0, len(charge.Refunds.Data))
	seenRefunds := make(map[string]struct{}, len(charge.Refunds.Data))
	for _, refund := range charge.Refunds.Data {
		if refund == nil {
			return nil, errStripeCheckoutInvalid
		}
		refundID := strings.TrimSpace(refund.ID)
		if refundID == "" {
			return nil, errStripeCheckoutInvalid
		}
		if _, exists := seenRefunds[refundID]; exists {
			return nil, errStripeCheckoutInvalid
		}
		seenRefunds[refundID] = struct{}{}
		if refundCurrency := strings.ToUpper(strings.TrimSpace(string(refund.Currency))); refundCurrency != currency {
			return nil, errStripeCheckoutInvalid
		}
		if refund.Charge != nil && strings.TrimSpace(refund.Charge.ID) != "" && strings.TrimSpace(refund.Charge.ID) != chargeID {
			return nil, errStripeCheckoutInvalid
		}
		refundPaymentIntentID := ""
		if refund.PaymentIntent != nil {
			refundPaymentIntentID = strings.TrimSpace(refund.PaymentIntent.ID)
		}
		if parentPaymentIntentID != "" && refundPaymentIntentID != "" && refundPaymentIntentID != parentPaymentIntentID {
			return nil, errStripeCheckoutInvalid
		}
		paymentIntent := charge.PaymentIntent
		if paymentIntent == nil {
			paymentIntent = refund.PaymentIntent
		}
		objects := stripeReversalPaymentObjects(paymentIntent, &charge)
		childBase := base
		childBase.DeliveryID = base.DeliveryID + ":" + refundID
		if len(childBase.DeliveryID) > 255 {
			return nil, errStripeCheckoutInvalid
		}
		delivery, deliveryErr := buildStripeRefundReversalDelivery(childBase, event.Type, refund, objects)
		if deliveryErr != nil {
			return nil, deliveryErr
		}
		deliveries = append(deliveries, delivery)
	}
	return deliveries, nil
}

func resolveStripeReversalBinding(input *model.ProviderRefundEventInput, objects []model.ProviderPaymentObject) (string, error) {
	if input == nil || len(objects) == 0 {
		return "", errStripeCheckoutInvalid
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
	input.ProviderObjectType = selectedObject.ObjectType
	input.ProviderTradeNo = selectedObject.ObjectID
	input.OrderTradeNo = selected.OrderTradeNo
	input.OrderKind = selected.OrderKind
	return "", nil
}

func processStripeReversalDelivery(ctx context.Context, sourceEvent stripe.Event, delivery stripeReversalDelivery) error {
	if !delivery.actionable {
		logger.LogInfo(ctx, fmt.Sprintf("Stripe reversal delivery is not terminal event_id=%s delivery_id=%s event_type=%s", sourceEvent.ID, delivery.input.DeliveryID, sourceEvent.Type))
		return nil
	}
	manualReason, err := resolveStripeReversalBinding(&delivery.input, delivery.objects)
	if err != nil {
		return err
	}
	if manualReason != "" && delivery.input.EventType != "refund.failed" {
		delivery.input.ForceManualReason = manualReason
	}
	result, err := model.ProcessProviderRefundEvent(delivery.input)
	if err != nil {
		return err
	}
	logger.LogInfo(ctx, fmt.Sprintf(
		"Stripe reversal delivery processed event_id=%s delivery_id=%s effect_id=%s event_type=%s status=%s already_processed=%t",
		sourceEvent.ID,
		delivery.input.DeliveryID,
		delivery.input.EffectID,
		sourceEvent.Type,
		result.Status,
		result.AlreadyProcessed,
	))
	return nil
}

func handleStripeReversalEvent(ctx context.Context, event stripe.Event, payload string) error {
	return handleStripeReversalEventWithConfig(ctx, event, payload, setting.GetStripeConfig())
}

func handleStripeReversalEventWithConfig(ctx context.Context, event stripe.Event, payload string, stripeConfig setting.StripeConfig) error {
	if event.Type == stripe.EventTypeChargeRefunded {
		deliveries, err := buildStripeChargeRefundedDeliveriesWithConfig(event, payload, stripeConfig)
		if err != nil {
			return err
		}
		actionableCount := 0
		for _, delivery := range deliveries {
			if delivery.actionable {
				actionableCount++
			}
			if err := processStripeReversalDelivery(ctx, event, delivery); err != nil {
				return err
			}
		}
		if actionableCount == 0 {
			logger.LogInfo(ctx, fmt.Sprintf("Stripe charge.refunded event has no terminal refunds event_id=%s refund_count=%d", event.ID, len(deliveries)))
		}
		return nil
	}
	input, objects, actionable, err := buildStripeReversalEventInputWithConfig(event, payload, stripeConfig)
	if err != nil {
		return err
	}
	return processStripeReversalDelivery(ctx, event, stripeReversalDelivery{input: input, objects: objects, actionable: actionable})
}

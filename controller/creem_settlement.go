package controller

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/shopspring/decimal"
)

var (
	errCreemEventInvalid  = errors.New("invalid Creem webhook event")
	errCreemEventMismatch = errors.New("Creem webhook does not match local order")
	// Renewal lifecycle events are intentionally fail-closed when the provider
	// omits the immutable order evidence needed to bind them to a local
	// subscription snapshot. The controller may acknowledge this sentinel
	// without mutating entitlement; malformed or contradictory evidence remains
	// a hard mismatch and is retried/reviewed instead.
	errCreemLifecycleEvidenceMissing = errors.New("Creem lifecycle evidence missing")
)

func creemMerchantSnapshot() string {
	return creemMerchantSnapshotWithConfig(setting.GetCreemConfig())
}

// Creem does not expose a stable merchant/account identifier in webhook
// payloads. Namespace provider objects by the credential that authenticated
// the delivery, without persisting the secret itself. Rotating the webhook
// secret deliberately creates a new scope; legacy in-flight orders must then
// be reconciled instead of being silently claimed by another Creem account.
func creemMerchantSnapshotWithConfig(cfg setting.CreemConfig) string {
	secret := strings.TrimSpace(cfg.WebhookSecret)
	if secret == "" {
		return ""
	}
	return fmt.Sprintf("webhook_%x", common.Sha256Raw([]byte("creem-webhook-scope-v1\x00"+secret)))
}

func creemProviderEnvironment(cfg setting.CreemConfig) string {
	return expectedCreemModeForConfig(cfg)
}

func creemCheckoutPaymentObjects(event *CreemWebhookEvent) []model.ProviderPaymentObject {
	if event == nil {
		return nil
	}
	objects := make([]model.ProviderPaymentObject, 0, 4)
	seen := make(map[string]struct{}, 4)
	add := func(objectType, objectID string) {
		objectID = strings.TrimSpace(objectID)
		if objectID == "" {
			return
		}
		key := objectType + "\x00" + objectID
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		objects = append(objects, model.ProviderPaymentObject{ObjectType: objectType, ObjectID: objectID})
	}
	add(model.ProviderPaymentObjectCheckoutSession, event.Object.Id)
	add(model.ProviderPaymentObjectOrder, event.Object.Order.Id)
	add(model.ProviderPaymentObjectTransaction, event.Object.Order.Transaction)
	add(model.ProviderPaymentObjectSubscription, creemSubscriptionID(event))
	return objects
}

func normalizeCreemMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "test", "sandbox", "testing":
		return "test"
	case "live", "prod", "production", "local":
		// Older Creem payloads called the production environment "local".
		return "live"
	default:
		return mode
	}
}

func expectedCreemMode() string {
	return expectedCreemModeForConfig(setting.GetCreemConfig())
}

func expectedCreemModeForConfig(cfg setting.CreemConfig) string {
	if cfg.TestMode {
		return "test"
	}
	return "live"
}

// validateCreemEventMode binds a webhook to the same Creem environment whose
// credentials authenticated it.  Test and live Creem accounts can emit the
// same-shaped identifiers; accepting a lifecycle event from the other mode
// could therefore mutate a local entitlement even though no checkout
// settlement was performed for this deployment.  The order/object mode is
// authoritative, while any expanded related objects must agree with it.
func validateCreemEventMode(event *CreemWebhookEvent, cfg setting.CreemConfig) error {
	if event == nil {
		return errCreemEventInvalid
	}
	// Lifecycle deliveries are not guaranteed to expand the checkout order;
	// some Creem versions carry the environment only on the subscription
	// object. Extract that mode before selecting the primary binding. A
	// malformed object is not equivalent to an omitted optional field: reject
	// it so an attacker cannot hide a mixed-environment payload behind a valid
	// top-level mode.
	subscriptionMode := ""
	if raw := strings.TrimSpace(string(event.Object.Subscription)); raw != "" && raw != "null" {
		jsonType := common.GetJsonType(event.Object.Subscription)
		switch jsonType {
		case "object":
			var subscription struct {
				Mode string `json:"mode"`
			}
			if err := common.Unmarshal(event.Object.Subscription, &subscription); err != nil {
				return errCreemEventInvalid
			}
			subscriptionMode = strings.TrimSpace(subscription.Mode)
		case "string":
			// Legacy payloads may expose only the subscription ID as a JSON
			// string. It carries no mode, so the enclosing object must provide
			// the environment binding.
			var subscriptionID string
			if err := common.Unmarshal(event.Object.Subscription, &subscriptionID); err != nil || strings.TrimSpace(subscriptionID) == "" {
				return errCreemEventInvalid
			}
		default:
			// Arrays, numbers, booleans, or malformed JSON are not valid
			// subscription references. Do not silently ignore them while
			// accepting a different top-level mode.
			return errCreemEventInvalid
		}
	}
	// Creem's envelope mode is not stable across event families: the dispute
	// example currently reports object.mode=local while its transaction/order
	// objects correctly report sandbox. Prefer the nested payment objects, which
	// are the ones that own the money movement. The envelope mode is a fallback
	// only when no nested object carries an environment.
	primaryMode := strings.TrimSpace(event.Object.Order.Mode)
	if primaryMode == "" {
		primaryMode = strings.TrimSpace(event.Object.Transaction.Mode)
	}
	if primaryMode == "" {
		primaryMode = strings.TrimSpace(event.Object.Checkout.Mode)
	}
	if primaryMode == "" {
		primaryMode = subscriptionMode
	}
	if primaryMode == "" {
		primaryMode = strings.TrimSpace(event.Object.Mode)
	}
	if primaryMode == "" || normalizeCreemMode(primaryMode) != expectedCreemModeForConfig(cfg) {
		return errCreemEventMismatch
	}
	normalizedPrimary := normalizeCreemMode(primaryMode)
	relatedModes := []string{
		event.Object.Order.Mode,
		event.Object.Transaction.Mode,
		event.Object.Checkout.Mode,
		event.Object.Product.Mode,
		event.Object.Customer.Mode,
	}
	// Lifecycle payloads may expand the subscription object independently of
	// the checkout order.  Include its mode when present so a mixed test/live
	// payload cannot pass merely because the order copy is valid.
	if len(event.Object.Subscription) > 0 {
		if subscriptionMode != "" {
			relatedModes = append(relatedModes, subscriptionMode)
		}
	}
	// object.mode is an envelope hint and is known to be inconsistent with the
	// nested mode on dispute.created. When nested evidence exists, do not let
	// that presentation quirk reject an otherwise consistently scoped payment.
	if primaryMode == strings.TrimSpace(event.Object.Mode) && len(relatedModes) == 0 {
		relatedModes = append(relatedModes, event.Object.Mode)
	}
	for _, relatedMode := range relatedModes {
		if strings.TrimSpace(relatedMode) != "" && normalizeCreemMode(relatedMode) != normalizedPrimary {
			return errCreemEventMismatch
		}
	}
	return nil
}

// creemAmountString validates the provider's immutable order amount against
// the amount frozen when the checkout was created. Creem represents order and
// product prices as integer minor units (for example EUR 10.00 is 1000), while
// amount_paid/amount_due may include tax and therefore be larger than the
// product amount. The latter fields are treated as lower-bound payment
// evidence, never as an alternative source of the price. In particular, do
// not accept a callback merely because amount/amount_due matches while
// amount_paid is lower: that would grant an underpaid order.
func creemAmountString(event *CreemWebhookEvent, expected string) (string, error) {
	if event == nil {
		return "", errCreemEventInvalid
	}
	expectedDecimal, err := decimal.NewFromString(strings.TrimSpace(expected))
	if err != nil || !expectedDecimal.IsPositive() {
		return "", errCreemEventInvalid
	}

	currency := strings.ToUpper(strings.TrimSpace(event.Object.Order.Currency))
	if currency == "" {
		currency = strings.ToUpper(strings.TrimSpace(event.Object.Product.Currency))
	}
	if len(currency) != 3 {
		return "", errCreemEventInvalid
	}

	// Order.Amount is the provider's base product amount in minor units. Use
	// decimal arithmetic so a float conversion cannot turn a fractional local
	// amount into a silently rounded integer.
	expectedMinor := expectedDecimal.Shift(stripeMinorExponent(currency))
	if !expectedMinor.Equal(expectedMinor.Round(0)) || !expectedMinor.IsPositive() {
		return "", errCreemEventInvalid
	}
	orderAmount := int64(event.Object.Order.Amount)
	if orderAmount <= 0 {
		return "", errCreemEventInvalid
	}
	// Compare as decimals instead of converting an unbounded local amount to
	// int64 (Decimal.IntPart intentionally leaves overflow behaviour undefined).
	if !expectedMinor.Equal(decimal.NewFromInt(orderAmount)) {
		return "", errCreemEventMismatch
	}

	// Product.Price is another signed copy of the base amount. It is omitted
	// by some older payloads; when present, a contradiction means the event is
	// not for the product that was frozen locally.
	if productPrice := int64(event.Object.Product.Price); productPrice > 0 && productPrice != orderAmount {
		return "", errCreemEventMismatch
	}
	// New API always creates one checkout unit. A quantity greater than one
	// would make order.amount differ from the frozen single-product price and
	// must not be accepted without an explicit local quantity snapshot.
	if event.Object.Units > 1 {
		return "", errCreemEventMismatch
	}

	amountDue := int64(event.Object.Order.AmountDue)
	if amountDue > 0 {
		// Taxes/fees may increase amount_due, but it can never be below the
		// signed base amount for a paid order.
		if amountDue < orderAmount {
			return "", errCreemEventMismatch
		}
	}
	amountPaid := int64(event.Object.Order.AmountPaid)
	if amountPaid > 0 {
		minimumPaid := orderAmount
		if amountDue > 0 {
			minimumPaid = amountDue
		}
		if amountPaid < minimumPaid {
			return "", errCreemEventMismatch
		}
	}

	return expectedDecimal.String(), nil
}

func creemEventMetadata(event *CreemWebhookEvent) map[string]string {
	if event == nil || event.Object.Metadata == nil {
		return map[string]string{}
	}
	return event.Object.Metadata
}

// creemSubscriptionID extracts the recurring object id without assuming that
// every historical webhook expands the object. The documented shape is an
// object ({"id":"sub_..."}); accepting a bare string keeps old deliveries
// parseable while an omitted/malformed optional field simply yields no id.
func creemSubscriptionID(event *CreemWebhookEvent) string {
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
	// Subscription lifecycle payloads from older Creem versions used the
	// enclosing object id instead of an expanded `subscription` field.
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(event.EventType)), "subscription.") &&
		isCreemSubscriptionReference(event.Object.Id) {
		return strings.TrimSpace(event.Object.Id)
	}
	// Refund/dispute and some lifecycle payloads expose the recurring id only
	// on the expanded transaction object. It is provider-owned evidence, unlike
	// customer-controlled metadata, so it is safe to use as an alias candidate.
	if id := strings.TrimSpace(event.Object.Transaction.Subscription); isCreemSubscriptionReference(id) {
		return id
	}
	return ""
}

// Metadata is customer-controlled checkout data. Only accept the provider's
// subscription identifier namespace from metadata; otherwise a valid signed
// refund/dispute for one customer could point at an arbitrary local row and
// revoke another customer's entitlement.
func isCreemSubscriptionReference(id string) bool {
	id = strings.TrimSpace(id)
	return strings.HasPrefix(strings.ToLower(id), "sub_") && len(id) > len("sub_")
}

// creemSubscriptionLifecycleEvidence extracts the recurring price proof from
// a Creem lifecycle payload. A renewal is allowed to move the local period
// only when all of the following are present and mutually consistent:
//   - order amount and product price (integer minor units);
//   - order and product currency;
//   - an explicitly paid amount covering the base price; and
//   - the provider product identifier.
//
// Creem may omit the expanded order/product object on status-only deliveries.
// Such a payload is represented by errCreemLifecycleEvidenceMissing so the
// caller can leave the current entitlement untouched. Contradictory values
// are wrapped with errCreemEventMismatch and must never be accepted as a
// renewal.
func creemSubscriptionLifecycleEvidence(event *CreemWebhookEvent) (*model.SubscriptionLifecycleEvidence, error) {
	if event == nil {
		return nil, errCreemEventInvalid
	}
	order := event.Object.Order
	product := event.Object.Product

	orderCurrency := strings.ToUpper(strings.TrimSpace(order.Currency))
	productCurrency := strings.ToUpper(strings.TrimSpace(product.Currency))
	if orderCurrency == "" || productCurrency == "" {
		return nil, errCreemLifecycleEvidenceMissing
	}
	if !isValidCreemCurrencyCode(orderCurrency) || !isValidCreemCurrencyCode(productCurrency) {
		return nil, errCreemEventInvalid
	}
	if orderCurrency != productCurrency {
		return nil, fmt.Errorf("%w: lifecycle currency differs between order and product", errCreemEventMismatch)
	}

	productID := strings.TrimSpace(product.Id)
	if productID == "" {
		// Some API versions put the product id only on the order object. This is
		// still explicit provider evidence, but an absent id in both places is
		// not enough to bind a renewal to a local plan.
		productID = strings.TrimSpace(order.Product)
	}
	if productID == "" {
		return nil, errCreemLifecycleEvidenceMissing
	}
	if product.Id != "" && strings.TrimSpace(order.Product) != "" && strings.TrimSpace(order.Product) != productID {
		return nil, fmt.Errorf("%w: lifecycle product differs between order and product", errCreemEventMismatch)
	}

	if order.Amount < 0 || product.Price < 0 || order.AmountPaid < 0 || order.AmountDue < 0 {
		return nil, errCreemEventInvalid
	}
	if order.Amount == 0 || product.Price == 0 || order.AmountPaid == 0 {
		return nil, errCreemLifecycleEvidenceMissing
	}
	if order.Status != "" && !strings.EqualFold(strings.TrimSpace(order.Status), "paid") {
		return nil, fmt.Errorf("%w: lifecycle order is not paid", errCreemEventMismatch)
	}
	if order.Type != "" {
		typ := strings.ToLower(strings.TrimSpace(order.Type))
		if typ != "recurring" && typ != "subscription" {
			return nil, fmt.Errorf("%w: lifecycle order is not recurring", errCreemEventMismatch)
		}
	}
	if product.BillingType != "" && !strings.EqualFold(strings.TrimSpace(product.BillingType), "recurring") {
		return nil, fmt.Errorf("%w: lifecycle product is not recurring", errCreemEventMismatch)
	}
	if order.Amount != product.Price {
		return nil, fmt.Errorf("%w: lifecycle amount differs between order and product", errCreemEventMismatch)
	}
	if order.AmountPaid < order.Amount {
		return nil, fmt.Errorf("%w: lifecycle amount_paid is below base amount", errCreemEventMismatch)
	}
	if order.AmountDue > 0 && order.AmountDue < order.Amount {
		return nil, fmt.Errorf("%w: lifecycle amount_due is below base amount", errCreemEventMismatch)
	}
	if order.AmountDue > 0 && order.AmountPaid < order.AmountDue {
		return nil, fmt.Errorf("%w: lifecycle amount_paid is below amount_due", errCreemEventMismatch)
	}

	return &model.SubscriptionLifecycleEvidence{
		Amount:    stripeAmountFromMinorUnits(int64(order.Amount), orderCurrency),
		Currency:  orderCurrency,
		ProductID: productID,
	}, nil
}

func isValidCreemCurrencyCode(currency string) bool {
	if len(currency) != 3 {
		return false
	}
	for i := 0; i < len(currency); i++ {
		if currency[i] < 'A' || currency[i] > 'Z' {
			return false
		}
	}
	return true
}

func buildCreemProviderSettlement(event *CreemWebhookEvent, referenceID string, kind string, expectedAmount string) (model.ProviderSettlement, error) {
	return buildCreemProviderSettlementWithConfig(event, referenceID, kind, expectedAmount, setting.GetCreemConfig())
}

// buildCreemProviderSettlementWithConfig validates a callback against the
// same Creem mode snapshot that authenticated the request. Keeping the
// snapshot explicit prevents a mode hot-reload between signature verification
// and settlement from changing the decision halfway through one webhook.
func buildCreemProviderSettlementWithConfig(event *CreemWebhookEvent, referenceID string, kind string, expectedAmount string, cfg setting.CreemConfig) (model.ProviderSettlement, error) {
	if event == nil || strings.TrimSpace(referenceID) == "" || strings.TrimSpace(event.Id) == "" {
		return model.ProviderSettlement{}, errCreemEventInvalid
	}
	if event.EventType != "checkout.completed" || strings.ToLower(strings.TrimSpace(event.Object.Order.Status)) != "paid" {
		return model.ProviderSettlement{}, errCreemEventInvalid
	}
	if objectStatus := strings.ToLower(strings.TrimSpace(event.Object.Status)); objectStatus != "" && objectStatus != "completed" {
		return model.ProviderSettlement{}, errCreemEventInvalid
	}
	orderType := strings.ToLower(strings.TrimSpace(event.Object.Order.Type))
	switch kind {
	case "topup":
		if orderType != "onetime" && orderType != "one_time" {
			return model.ProviderSettlement{}, errCreemEventMismatch
		}
	case "subscription":
		if orderType != "recurring" && orderType != "subscription" {
			return model.ProviderSettlement{}, errCreemEventMismatch
		}
	default:
		return model.ProviderSettlement{}, errCreemEventInvalid
	}
	if err := validateCreemEventMode(event, cfg); err != nil {
		return model.ProviderSettlement{}, err
	}
	if event.Object.RequestId != referenceID {
		return model.ProviderSettlement{}, errCreemEventMismatch
	}
	metadata := creemEventMetadata(event)
	if metadata["reference_id"] != "" && metadata["reference_id"] != referenceID {
		return model.ProviderSettlement{}, errCreemEventMismatch
	}
	if metadata["order_kind"] != "" && metadata["order_kind"] != kind {
		return model.ProviderSettlement{}, errCreemEventMismatch
	}
	productID := strings.TrimSpace(event.Object.Product.Id)
	if productID == "" {
		productID = strings.TrimSpace(event.Object.Order.Product)
	}
	if productID == "" || (metadata["product_id"] != "" && metadata["product_id"] != productID) {
		return model.ProviderSettlement{}, errCreemEventMismatch
	}
	if orderProductID := strings.TrimSpace(event.Object.Order.Product); orderProductID != "" && orderProductID != productID {
		return model.ProviderSettlement{}, errCreemEventMismatch
	}
	currency := strings.ToUpper(strings.TrimSpace(event.Object.Order.Currency))
	if currency == "" {
		currency = strings.ToUpper(strings.TrimSpace(event.Object.Product.Currency))
	}
	if currency == "" || (metadata["currency"] != "" && strings.ToUpper(metadata["currency"]) != currency) {
		return model.ProviderSettlement{}, errCreemEventMismatch
	}
	if orderCurrency := strings.ToUpper(strings.TrimSpace(event.Object.Order.Currency)); orderCurrency != "" {
		if productCurrency := strings.ToUpper(strings.TrimSpace(event.Object.Product.Currency)); productCurrency != "" && productCurrency != orderCurrency {
			return model.ProviderSettlement{}, errCreemEventMismatch
		}
	}
	amount, err := creemAmountString(event, expectedAmount)
	if err != nil {
		return model.ProviderSettlement{}, err
	}
	providerTradeNo := strings.TrimSpace(event.Object.Order.Id)
	if providerTradeNo == "" {
		providerTradeNo = strings.TrimSpace(event.Object.Order.Transaction)
	}
	if providerTradeNo == "" {
		return model.ProviderSettlement{}, errCreemEventInvalid
	}
	checkoutID := strings.TrimSpace(event.Object.Id)
	if checkoutID == "" {
		return model.ProviderSettlement{}, errCreemEventInvalid
	}
	providerAccountID := creemMerchantSnapshotWithConfig(cfg)
	if providerAccountID == "" {
		return model.ProviderSettlement{}, errCreemEventInvalid
	}
	providerEnvironment := creemProviderEnvironment(cfg)
	providerKeyFingerprint, ok := model.ProviderPaymentScopeFingerprint(model.PaymentProviderCreem, providerAccountID, providerEnvironment)
	if !ok {
		return model.ProviderSettlement{}, errCreemEventInvalid
	}
	paymentObjects := creemCheckoutPaymentObjects(event)
	if len(paymentObjects) < 2 {
		return model.ProviderSettlement{}, errCreemEventInvalid
	}
	orderName := strings.TrimSpace(event.Object.Product.Name)
	if orderName == "" {
		orderName = strings.TrimSpace(metadata["product_name"])
	}
	return model.ProviderSettlement{
		OrderTradeNo:           referenceID,
		Provider:               model.PaymentProviderCreem,
		ProviderTradeNo:        providerTradeNo,
		ProviderEventID:        strings.TrimSpace(event.Id),
		ProviderAccountID:      providerAccountID,
		ProviderEnvironment:    providerEnvironment,
		ProviderKeyFingerprint: providerKeyFingerprint,
		MerchantID:             providerAccountID,
		ProductID:              productID,
		Currency:               currency,
		Amount:                 amount,
		OrderName:              orderName,
		CheckoutID:             checkoutID,
		ProviderSubscriptionID: creemSubscriptionID(event),
		Payload:                common.GetJsonString(event),
		PaymentObjects:         paymentObjects,
	}, nil
}

func validateCreemProductConfig(product *CreemProduct) error {
	if product == nil || strings.TrimSpace(product.ProductId) == "" || product.Price <= 0 || math.IsNaN(product.Price) || math.IsInf(product.Price, 0) {
		return fmt.Errorf("invalid Creem product configuration")
	}
	if !isValidCreemCurrencyCode(strings.ToUpper(strings.TrimSpace(product.Currency))) {
		return fmt.Errorf("invalid Creem product configuration")
	}
	return nil
}

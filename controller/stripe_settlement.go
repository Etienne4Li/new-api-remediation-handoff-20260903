package controller

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/shopspring/decimal"
	"github.com/stripe/stripe-go/v81"
)

var (
	errStripeCheckoutInvalid  = errors.New("invalid Stripe checkout event")
	errStripeCheckoutMismatch = errors.New("Stripe checkout does not match local order")
	errStripeCheckoutMode     = errors.New("Stripe checkout mode or environment mismatch")
	errStripeOrderConflict    = errors.New("Stripe trade number maps to multiple local orders")
)

// stripeSettlementHTTPStatus maps a handler failure to the response class
// Stripe should see.  A signed event whose identity/payload conflicts with a
// local immutable snapshot is permanently non-actionable: retrying it cannot
// make the event valid and, more importantly, returning 5xx makes Stripe keep
// redelivering a poisoned event indefinitely.  Database/driver failures and
// other unknown errors remain 5xx so a valid payment is retried after the
// infrastructure recovers.
func stripeSettlementHTTPStatus(err error) int {
	switch {
	case errors.Is(err, errStripeCheckoutInvalid),
		errors.Is(err, errStripeCheckoutMismatch),
		errors.Is(err, errStripeCheckoutMode),
		errors.Is(err, errStripeOrderConflict),
		errors.Is(err, model.ErrProviderPaymentBindingInvalid),
		errors.Is(err, model.ErrProviderPaymentBindingConflict),
		errors.Is(err, model.ErrProviderPaymentBindingNotFound),
		errors.Is(err, model.ErrProviderRefundInvalid),
		errors.Is(err, model.ErrProviderRefundConflict),
		errors.Is(err, model.ErrProviderSettlementInvalid),
		errors.Is(err, model.ErrProviderSnapshotMissing),
		errors.Is(err, model.ErrProviderSnapshotMismatch),
		errors.Is(err, model.ErrProviderEventConflict),
		errors.Is(err, model.ErrPaymentMethodMismatch),
		errors.Is(err, model.ErrTopUpNotFound),
		errors.Is(err, model.ErrTopUpStatusInvalid),
		errors.Is(err, model.ErrInvalidTopUpQuota),
		errors.Is(err, model.ErrTopUpQuotaLimitExceeded),
		errors.Is(err, model.ErrWalletQuotaLimitExceeded),
		errors.Is(err, model.ErrSubscriptionOrderNotFound),
		errors.Is(err, model.ErrSubscriptionOrderStatusInvalid),
		errors.Is(err, model.ErrSubscriptionOrderConflict),
		errors.Is(err, model.ErrSubscriptionEntitlementSnapshotMissing),
		errors.Is(err, model.ErrSubscriptionEntitlementSnapshotInvalid),
		errors.Is(err, model.ErrSubscriptionPreConsumeConflict):
		return http.StatusBadRequest
	default:
		return http.StatusServiceUnavailable
	}
}

// lookupStripeOrderKind resolves the local grant target without swallowing
// database failures. Stripe webhooks must retry when the database is
// unavailable, and a trade number that exists in both order tables must be
// rejected rather than silently choosing one entitlement type.
func lookupStripeOrderKind(tradeNo string) (string, error) {
	subscriptionOrder, subscriptionErr := model.GetSubscriptionOrderByTradeNoWithError(tradeNo)
	if subscriptionErr != nil && !errors.Is(subscriptionErr, model.ErrSubscriptionOrderNotFound) {
		return "", subscriptionErr
	}
	if errors.Is(subscriptionErr, model.ErrSubscriptionOrderNotFound) {
		subscriptionOrder = nil
	}
	topUp, topUpErr := model.GetTopUpByTradeNoWithError(tradeNo)
	if topUpErr != nil && !errors.Is(topUpErr, model.ErrTopUpNotFound) {
		return "", topUpErr
	}
	if errors.Is(topUpErr, model.ErrTopUpNotFound) {
		topUp = nil
	}
	if subscriptionOrder != nil && topUp != nil {
		return "", errStripeOrderConflict
	}
	if subscriptionOrder != nil {
		return "subscription", nil
	}
	if topUp != nil {
		return "topup", nil
	}
	return "", model.ErrTopUpNotFound
}

func getStripeCurrency() string {
	return getStripeCurrencyWithConfig(setting.GetStripeConfig())
}

func getStripeCurrencyWithConfig(stripeConfig setting.StripeConfig) string {
	currency := strings.ToUpper(strings.TrimSpace(stripeConfig.Currency))
	if len(currency) != 3 {
		return "USD"
	}
	return currency
}

func stripeLiveMode() (bool, error) {
	return stripeLiveModeWithConfig(setting.GetStripeConfig())
}

func stripeLiveModeWithConfig(stripeConfig setting.StripeConfig) (bool, error) {
	key := strings.TrimSpace(stripeConfig.ApiSecret)
	switch {
	case strings.HasPrefix(key, "sk_test_") || strings.HasPrefix(key, "rk_test_"):
		return false, nil
	case strings.HasPrefix(key, "sk_live_") || strings.HasPrefix(key, "rk_live_"):
		return true, nil
	default:
		return false, fmt.Errorf("Stripe secret key environment is unknown")
	}
}

// stripeKeyFingerprint identifies the configured API credential without
// persisting or echoing the secret itself.
func stripeKeyFingerprint(apiSecret string) string {
	apiSecret = strings.TrimSpace(apiSecret)
	if apiSecret == "" {
		return ""
	}
	return fmt.Sprintf("%x", common.Sha256Raw([]byte("stripe-api-key-v1\x00"+apiSecret)))
}

func stripeStandardAccountScope(stripeConfig setting.StripeConfig) string {
	if accountID := stripeConfiguredAccountID(stripeConfig); accountID != "" {
		return accountID
	}
	// Stripe omits account on standard-account events. Older installations may
	// not have the explicit account setting yet, so keep a fail-closed credential
	// namespace until an acct_ id is configured. Key rotation changes this
	// fallback by design; it never guesses that two credentials share an account.
	fingerprint := stripeKeyFingerprint(stripeConfig.ApiSecret)
	if fingerprint == "" {
		return ""
	}
	return "key_" + fingerprint
}

func stripeConfiguredAccountID(stripeConfig setting.StripeConfig) string {
	accountID := strings.TrimSpace(stripeConfig.AccountID)
	if !strings.HasPrefix(accountID, "acct_") {
		return ""
	}
	return accountID
}

func isStripeCredentialAccountScope(accountID string) bool {
	accountID = strings.TrimSpace(accountID)
	if len(accountID) != len("key_")+sha256.Size*2 || !strings.HasPrefix(accountID, "key_") {
		return false
	}
	for _, char := range accountID[len("key_"):] {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func stripeProviderScopeFingerprint(providerAccountID, providerEnvironment string) string {
	fingerprint, ok := model.ProviderPaymentScopeFingerprint(model.PaymentProviderStripe, providerAccountID, providerEnvironment)
	if !ok {
		return ""
	}
	return fingerprint
}

func stripeMinorExponent(currency string) int32 {
	switch strings.ToUpper(strings.TrimSpace(currency)) {
	case "BIF", "CLP", "DJF", "GNF", "JPY", "KMF", "KRW", "MGA", "PYG", "RWF", "UGX", "VND", "VUV", "XAF", "XOF", "XPF":
		return 0
	case "BHD", "JOD", "KWD", "OMR", "TND":
		return 3
	default:
		return 2
	}
}

func stripeAmountFromMinorUnits(minor int64, currency string) string {
	exponent := stripeMinorExponent(currency)
	return decimal.NewFromInt(minor).Shift(-exponent).StringFixed(exponent)
}

func stripeAmountFromMajorUnits(amount float64, currency string) string {
	exponent := stripeMinorExponent(currency)
	return decimal.NewFromFloat(amount).Round(exponent).StringFixed(exponent)
}

func stripeObjectString(node interface{}, keys ...string) string {
	for _, key := range keys {
		m, ok := node.(map[string]interface{})
		if !ok {
			return ""
		}
		node = m[key]
	}
	switch value := node.(type) {
	case string:
		return strings.TrimSpace(value)
	default:
		return ""
	}
}

func stripeSessionFromEvent(event stripe.Event) (*stripe.CheckoutSession, error) {
	if event.Data == nil || len(event.Data.Raw) == 0 {
		return nil, errStripeCheckoutInvalid
	}
	var checkout stripe.CheckoutSession
	if err := common.Unmarshal(event.Data.Raw, &checkout); err != nil {
		return nil, fmt.Errorf("decode Stripe checkout session: %w", err)
	}
	if strings.TrimSpace(checkout.ID) == "" {
		return nil, errStripeCheckoutInvalid
	}
	return &checkout, nil
}

func stripeProviderEnvironment(live bool) string {
	if live {
		return "live"
	}
	return "test"
}

func stripeProviderAccountID(event stripe.Event, standardAccountScope string) string {
	if accountID := strings.TrimSpace(event.Account); accountID != "" {
		return accountID
	}
	return strings.TrimSpace(standardAccountScope)
}

type stripePaymentObjectCollector struct {
	objects []model.ProviderPaymentObject
	seen    map[string]struct{}
}

func newStripePaymentObjectCollector() *stripePaymentObjectCollector {
	return &stripePaymentObjectCollector{
		objects: make([]model.ProviderPaymentObject, 0, 5),
		seen:    make(map[string]struct{}, 5),
	}
}

func (collector *stripePaymentObjectCollector) add(objectType, objectID string) {
	objectType = strings.TrimSpace(objectType)
	objectID = strings.TrimSpace(objectID)
	if objectType == "" || objectID == "" {
		return
	}
	key := objectType + "\x00" + objectID
	if _, exists := collector.seen[key]; exists {
		return
	}
	collector.seen[key] = struct{}{}
	collector.objects = append(collector.objects, model.ProviderPaymentObject{ObjectType: objectType, ObjectID: objectID})
}

func (collector *stripePaymentObjectCollector) addPaymentIntent(paymentIntent *stripe.PaymentIntent) {
	if paymentIntent == nil {
		return
	}
	collector.add(model.ProviderPaymentObjectPaymentIntent, paymentIntent.ID)
	if paymentIntent.LatestCharge != nil {
		collector.add(model.ProviderPaymentObjectCharge, paymentIntent.LatestCharge.ID)
	}
	if paymentIntent.Invoice != nil {
		collector.add(model.ProviderPaymentObjectInvoice, paymentIntent.Invoice.ID)
	}
}

func (collector *stripePaymentObjectCollector) addInvoice(invoice *stripe.Invoice) {
	if invoice == nil {
		return
	}
	collector.add(model.ProviderPaymentObjectInvoice, invoice.ID)
	if invoice.Charge != nil {
		collector.add(model.ProviderPaymentObjectCharge, invoice.Charge.ID)
	}
	collector.addPaymentIntent(invoice.PaymentIntent)
	if invoice.Subscription != nil {
		collector.add(model.ProviderPaymentObjectSubscription, invoice.Subscription.ID)
	}
}

func stripeCheckoutPaymentObjects(checkout *stripe.CheckoutSession) []model.ProviderPaymentObject {
	if checkout == nil {
		return nil
	}
	collector := newStripePaymentObjectCollector()
	collector.add(model.ProviderPaymentObjectCheckoutSession, checkout.ID)
	collector.addPaymentIntent(checkout.PaymentIntent)
	collector.addInvoice(checkout.Invoice)
	if checkout.PaymentIntent != nil {
		collector.addInvoice(checkout.PaymentIntent.Invoice)
	}
	if checkout.Subscription != nil {
		collector.add(model.ProviderPaymentObjectSubscription, checkout.Subscription.ID)
	}
	return collector.objects
}

func stripeInvoicePaymentObjects(event stripe.Event) ([]model.ProviderPaymentObject, error) {
	if event.Data == nil || len(event.Data.Raw) == 0 {
		return nil, errStripeCheckoutInvalid
	}
	var invoice stripe.Invoice
	if err := common.Unmarshal(event.Data.Raw, &invoice); err != nil {
		return nil, fmt.Errorf("%w: decode Stripe invoice: %v", errStripeCheckoutInvalid, err)
	}
	if strings.TrimSpace(invoice.ID) == "" {
		return nil, errStripeCheckoutInvalid
	}
	collector := newStripePaymentObjectCollector()
	collector.addInvoice(&invoice)
	return collector.objects, nil
}

// buildStripeProviderSettlement verifies the provider-side invariants that
// can be checked without a database row. The final comparison against the
// immutable local snapshot happens in model.SettleTopUpProvider or
// CompleteSubscriptionOrderVerified.
func buildStripeProviderSettlement(event stripe.Event, referenceID string, expectedKind string) (model.ProviderSettlement, error) {
	stripeConfig := setting.GetStripeConfig()
	checkout, err := stripeSessionFromEvent(event)
	if err != nil {
		return model.ProviderSettlement{}, err
	}
	if strings.TrimSpace(referenceID) == "" || checkout.ClientReferenceID != referenceID {
		return model.ProviderSettlement{}, errStripeCheckoutMismatch
	}
	if checkout.Status != stripe.CheckoutSessionStatusComplete || checkout.PaymentStatus != stripe.CheckoutSessionPaymentStatusPaid {
		return model.ProviderSettlement{}, errStripeCheckoutInvalid
	}
	if checkout.Mode != stripe.CheckoutSessionModePayment && checkout.Mode != stripe.CheckoutSessionModeSubscription {
		return model.ProviderSettlement{}, errStripeCheckoutMode
	}
	if expectedKind == "topup" && checkout.Mode != stripe.CheckoutSessionModePayment {
		return model.ProviderSettlement{}, errStripeCheckoutMode
	}
	if expectedKind == "subscription" && checkout.Mode != stripe.CheckoutSessionModeSubscription {
		return model.ProviderSettlement{}, errStripeCheckoutMode
	}
	if event.Livemode != checkout.Livemode {
		return model.ProviderSettlement{}, errStripeCheckoutMode
	}
	live := event.Livemode
	currency := strings.ToUpper(strings.TrimSpace(string(checkout.Currency)))
	if currency == "" {
		return model.ProviderSettlement{}, errStripeCheckoutInvalid
	}
	metadata := checkout.Metadata
	if metadata == nil {
		metadata = map[string]string{}
	}
	if metadata["new_api_order_id"] != referenceID || metadata["new_api_order_kind"] != expectedKind {
		return model.ProviderSettlement{}, errStripeCheckoutMismatch
	}
	if expectedCurrency := strings.ToUpper(strings.TrimSpace(metadata["new_api_currency"])); expectedCurrency != "" && expectedCurrency != currency {
		return model.ProviderSettlement{}, errStripeCheckoutMismatch
	}
	productID := strings.TrimSpace(metadata["new_api_product_id"])
	if productID == "" {
		productID = stripeObjectString(event.Data.Object, "line_items", "data", "0", "price", "id")
	}
	if productID == "" {
		return model.ProviderSettlement{}, errStripeCheckoutMismatch
	}
	merchantID := strings.TrimSpace(metadata["new_api_merchant_id"])
	standardAccountScope := stripeConfiguredAccountID(stripeConfig)
	if strings.TrimSpace(stripeConfig.AccountID) != "" && standardAccountScope == "" {
		return model.ProviderSettlement{}, errStripeCheckoutMismatch
	}
	if standardAccountScope == "" && isStripeCredentialAccountScope(merchantID) {
		// Credential-scoped top-ups created before an explicit acct_ id was
		// configured retain their frozen namespace in signed Checkout metadata.
		standardAccountScope = merchantID
	}
	providerAccountID := stripeProviderAccountID(event, standardAccountScope)
	if merchantID == "" || providerAccountID == "" || providerAccountID != merchantID {
		// Connect deliveries are scoped by event.account. Standard-account
		// deliveries are scoped by the credential fingerprint frozen into the
		// Checkout Session metadata before redirecting the user.
		return model.ProviderSettlement{}, errStripeCheckoutMismatch
	}
	providerEnvironment := stripeProviderEnvironment(live)
	providerEventID := strings.TrimSpace(event.ID)
	if providerEventID == "" {
		return model.ProviderSettlement{}, errStripeCheckoutInvalid
	}
	amount := stripeAmountFromMinorUnits(checkout.AmountTotal, currency)
	if checkout.AmountTotal <= 0 {
		return model.ProviderSettlement{}, errStripeCheckoutInvalid
	}
	if expectedAmount := strings.TrimSpace(metadata["new_api_provider_amount"]); expectedAmount != "" && !compareStripeAmounts(expectedAmount, amount) {
		return model.ProviderSettlement{}, errStripeCheckoutMismatch
	}
	customerID := ""
	if checkout.Customer != nil {
		customerID = checkout.Customer.ID
	}
	if customerID == "" {
		customerID = stripeObjectString(event.Data.Object, "customer")
	}
	providerSubscriptionID := ""
	if checkout.Subscription != nil {
		providerSubscriptionID = strings.TrimSpace(checkout.Subscription.ID)
	}
	return model.ProviderSettlement{
		OrderTradeNo:           referenceID,
		Provider:               model.PaymentProviderStripe,
		ProviderTradeNo:        checkout.ID,
		ProviderEventID:        providerEventID,
		ProviderAccountID:      providerAccountID,
		ProviderEnvironment:    providerEnvironment,
		ProviderKeyFingerprint: stripeProviderScopeFingerprint(providerAccountID, providerEnvironment),
		MerchantID:             merchantID,
		ProductID:              productID,
		CheckoutID:             checkout.ID,
		ProviderSubscriptionID: providerSubscriptionID,
		Currency:               currency,
		Amount:                 amount,
		OrderName:              strings.TrimSpace(metadata["new_api_order_name"]),
		CustomerID:             customerID,
		Payload:                string(event.Data.Raw),
		PaymentObjects:         stripeCheckoutPaymentObjects(checkout),
	}, nil
}

// validateStripeCheckoutLifecycle binds non-payment lifecycle events (expired
// and async-payment-failed) to the exact checkout session that created the
// local order.  These events do not carry a successful payment amount, so the
// checkout id, client reference, environment, mode, and immutable metadata
// are the remaining identity fence.  A missing local checkout id fails closed
// rather than allowing a legacy/ambiguous event to mutate an order.
func validateStripeCheckoutLifecycle(event stripe.Event, referenceID, expectedKind, expectedCheckoutID, expectedProductID, expectedMerchantID string, expectedStatus stripe.CheckoutSessionStatus) (*stripe.CheckoutSession, error) {
	referenceID = strings.TrimSpace(referenceID)
	expectedKind = strings.TrimSpace(expectedKind)
	expectedCheckoutID = strings.TrimSpace(expectedCheckoutID)
	expectedProductID = strings.TrimSpace(expectedProductID)
	expectedMerchantID = strings.TrimSpace(expectedMerchantID)
	if referenceID == "" || expectedKind == "" || expectedCheckoutID == "" || expectedProductID == "" || expectedMerchantID == "" {
		return nil, errStripeCheckoutInvalid
	}
	if expectedStatus == stripe.CheckoutSessionStatusComplete && event.Type != stripe.EventTypeCheckoutSessionAsyncPaymentFailed {
		return nil, errStripeCheckoutInvalid
	}
	if expectedStatus == stripe.CheckoutSessionStatusExpired && event.Type != stripe.EventTypeCheckoutSessionExpired {
		return nil, errStripeCheckoutInvalid
	}
	checkout, err := stripeSessionFromEvent(event)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(checkout.ID) != expectedCheckoutID || strings.TrimSpace(checkout.ClientReferenceID) != referenceID {
		return nil, errStripeCheckoutMismatch
	}
	if strings.TrimSpace(checkout.Object) != "checkout.session" {
		return nil, errStripeCheckoutInvalid
	}
	if checkout.Status != expectedStatus {
		return nil, errStripeCheckoutInvalid
	}
	stripeConfig := setting.GetStripeConfig()
	live, err := stripeLiveModeWithConfig(stripeConfig)
	if err != nil || event.Livemode != live || checkout.Livemode != live {
		return nil, errStripeCheckoutMode
	}
	providerAccountID := stripeProviderAccountID(event, stripeStandardAccountScope(stripeConfig))
	if providerAccountID == "" || providerAccountID != expectedMerchantID {
		return nil, errStripeCheckoutMismatch
	}
	switch expectedKind {
	case "topup":
		if checkout.Mode != stripe.CheckoutSessionModePayment {
			return nil, errStripeCheckoutMode
		}
	case "subscription":
		if checkout.Mode != stripe.CheckoutSessionModeSubscription {
			return nil, errStripeCheckoutMode
		}
	default:
		return nil, errStripeCheckoutInvalid
	}
	metadata := checkout.Metadata
	if metadata == nil || strings.TrimSpace(metadata["new_api_order_id"]) != referenceID || strings.TrimSpace(metadata["new_api_order_kind"]) != expectedKind {
		return nil, errStripeCheckoutMismatch
	}
	if strings.TrimSpace(metadata["new_api_product_id"]) != expectedProductID || strings.TrimSpace(metadata["new_api_merchant_id"]) != expectedMerchantID {
		return nil, errStripeCheckoutMismatch
	}
	// A session marked paid must never be transitioned to failed by an async
	// failure callback. Stripe normally reports "unpaid" here; accepting an
	// empty value keeps compatibility with minimally expanded historical
	// payloads while still rejecting the contradictory paid state.
	if expectedStatus == stripe.CheckoutSessionStatusComplete && checkout.PaymentStatus == stripe.CheckoutSessionPaymentStatusPaid {
		return nil, errStripeCheckoutInvalid
	}
	return checkout, nil
}

func compareStripeAmounts(expected, actual string) bool {
	a, errA := decimal.NewFromString(strings.TrimSpace(expected))
	b, errB := decimal.NewFromString(strings.TrimSpace(actual))
	return errA == nil && errB == nil && a.Equal(b)
}

func stripeMetadata(referenceID, kind, productID, orderName, providerAccountID string, creditedQuota int) map[string]string {
	return map[string]string{
		"new_api_order_id":       referenceID,
		"new_api_order_kind":     kind,
		"new_api_product_id":     strings.TrimSpace(productID),
		"new_api_order_name":     strings.TrimSpace(orderName),
		"new_api_credited_quota": strconv.Itoa(creditedQuota),
		"new_api_merchant_id":    strings.TrimSpace(providerAccountID),
	}
}

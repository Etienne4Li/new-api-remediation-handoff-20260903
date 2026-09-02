package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	pancake "github.com/waffo-com/waffo-pancake-sdk-go"
)

// WaffoPancakeProductTypeOneTime is the only Pancake product type currently
// supported by new-api. Pancake's recurring products emit renewal and
// cancellation events that this integration deliberately does not consume.
// Keep this value explicit in checkout and product metadata so a product
// cannot silently acquire recurring semantics as lifecycle support evolves.
const (
	WaffoPancakeProductTypeOneTime     = "onetime"
	WaffoPancakeProductTypeMetadataKey = "new_api_product_type"
)

// ErrWaffoPancakeUnsupportedProductType is returned when a caller attempts to
// create a checkout for a product type whose lifecycle is not implemented.
var ErrWaffoPancakeUnsupportedProductType = errors.New("Waffo Pancake recurring products are not supported")

// ErrWaffoPancakeBindingInvalid means that a configured store/product pair
// could not be proven to be an active one-time product in the authenticated
// Pancake catalog. Configuration must fail closed here: a product ID has the
// same shape for one-time and recurring products, so the local metadata marker
// is not authoritative evidence of its provider-side type.
var ErrWaffoPancakeBindingInvalid = errors.New("invalid Waffo Pancake store/product binding")

// NormalizeWaffoPancakeProductType validates and canonicalizes the product
// type used by the Pancake integration. It intentionally accepts only the
// one-time type; accepting a recurring type without a verified lifecycle
// handler could charge a customer while leaving local access stale.
func NormalizeWaffoPancakeProductType(value string) (string, error) {
	if strings.EqualFold(strings.TrimSpace(value), WaffoPancakeProductTypeOneTime) {
		return WaffoPancakeProductTypeOneTime, nil
	}
	return "", ErrWaffoPancakeUnsupportedProductType
}

// WaffoPancakePriceSnapshot is the per-session price override sent with checkout.
type WaffoPancakePriceSnapshot struct {
	Amount      string
	TaxCategory string
}

// WaffoPancakeCreateSessionParams is the input to CreateWaffoPancakeCheckoutSession.
// BuyerIdentity must be stable per user (see WaffoPancakeBuyerIdentityFromUserID).
// OrderMerchantExternalID = our trade_no; Pancake echoes it back in webhooks.
type WaffoPancakeCreateSessionParams struct {
	ProductID string
	// ProductType is required and currently must be "onetime". It is kept
	// separate from ProductID because Pancake uses the same PROD_* identifier
	// shape for one-time and recurring products.
	ProductType             string
	Currency                string
	BuyerIdentity           string
	PriceSnapshot           *WaffoPancakePriceSnapshot
	BuyerEmail              string
	ExpiresInSeconds        *int
	OrderMerchantExternalID string
	// Metadata is echoed in signed order webhooks. Callers use it to bind a
	// provider event to the immutable local checkout snapshot (order kind,
	// product, amount, currency, and merchant/store identifiers).
	Metadata map[string]string
}

// WaffoPancakeCheckoutSession is the response of CreateWaffoPancakeCheckoutSession.
// CheckoutURL already carries the `#token=...` fragment; Token / TokenExpiresAt
// are exposed separately for self-service flows driven from new-api's own UI.
type WaffoPancakeCheckoutSession struct {
	SessionID      string
	CheckoutURL    string
	ExpiresAt      string
	OrderID        string
	Token          string
	TokenExpiresAt string
}

// WaffoPancakeWebhookEvent mirrors the SDK's WebhookEvent shape using plain
// strings so controllers don't have to import the SDK package.
type WaffoPancakeWebhookEvent struct {
	ID        string
	Timestamp string
	EventType string
	EventID   string
	StoreID   string
	Mode      string
	Data      WaffoPancakeWebhookData
}

type WaffoPancakeWebhookData struct {
	// OrderID = Pancake ORD_* (logs); OrderMerchantExternalID = our trade_no (lookup).
	OrderID                 string
	OrderStatus             string
	OrderMerchantExternalID string
	// RefundTicketMerchantExternalID is present only on refund.* events. It is
	// deliberately kept separate from OrderMerchantExternalID: the latter is
	// the immutable local checkout identity and is the only safe lookup key.
	RefundTicketMerchantExternalID string
	BuyerEmail                     string
	Currency                       string
	Amount                         string
	TaxAmount                      string
	Subtotal                       string
	Total                          string
	ProductName                    string
	MerchantProvidedBuyerIdentity  string
	OrderMetadata                  map[string]string
	ProductMetadata                map[string]string
	PaymentID                      string
	PaymentStatus                  string
	PaymentMethod                  string
	PaymentFailureReason           string
	PaymentDate                    string
	BillingPeriod                  string
	CurrentPeriodStart             string
	CurrentPeriodEnd               string
	CanceledAt                     string
	RefundStatus                   string
	RefundReason                   string
	RefundCreatedAt                string
}

// NormalizedEventType returns the event type or empty string for a nil event.
func (e *WaffoPancakeWebhookEvent) NormalizedEventType() string {
	if e == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(e.EventType))
}

// newWaffoPancakeClient builds an SDK client from persisted settings. The
// runtime checkout / webhook paths use this; configuration endpoints use
// newWaffoPancakeClientFromCreds so the operator can verify typed-but-not-
// yet-saved credentials.
func newWaffoPancakeClient() (*pancake.Client, error) {
	return newWaffoPancakeClientWithConfig(setting.GetWaffoPancakeConfig())
}

func newWaffoPancakeClientWithConfig(cfg setting.WaffoPancakeConfig) (*pancake.Client, error) {
	return pancake.New(pancake.Config{
		MerchantID: cfg.MerchantID,
		PrivateKey: cfg.PrivateKey,
	})
}

func newWaffoPancakeClientFromCreds(merchantID, privateKey string) (*pancake.Client, error) {
	if strings.TrimSpace(merchantID) == "" || strings.TrimSpace(privateKey) == "" {
		return nil, fmt.Errorf("merchant id and private key are required")
	}
	return pancake.New(pancake.Config{
		MerchantID: merchantID,
		PrivateKey: privateKey,
	})
}

// CreateWaffoPancakeCheckoutSession creates an Authenticated-mode checkout
// session: the order is bound to BuyerIdentity (stable per user) so it stays
// attributable even if the buyer edits the email on Waffo's checkout form.
func CreateWaffoPancakeCheckoutSession(ctx context.Context, params *WaffoPancakeCreateSessionParams) (*WaffoPancakeCheckoutSession, error) {
	return CreateWaffoPancakeCheckoutSessionWithConfig(ctx, params, setting.GetWaffoPancakeConfig())
}

// CreateWaffoPancakeCheckoutSessionWithConfig is the request-snapshot variant
// used when the controller already captured a coherent gateway configuration.
func CreateWaffoPancakeCheckoutSessionWithConfig(ctx context.Context, params *WaffoPancakeCreateSessionParams, cfg setting.WaffoPancakeConfig) (*WaffoPancakeCheckoutSession, error) {
	if params == nil {
		return nil, fmt.Errorf("missing checkout params")
	}
	if strings.TrimSpace(params.BuyerIdentity) == "" {
		return nil, fmt.Errorf("missing buyer identity")
	}
	if strings.TrimSpace(params.OrderMerchantExternalID) == "" {
		return nil, fmt.Errorf("missing order merchant external id")
	}
	currency := strings.ToUpper(strings.TrimSpace(params.Currency))
	if currency == "" {
		currency = "USD"
	}
	if len(currency) != 3 {
		return nil, fmt.Errorf("invalid checkout currency")
	}
	productID := strings.TrimSpace(params.ProductID)
	if productID == "" {
		return nil, fmt.Errorf("missing product id")
	}
	productType, err := NormalizeWaffoPancakeProductType(params.ProductType)
	if err != nil {
		return nil, err
	}
	metadata := make(map[string]string, len(params.Metadata)+1)
	for key, value := range params.Metadata {
		metadata[key] = value
	}
	if declared := strings.TrimSpace(metadata[WaffoPancakeProductTypeMetadataKey]); declared != "" && !strings.EqualFold(declared, productType) {
		return nil, fmt.Errorf("%w: checkout metadata declares %q", ErrWaffoPancakeUnsupportedProductType, declared)
	}
	// Always send the canonical marker, even when a caller omitted it. The
	// signed order webhook must carry this evidence for settlement to proceed.
	metadata[WaffoPancakeProductTypeMetadataKey] = productType
	client, err := newWaffoPancakeClientWithConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build Waffo Pancake client: %w", err)
	}

	sdkParams := pancake.AuthenticatedCheckoutParams{
		CreateCheckoutSessionParams: pancake.CreateCheckoutSessionParams{
			ProductID:               productID,
			Currency:                currency,
			BuyerEmail:              optionalString(params.BuyerEmail),
			ExpiresInSeconds:        params.ExpiresInSeconds,
			Metadata:                metadata,
			OrderMerchantExternalID: optionalString(params.OrderMerchantExternalID),
		},
		BuyerIdentity: params.BuyerIdentity,
	}
	// ProductType is validated above even though the Pancake checkout API does
	// not carry a separate type field. The type is echoed in our signed
	// metadata and checked again by the webhook settlement adapter.
	if params.PriceSnapshot != nil {
		sdkParams.PriceSnapshot = &pancake.PriceInfo{
			Amount:      params.PriceSnapshot.Amount,
			TaxCategory: pancake.TaxCategory(params.PriceSnapshot.TaxCategory),
		}
	}

	session, err := client.Checkout.Authenticated.Create(ctx, sdkParams)
	if err != nil {
		return nil, err
	}
	if session == nil || strings.TrimSpace(session.CheckoutURL) == "" || strings.TrimSpace(session.SessionID) == "" {
		return nil, fmt.Errorf("Waffo Pancake returned empty checkout session")
	}
	return &WaffoPancakeCheckoutSession{
		SessionID:      session.SessionID,
		CheckoutURL:    session.CheckoutURL,
		ExpiresAt:      session.ExpiresAt,
		Token:          session.Token,
		TokenExpiresAt: session.TokenExpiresAt,
	}, nil
}

func optionalString(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	v := s
	return &v
}

// WaffoPancakeBuyerIdentityFromUserID renders the canonical buyer identity
// for checkout. Webhook handlers compare against the value rendered here to
// reject identity mismatches, so both call sites must use this function.
func WaffoPancakeBuyerIdentityFromUserID(userID int) string {
	return fmt.Sprintf("new-api-user-%d", userID)
}

// VerifyConfiguredWaffoPancakeWebhook verifies the signature header. The SDK
// picks the matching test / prod public key from the payload's `mode` field.
func VerifyConfiguredWaffoPancakeWebhook(payload string, signatureHeader string) (*WaffoPancakeWebhookEvent, error) {
	evt, err := pancake.VerifyWebhookTyped[pancake.WebhookEventData](payload, signatureHeader, nil)
	if err != nil {
		return nil, err
	}
	identity := ""
	if evt.Data.MerchantProvidedBuyerIdentity != nil {
		identity = *evt.Data.MerchantProvidedBuyerIdentity
	}
	externalID := ""
	if evt.Data.OrderMerchantExternalID != nil {
		externalID = *evt.Data.OrderMerchantExternalID
	}
	paymentID := ""
	if evt.Data.PaymentID != nil {
		paymentID = strings.TrimSpace(*evt.Data.PaymentID)
	}
	paymentStatus := ""
	if evt.Data.PaymentStatus != nil {
		paymentStatus = strings.TrimSpace(*evt.Data.PaymentStatus)
	}
	return &WaffoPancakeWebhookEvent{
		ID:        evt.ID,
		Timestamp: evt.Timestamp,
		EventType: evt.EventType,
		EventID:   evt.EventID,
		StoreID:   evt.StoreID,
		Mode:      string(evt.Mode),
		Data: WaffoPancakeWebhookData{
			OrderID:                        evt.Data.OrderID,
			OrderStatus:                    optionalWebhookValue(evt.Data.OrderStatus),
			OrderMerchantExternalID:        externalID,
			RefundTicketMerchantExternalID: optionalWebhookValue(evt.Data.RefundTicketMerchantExternalID),
			BuyerEmail:                     evt.Data.BuyerEmail,
			Currency:                       evt.Data.Currency,
			Amount:                         evt.Data.Amount,
			TaxAmount:                      evt.Data.TaxAmount,
			Subtotal:                       optionalWebhookValue(evt.Data.Subtotal),
			Total:                          optionalWebhookValue(evt.Data.Total),
			ProductName:                    evt.Data.ProductName,
			MerchantProvidedBuyerIdentity:  identity,
			OrderMetadata:                  evt.Data.OrderMetadata,
			ProductMetadata:                evt.Data.ProductMetadata,
			PaymentID:                      paymentID,
			PaymentStatus:                  paymentStatus,
			PaymentMethod:                  optionalWebhookValue(evt.Data.PaymentMethod),
			PaymentFailureReason:           optionalWebhookValue(evt.Data.PaymentFailureReason),
			PaymentDate:                    optionalWebhookValue(evt.Data.PaymentDate),
			BillingPeriod:                  optionalWebhookValue(evt.Data.BillingPeriod),
			CurrentPeriodStart:             optionalWebhookValue(evt.Data.CurrentPeriodStart),
			CurrentPeriodEnd:               optionalWebhookValue(evt.Data.CurrentPeriodEnd),
			CanceledAt:                     optionalWebhookValue(evt.Data.CanceledAt),
			RefundStatus:                   optionalWebhookValue(evt.Data.RefundStatus),
			RefundReason:                   optionalWebhookValue(evt.Data.RefundReason),
			RefundCreatedAt:                optionalWebhookValue(evt.Data.RefundCreatedAt),
		},
	}, nil
}

func optionalWebhookValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

// ResolveWaffoPancakeTradeNo maps a verified webhook event to a local TopUp
// trade_no via OrderMerchantExternalID, and rejects buyer-identity mismatches.
func ResolveWaffoPancakeTradeNo(event *WaffoPancakeWebhookEvent) (string, error) {
	if event == nil {
		return "", fmt.Errorf("missing webhook event")
	}
	tradeNo := strings.TrimSpace(event.Data.OrderMerchantExternalID)
	if tradeNo == "" {
		return "", fmt.Errorf("missing webhook orderMerchantExternalId")
	}
	topUp := model.GetTopUpByTradeNo(tradeNo)
	if topUp == nil || topUp.PaymentProvider != model.PaymentProviderWaffoPancake {
		return "", fmt.Errorf("waffo pancake order not found for tradeNo=%s", tradeNo)
	}
	expectedIdentity := WaffoPancakeBuyerIdentityFromUserID(topUp.UserId)
	actualIdentity := strings.TrimSpace(event.Data.MerchantProvidedBuyerIdentity)
	if actualIdentity != expectedIdentity {
		return "", fmt.Errorf(
			"waffo pancake buyer identity mismatch for tradeNo=%s: expected=%q actual=%q",
			tradeNo,
			expectedIdentity,
			actualIdentity,
		)
	}
	return tradeNo, nil
}

// ResolveWaffoPancakeSubscriptionTradeNo is the SubscriptionOrder counterpart
// of ResolveWaffoPancakeTradeNo.
func ResolveWaffoPancakeSubscriptionTradeNo(event *WaffoPancakeWebhookEvent) (string, error) {
	if event == nil {
		return "", fmt.Errorf("missing webhook event")
	}
	tradeNo := strings.TrimSpace(event.Data.OrderMerchantExternalID)
	if tradeNo == "" {
		return "", fmt.Errorf("missing webhook orderMerchantExternalId")
	}
	order := model.GetSubscriptionOrderByTradeNo(tradeNo)
	if order == nil || order.PaymentProvider != model.PaymentProviderWaffoPancake {
		return "", fmt.Errorf("waffo pancake subscription order not found for tradeNo=%s", tradeNo)
	}
	expectedIdentity := WaffoPancakeBuyerIdentityFromUserID(order.UserId)
	actualIdentity := strings.TrimSpace(event.Data.MerchantProvidedBuyerIdentity)
	if actualIdentity != expectedIdentity {
		return "", fmt.Errorf(
			"waffo pancake buyer identity mismatch for subscription tradeNo=%s: expected=%q actual=%q",
			tradeNo,
			expectedIdentity,
			actualIdentity,
		)
	}
	return tradeNo, nil
}

// Deterministic default names for "+ Create": stable bodies mean stable
// X-Idempotency-Key, which lets Pancake dedupe retries server-side.
const (
	defaultWaffoPancakeStoreName   = "new-api-store"
	defaultWaffoPancakeProductName = "new-api-charge-product"
)

// WaffoPancakePrimaryProductName is the stable name used by the built-in
// wallet product. Exposing it keeps checkout snapshots and webhook checks in
// the controller aligned with the configuration wizard's product creator.
func WaffoPancakePrimaryProductName() string { return defaultWaffoPancakeProductName }

// CreateWaffoPancakePrimaryStore creates a Pancake Store using in-flight
// (not-yet-persisted) credentials and returns the new store ID.
func CreateWaffoPancakePrimaryStore(ctx context.Context, merchantID, privateKey string) (string, error) {
	client, err := newWaffoPancakeClientFromCreds(merchantID, privateKey)
	if err != nil {
		return "", err
	}
	storeRes, err := client.Stores.Create(ctx, pancake.CreateStoreParams{
		Name: defaultWaffoPancakeStoreName,
	})
	if err != nil {
		return "", fmt.Errorf("create Waffo Pancake store: %w", err)
	}
	return storeRes.Store.ID, nil
}

// CreateWaffoPancakeOneTimeProductForPlan mints (and publishes) a Pancake
// OnetimeProduct priced at `amount` USD, used as a time-limited plan's
// SubscriptionPlan.WaffoPancakeProductId. The local plan grants one period
// after a successful payment; this product never opts the buyer into
// Pancake's recurring billing lifecycle.
//
// A recurring Pancake product must not be passed to this flow until renewal
// and cancellation webhooks are implemented and verified.
func CreateWaffoPancakeOneTimeProductForPlan(ctx context.Context, merchantID, privateKey, storeID, name, amount, returnURL string) (string, error) {
	storeID = strings.TrimSpace(storeID)
	if storeID == "" {
		return "", fmt.Errorf("store id is required to create a product")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("plan name is required")
	}
	amount = strings.TrimSpace(amount)
	if amount == "" {
		return "", fmt.Errorf("plan price is required")
	}
	client, err := newWaffoPancakeClientFromCreds(merchantID, privateKey)
	if err != nil {
		return "", err
	}
	prodRes, err := client.OnetimeProducts.Create(ctx, pancake.CreateOnetimeProductParams{
		StoreID: storeID,
		Name:    name,
		Prices: pancake.Prices{
			"USD": {
				Amount:      amount,
				TaxCategory: pancake.TaxCategory("saas"),
			},
		},
		Metadata: map[string]any{
			WaffoPancakeProductTypeMetadataKey: WaffoPancakeProductTypeOneTime,
		},
		SuccessURL: optionalString(strings.TrimSpace(returnURL)),
	})
	if err != nil {
		return "", fmt.Errorf("create Waffo Pancake plan product: %w", err)
	}
	productID := prodRes.Product.ID
	if _, err := client.OnetimeProducts.Publish(ctx, pancake.PublishOnetimeProductParams{ID: productID}); err != nil {
		return "", fmt.Errorf("publish Waffo Pancake plan product: %w", err)
	}
	return productID, nil
}

// CreateWaffoPancakeProductForPlan is retained as a source-compatible alias
// for integrations compiled against earlier releases. Despite the historical
// name, it always creates a one-time product; use
// CreateWaffoPancakeOneTimeProductForPlan for new callers.
func CreateWaffoPancakeProductForPlan(ctx context.Context, merchantID, privateKey, storeID, name, amount, returnURL string) (string, error) {
	return CreateWaffoPancakeOneTimeProductForPlan(ctx, merchantID, privateKey, storeID, name, amount, returnURL)
}

// CreateWaffoPancakePrimaryProduct mints (and publishes) the wallet-top-up
// OnetimeProduct under storeID. Per-checkout price overrides via PriceSnapshot
// are what make the "1.00" seed price irrelevant at runtime.
func CreateWaffoPancakePrimaryProduct(ctx context.Context, merchantID, privateKey, storeID, returnURL string) (string, error) {
	storeID = strings.TrimSpace(storeID)
	if storeID == "" {
		return "", fmt.Errorf("store id is required to create a product")
	}
	client, err := newWaffoPancakeClientFromCreds(merchantID, privateKey)
	if err != nil {
		return "", err
	}
	prodRes, err := client.OnetimeProducts.Create(ctx, pancake.CreateOnetimeProductParams{
		StoreID: storeID,
		Name:    defaultWaffoPancakeProductName,
		Prices: pancake.Prices{
			"USD": {
				Amount:      "1.00", // overridden at checkout via PriceSnapshot
				TaxCategory: pancake.TaxCategory("saas"),
			},
		},
		Metadata: map[string]any{
			WaffoPancakeProductTypeMetadataKey: WaffoPancakeProductTypeOneTime,
		},
		SuccessURL: optionalString(strings.TrimSpace(returnURL)),
	})
	if err != nil {
		return "", fmt.Errorf("create Waffo Pancake product: %w", err)
	}
	productID := prodRes.Product.ID
	if _, err := client.OnetimeProducts.Publish(ctx, pancake.PublishOnetimeProductParams{ID: productID}); err != nil {
		return "", fmt.Errorf("publish Waffo Pancake product: %w", err)
	}
	return productID, nil
}

// WaffoPancakePairResult is the response of CreateWaffoPancakePrimaryPair.
// When OrphanStore is true the store was created but the product wasn't,
// so the caller can surface a partial-failure message with StoreID.
type WaffoPancakePairResult struct {
	StoreID     string
	StoreName   string
	ProductID   string
	ProductName string
	OrphanStore bool
}

// CreateWaffoPancakePrimaryPair mints a Store + OnetimeProduct in one
// round-trip — the canonical "+ Create" entry point. Nothing is persisted
// to settings; the operator's final Save commits the chosen IDs.
func CreateWaffoPancakePrimaryPair(ctx context.Context, merchantID, privateKey, returnURL string) (*WaffoPancakePairResult, error) {
	storeID, err := CreateWaffoPancakePrimaryStore(ctx, merchantID, privateKey)
	if err != nil {
		return nil, err
	}
	productID, err := CreateWaffoPancakePrimaryProduct(ctx, merchantID, privateKey, storeID, returnURL)
	if err != nil {
		return &WaffoPancakePairResult{
			StoreID:     storeID,
			StoreName:   defaultWaffoPancakeStoreName,
			OrphanStore: true,
		}, fmt.Errorf("store created at %s but product creation failed: %w", storeID, err)
	}
	return &WaffoPancakePairResult{
		StoreID:     storeID,
		StoreName:   defaultWaffoPancakeStoreName,
		ProductID:   productID,
		ProductName: defaultWaffoPancakeProductName,
	}, nil
}

// SaveWaffoPancakeConfig persists the operator-controlled fields atomically
// at the end of the configuration flow via model.UpdateOptionsBulk (single
// DB transaction). A blank privateKey is treated as "keep current"
// (Stripe-style API-secret UX) and is omitted from the bulk payload.
func SaveWaffoPancakeConfig(ctx context.Context, merchantID, privateKey, returnURL, storeID, productID string) error {
	merchantID = strings.TrimSpace(merchantID)
	storeID = strings.TrimSpace(storeID)
	productID = strings.TrimSpace(productID)
	if merchantID == "" || storeID == "" || productID == "" {
		return fmt.Errorf("merchant id, store id, and product id are required to save")
	}
	// A blank key means "keep the existing key" in the admin UI. Resolve that
	// value before validating so a save can never persist an unverified product
	// ID merely because the key field is intentionally redacted on GET /option.
	if strings.TrimSpace(privateKey) == "" {
		privateKey = strings.TrimSpace(setting.GetWaffoPancakeConfig().PrivateKey)
	}
	if privateKey == "" {
		return fmt.Errorf("private key is required to verify the store/product binding")
	}
	if err := ValidateWaffoPancakeBinding(ctx, merchantID, privateKey, storeID, productID); err != nil {
		return err
	}
	values := map[string]string{
		"WaffoPancakeMerchantID": merchantID,
		"WaffoPancakeReturnURL":  strings.TrimSpace(returnURL),
		"WaffoPancakeStoreID":    storeID,
		"WaffoPancakeProductID":  productID,
	}
	if pk := strings.TrimSpace(privateKey); pk != "" {
		values["WaffoPancakePrivateKey"] = pk
	}
	if err := model.UpdateOptionsBulk(values); err != nil {
		return fmt.Errorf("persist Waffo Pancake config: %w", err)
	}
	return nil
}

type WaffoPancakeCatalogProduct struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	ProductType string `json:"product_type"`
}

// WaffoPancakeCatalogStore nests its OnetimeProducts so the UI can render a
// dependent store→product select without a second round-trip.
type WaffoPancakeCatalogStore struct {
	ID              string                       `json:"id"`
	Name            string                       `json:"name"`
	Status          string                       `json:"status"`
	ProdEnabled     bool                         `json:"prodEnabled"`
	OnetimeProducts []WaffoPancakeCatalogProduct `json:"onetimeProducts"`
}

type WaffoPancakeCatalog struct {
	Stores []WaffoPancakeCatalogStore `json:"stores"`
}

// validateWaffoPancakeBindingInCatalog is kept separate from the network
// query so the security rule is deterministic and directly regression-tested.
func validateWaffoPancakeBindingInCatalog(catalog *WaffoPancakeCatalog, storeID, productID string) error {
	if catalog == nil {
		return fmt.Errorf("%w: empty catalog", ErrWaffoPancakeBindingInvalid)
	}
	storeID = strings.TrimSpace(storeID)
	productID = strings.TrimSpace(productID)
	if storeID == "" || productID == "" {
		return fmt.Errorf("%w: store and product are required", ErrWaffoPancakeBindingInvalid)
	}
	for _, store := range catalog.Stores {
		if strings.TrimSpace(store.ID) != storeID {
			continue
		}
		if status := strings.TrimSpace(store.Status); status != "" && !strings.EqualFold(status, "active") {
			return fmt.Errorf("%w: store %q is %s", ErrWaffoPancakeBindingInvalid, storeID, status)
		}
		for _, product := range store.OnetimeProducts {
			if strings.TrimSpace(product.ID) == productID &&
				strings.EqualFold(strings.TrimSpace(product.ProductType), WaffoPancakeProductTypeOneTime) &&
				(strings.TrimSpace(product.Status) == "" || strings.EqualFold(strings.TrimSpace(product.Status), "active")) {
				return nil
			}
		}
		return fmt.Errorf("%w: product %q is not an active one-time product in store %q", ErrWaffoPancakeBindingInvalid, productID, storeID)
	}
	return fmt.Errorf("%w: store %q was not found", ErrWaffoPancakeBindingInvalid, storeID)
}

// ValidateWaffoPancakeBinding performs an authoritative provider catalog
// lookup for an operator-selected binding. It is used before persisting the
// gateway configuration and can also be called by runtime checkout paths when
// a remote product may have been deactivated since the last save.
func ValidateWaffoPancakeBinding(ctx context.Context, merchantID, privateKey, storeID, productID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	merchantID = strings.TrimSpace(merchantID)
	privateKey = strings.TrimSpace(privateKey)
	if merchantID == "" || privateKey == "" {
		return fmt.Errorf("merchant id and private key are required to verify the store/product binding")
	}
	catalog, err := ListWaffoPancakeCatalog(ctx, merchantID, privateKey)
	if err != nil {
		return fmt.Errorf("verify Waffo Pancake store/product binding: %w", err)
	}
	return validateWaffoPancakeBindingInCatalog(catalog, storeID, productID)
}

// ListWaffoPancakeCatalog queries Pancake's GraphQL `stores` for the
// merchant's stores + onetime products. A successful call also proves
// the supplied credentials authenticate (doubles as a credential probe).
func ListWaffoPancakeCatalog(ctx context.Context, merchantID, privateKey string) (*WaffoPancakeCatalog, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	client, err := newWaffoPancakeClientFromCreds(merchantID, privateKey)
	if err != nil {
		return nil, err
	}

	type storesQuery struct {
		Stores []WaffoPancakeCatalogStore `json:"stores"`
	}
	// Pancake exposes products as a top-level connection, not as a nested
	// field on `stores`. Query stores first, then ask for the active one-time
	// products belonging to each returned store. Keeping the store ID in a
	// GraphQL variable avoids interpolating provider-controlled identifiers.
	resp, err := pancake.GraphQLQuery[storesQuery](ctx, client, pancake.GraphQLParams{
		Query: `query { stores { id name status prodEnabled } }`,
	})
	if err != nil {
		return nil, fmt.Errorf("query Waffo Pancake catalog: %w", err)
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("waffo pancake catalog query returned %d errors: %s",
			len(resp.Errors), resp.Errors[0].Message)
	}
	stores := resp.Data.Stores
	type productsQuery struct {
		OnetimeProducts []WaffoPancakeCatalogProduct `json:"onetimeProducts"`
	}
	for i := range stores {
		storeID := strings.TrimSpace(stores[i].ID)
		if storeID == "" {
			continue
		}
		productsResp, productErr := pancake.GraphQLQuery[productsQuery](ctx, client, pancake.GraphQLParams{
			Query: `query ($storeId: String!) {
				onetimeProducts(storeId: $storeId, filter: { status: { eq: "active" } }) {
					id
					name
					status
				}
			}`,
			Variables: map[string]any{"storeId": storeID},
		})
		if productErr != nil {
			return nil, fmt.Errorf("query Waffo Pancake products for store %q: %w", storeID, productErr)
		}
		if len(productsResp.Errors) > 0 {
			return nil, fmt.Errorf("Waffo Pancake product query for store %q returned %d errors: %s",
				storeID, len(productsResp.Errors), productsResp.Errors[0].Message)
		}
		for _, product := range productsResp.Data.OnetimeProducts {
			if strings.EqualFold(strings.TrimSpace(product.Status), "active") {
				product.ProductType = WaffoPancakeProductTypeOneTime
				stores[i].OnetimeProducts = append(stores[i].OnetimeProducts, product)
			}
		}
	}
	return &WaffoPancakeCatalog{Stores: stores}, nil
}

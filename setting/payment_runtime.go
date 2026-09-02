package setting

import (
	"sync"

	"github.com/QuantumNous/new-api/common"
)

// StripeConfig is a coherent snapshot of the Stripe top-up configuration.
// The individual exported variables remain for source compatibility, but
// request/webhook code must use GetStripeConfig so a hot update cannot expose
// a partially changed credential/product/currency tuple.
type StripeConfig struct {
	ApiSecret             string
	WebhookSecret         string
	AccountID             string
	PriceID               string
	UnitPrice             float64
	Currency              string
	MinTopUp              int
	PromotionCodesEnabled bool
}

var paymentConfigMu sync.RWMutex

func stripeConfigFromLegacy() StripeConfig {
	return StripeConfig{
		ApiSecret:             StripeApiSecret,
		WebhookSecret:         StripeWebhookSecret,
		AccountID:             StripeAccountId,
		PriceID:               StripePriceId,
		UnitPrice:             StripeUnitPrice,
		Currency:              StripeCurrency,
		MinTopUp:              StripeMinTopUp,
		PromotionCodesEnabled: StripePromotionCodesEnabled,
	}
}

func applyStripeConfigToLegacy(cfg StripeConfig) {
	StripeApiSecret = cfg.ApiSecret
	StripeWebhookSecret = cfg.WebhookSecret
	StripeAccountId = cfg.AccountID
	StripePriceId = cfg.PriceID
	StripeUnitPrice = cfg.UnitPrice
	StripeCurrency = cfg.Currency
	StripeMinTopUp = cfg.MinTopUp
	StripePromotionCodesEnabled = cfg.PromotionCodesEnabled
}

// GetStripeConfig returns a copy suitable for one request or webhook.
func GetStripeConfig() StripeConfig {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	paymentConfigMu.RLock()
	defer paymentConfigMu.RUnlock()
	return stripeConfigFromLegacy()
}

// UpdateStripeConfig atomically publishes related Stripe fields.  The
// callback mutates a private copy, so readers never observe a half-published
// tuple when an administrator changes several payment options in one update.
func UpdateStripeConfig(update func(*StripeConfig)) {
	if update == nil {
		return
	}
	paymentConfigMu.Lock()
	defer paymentConfigMu.Unlock()
	cfg := stripeConfigFromLegacy()
	update(&cfg)
	applyStripeConfigToLegacy(cfg)
}

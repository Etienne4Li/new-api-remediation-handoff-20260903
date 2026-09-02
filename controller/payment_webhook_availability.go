package controller

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

func isPaymentComplianceConfirmed() bool {
	return operation_setting.IsPaymentComplianceConfirmed()
}

func isStripeTopUpEnabled() bool {
	if !isPaymentComplianceConfirmed() {
		return false
	}
	cfg := setting.GetStripeConfig()
	return strings.TrimSpace(cfg.ApiSecret) != "" &&
		strings.TrimSpace(cfg.WebhookSecret) != "" &&
		strings.TrimSpace(cfg.PriceID) != ""
}

func isStripeWebhookConfigured() bool {
	return strings.TrimSpace(setting.GetStripeConfig().WebhookSecret) != ""
}

func isStripeWebhookEnabled() bool {
	// Webhook draining is independent from new-sale controls. Price IDs,
	// compliance acknowledgement, and the checkout API secret are not needed
	// to authenticate a signed event for an already-created order.
	return isStripeWebhookConfigured()
}

func isCreemTopUpEnabled() bool {
	if !isPaymentComplianceConfirmed() {
		return false
	}
	cfg := setting.GetCreemConfig()
	return isCreemTopUpEnabledWithConfig(cfg)
}

func isCreemTopUpEnabledWithConfig(cfg setting.CreemConfig) bool {
	products := strings.TrimSpace(cfg.Products)
	return strings.TrimSpace(cfg.ApiKey) != "" &&
		strings.TrimSpace(cfg.WebhookSecret) != "" &&
		products != "" &&
		products != "[]"
}

func isCreemWebhookConfigured() bool {
	return isCreemWebhookConfiguredWithConfig(setting.GetCreemConfig())
}

func isCreemWebhookConfiguredWithConfig(cfg setting.CreemConfig) bool {
	return strings.TrimSpace(cfg.WebhookSecret) != ""
}

func isCreemWebhookEnabled() bool {
	// Callback processing is deliberately independent from new-sale controls.
	// An operator may drain top-up sales or keep only subscription products
	// configured while still needing to settle already-created orders.
	return isCreemWebhookConfigured()
}

func isCreemWebhookEnabledWithConfig(cfg setting.CreemConfig) bool {
	return isCreemWebhookConfiguredWithConfig(cfg)
}

// isCreemSubscriptionCheckoutEnabled guards creation of new subscription
// checkouts. A webhook secret is mandatory even in test mode: without it the
// public callback endpoint cannot authenticate the eventual payment event and
// the newly-created order would remain pending indefinitely.
func isCreemSubscriptionCheckoutEnabled() bool {
	cfg := setting.GetCreemConfig()
	return isCreemSubscriptionCheckoutEnabledWithConfig(cfg)
}

func isCreemSubscriptionCheckoutEnabledWithConfig(cfg setting.CreemConfig) bool {
	return isPaymentComplianceConfirmed() &&
		strings.TrimSpace(cfg.ApiKey) != "" &&
		strings.TrimSpace(cfg.WebhookSecret) != ""
}

func isWaffoTopUpEnabled() bool {
	if !isPaymentComplianceConfirmed() {
		return false
	}
	return isWaffoTopUpEnabledWithConfig(setting.GetWaffoConfig())
}

func isWaffoTopUpEnabledWithConfig(cfg setting.WaffoConfig) bool {
	return cfg.Enabled && isWaffoWebhookConfiguredWithConfig(cfg)
}

func isWaffoWebhookConfigured() bool {
	return isWaffoWebhookConfiguredWithConfig(setting.GetWaffoConfig())
}

func isWaffoWebhookConfiguredWithConfig(cfg setting.WaffoConfig) bool {
	if cfg.Sandbox {
		return strings.TrimSpace(cfg.SandboxApiKey) != "" &&
			strings.TrimSpace(cfg.SandboxPrivateKey) != "" &&
			strings.TrimSpace(cfg.SandboxPublicCert) != ""
	}

	return strings.TrimSpace(cfg.ApiKey) != "" &&
		strings.TrimSpace(cfg.PrivateKey) != "" &&
		strings.TrimSpace(cfg.PublicCert) != ""
}

func isWaffoWebhookEnabled() bool {
	// Keep accepting callbacks for in-flight orders while an operator pauses
	// new Waffo sales or revokes the compliance flag.
	return isWaffoWebhookConfigured()
}

func isWaffoWebhookEnabledWithConfig(cfg setting.WaffoConfig) bool {
	return isWaffoWebhookConfiguredWithConfig(cfg)
}

func isWaffoPancakeTopUpEnabled() bool {
	if !isPaymentComplianceConfirmed() {
		return false
	}
	return isWaffoPancakeTopUpEnabledWithConfig(setting.GetWaffoPancakeConfig())
}

func isWaffoPancakeTopUpEnabledWithConfig(cfg setting.WaffoPancakeConfig) bool {
	// Presence-of-credentials = enabled. Webhook public keys ship inside
	// the SDK; mode (test/prod) is read from each event.
	return strings.TrimSpace(cfg.MerchantID) != "" &&
		strings.TrimSpace(cfg.PrivateKey) != "" &&
		strings.TrimSpace(cfg.ProductID) != ""
}

func isWaffoPancakeWebhookConfigured() bool {
	return isWaffoPancakeWebhookConfiguredWithConfig(setting.GetWaffoPancakeConfig())
}

func isWaffoPancakeWebhookConfiguredWithConfig(cfg setting.WaffoPancakeConfig) bool {
	// ProductID is a sales control. Existing subscription orders can still be
	// settled after the wallet product is disabled, so webhook availability
	// only depends on the merchant/store binding used in event metadata.
	return strings.TrimSpace(cfg.MerchantID) != "" &&
		strings.TrimSpace(cfg.StoreID) != ""
}

func isWaffoPancakeWebhookEnabled() bool {
	// Webhook processing must remain available while new Pancake sales are
	// drained or the wallet product is disabled. Only the merchant/store
	// binding is required to route and authenticate an in-flight callback.
	return isWaffoPancakeWebhookConfigured()
}

func isWaffoPancakeWebhookEnabledWithConfig(cfg setting.WaffoPancakeConfig) bool {
	return isWaffoPancakeWebhookConfiguredWithConfig(cfg)
}

func isEpayTopUpEnabled() bool {
	return isEpayCheckoutEnabled()
}

// isEpayCheckoutEnabled controls creation of new EPay payments. It is kept
// separate from webhook availability so an operator can stop sales without
// strand­ing payments that are already in flight.
func isEpayCheckoutEnabled() bool {
	cfg := operation_setting.GetPaymentRuntimeConfig()
	return isEpayCheckoutEnabledWithConfig(cfg, operation_setting.GetPaymentSettingSnapshot())
}

func isEpayCheckoutEnabledWithConfig(cfg operation_setting.PaymentRuntimeConfig, paymentSetting operation_setting.PaymentSetting) bool {
	return paymentSetting.ComplianceConfirmed &&
		paymentSetting.ComplianceTermsVersion == operation_setting.CurrentComplianceTermsVersion &&
		isEpayWebhookConfiguredWithConfig(cfg) && len(cfg.PayMethods) > 0
}

func isEpayWebhookConfigured() bool {
	return isEpayWebhookConfiguredWithConfig(operation_setting.GetPaymentRuntimeConfig())
}

func isEpayWebhookConfiguredWithConfig(cfg operation_setting.PaymentRuntimeConfig) bool {
	return strings.TrimSpace(cfg.PayAddress) != "" &&
		strings.TrimSpace(cfg.EpayID) != "" &&
		strings.TrimSpace(cfg.EpayKey) != ""
}

func isEpayWebhookEnabled() bool {
	// Do not couple this to PayMethods or compliance: both are sales controls,
	// while a valid provider callback must remain processable during a drain.
	return isEpayWebhookConfigured()
}

// EPay's legacy integrations sometimes send the signed notification as a GET
// query string.  Query strings are routinely copied into reverse-proxy access
// logs, browser history and tracing metadata, so POST is the safe default for
// new deployments.  Keep an explicit, operator-controlled compatibility
// switch for providers that cannot be migrated immediately; the notify
// handler still performs the same full signature and order-snapshot checks.
func isEpayGetWebhookEnabled() bool {
	return common.GetEnvOrDefaultBool("EPAY_NOTIFY_GET_ENABLED", false)
}

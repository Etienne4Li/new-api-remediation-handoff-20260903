package setting

var StripeApiSecret = ""
var StripeWebhookSecret = ""
var StripeAccountId = ""
var StripePriceId = ""
var StripeUnitPrice = 8.0

// StripeCurrency is the currency configured for the Stripe Price used by
// top-up checkout. Stripe webhook amounts are compared against this snapshot.
var StripeCurrency = "USD"
var StripeMinTopUp = 1
var StripePromotionCodesEnabled = false

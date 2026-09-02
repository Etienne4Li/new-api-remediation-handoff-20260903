package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func confirmPaymentComplianceForTest(t *testing.T) {
	t.Helper()
	paymentSetting := operation_setting.GetPaymentSetting()
	originalConfirmed := paymentSetting.ComplianceConfirmed
	originalTermsVersion := paymentSetting.ComplianceTermsVersion
	t.Cleanup(func() {
		paymentSetting.ComplianceConfirmed = originalConfirmed
		paymentSetting.ComplianceTermsVersion = originalTermsVersion
	})
	paymentSetting.ComplianceConfirmed = true
	paymentSetting.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion
}

func TestStripeWebhookEnabledIsIndependentFromCheckoutConfig(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	originalAPISecret := setting.StripeApiSecret
	originalWebhookSecret := setting.StripeWebhookSecret
	originalPriceID := setting.StripePriceId
	t.Cleanup(func() {
		setting.StripeApiSecret = originalAPISecret
		setting.StripeWebhookSecret = originalWebhookSecret
		setting.StripePriceId = originalPriceID
	})

	setting.StripeWebhookSecret = ""
	setting.StripeApiSecret = "sk_test_123"
	setting.StripePriceId = "price_123"
	require.False(t, isStripeWebhookEnabled())

	setting.StripeWebhookSecret = "whsec_test"
	require.True(t, isStripeWebhookEnabled())

	setting.StripePriceId = ""
	require.True(t, isStripeWebhookEnabled(), "draining in-flight Stripe orders must not depend on a new-sale price")
	setting.StripeWebhookSecret = ""
	require.False(t, isStripeWebhookEnabled())
}

func TestCreemWebhookEnabledIsIndependentFromTopUpConfig(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	originalAPIKey := setting.CreemApiKey
	originalProducts := setting.CreemProducts
	originalWebhookSecret := setting.CreemWebhookSecret
	t.Cleanup(func() {
		setting.CreemApiKey = originalAPIKey
		setting.CreemProducts = originalProducts
		setting.CreemWebhookSecret = originalWebhookSecret
	})

	setting.CreemWebhookSecret = ""
	setting.CreemApiKey = "creem_api_key"
	setting.CreemProducts = `[{"productId":"prod_123"}]`
	require.False(t, isCreemWebhookEnabled())

	setting.CreemWebhookSecret = "creem_secret"
	require.True(t, isCreemWebhookEnabled())

	setting.CreemProducts = "[]"
	require.True(t, isCreemWebhookEnabled(), "in-flight subscription callbacks must not depend on the top-up product catalog")

	setting.CreemApiKey = ""
	require.True(t, isCreemWebhookEnabled(), "webhook settlement only needs the signing secret")
	operation_setting.GetPaymentSetting().ComplianceConfirmed = false
	require.True(t, isCreemWebhookEnabled(), "draining new sales must not disable in-flight callbacks")

	setting.CreemWebhookSecret = ""
	require.False(t, isCreemWebhookEnabled())
}

func TestCreemTopUpEnabledRequiresWebhookSecret(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	originalAPIKey := setting.CreemApiKey
	originalProducts := setting.CreemProducts
	originalWebhookSecret := setting.CreemWebhookSecret
	t.Cleanup(func() {
		setting.CreemApiKey = originalAPIKey
		setting.CreemProducts = originalProducts
		setting.CreemWebhookSecret = originalWebhookSecret
	})

	setting.CreemApiKey = "creem_api_key"
	setting.CreemProducts = `[{"productId":"prod_123"}]`
	setting.CreemWebhookSecret = ""
	require.False(t, isCreemTopUpEnabled(), "selling a top-up without a verifiable callback would strand the order")

	setting.CreemWebhookSecret = "creem_secret"
	require.True(t, isCreemTopUpEnabled())
}

func TestCreemSubscriptionCheckoutRequiresWebhookSecretInTestMode(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	originalAPIKey := setting.CreemApiKey
	originalWebhookSecret := setting.CreemWebhookSecret
	originalTestMode := setting.CreemTestMode
	t.Cleanup(func() {
		setting.CreemApiKey = originalAPIKey
		setting.CreemWebhookSecret = originalWebhookSecret
		setting.CreemTestMode = originalTestMode
	})

	setting.CreemApiKey = "creem_api_key"
	setting.CreemTestMode = true
	setting.CreemWebhookSecret = ""
	require.False(t, isCreemSubscriptionCheckoutEnabled(), "test mode must not create orders that the public webhook cannot authenticate")

	setting.CreemWebhookSecret = "creem_secret"
	require.True(t, isCreemSubscriptionCheckoutEnabled())
}

func TestCreemSignatureNeverSkipsVerificationWithEmptySecret(t *testing.T) {
	originalTestMode := setting.CreemTestMode
	t.Cleanup(func() { setting.CreemTestMode = originalTestMode })
	setting.CreemTestMode = true
	require.False(t, verifyCreemSignature(`{"event":"checkout.completed"}`, "", ""))
	require.False(t, verifyCreemSignature(`{"event":"checkout.completed"}`, "anything", ""))
}

func TestCreemTopUpCheckoutRejectsMissingWebhookSecret(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	originalAPIKey := setting.CreemApiKey
	originalProducts := setting.CreemProducts
	originalWebhookSecret := setting.CreemWebhookSecret
	t.Cleanup(func() {
		setting.CreemApiKey = originalAPIKey
		setting.CreemProducts = originalProducts
		setting.CreemWebhookSecret = originalWebhookSecret
	})

	setting.CreemApiKey = "creem_api_key"
	setting.CreemProducts = `[{"productId":"prod_123","quota":1}]`
	setting.CreemWebhookSecret = ""

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	creemAdaptor.RequestPay(ctx, &CreemPayRequest{ProductId: "prod_123", PaymentMethod: model.PaymentMethodCreem})

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), "Creem 支付暂不可用")
}

func TestWaffoWebhookEnabledRequiresTopUpAndWebhookConfig(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	originalEnabled := setting.WaffoEnabled
	originalSandbox := setting.WaffoSandbox
	originalAPIKey := setting.WaffoApiKey
	originalPrivateKey := setting.WaffoPrivateKey
	originalPublicCert := setting.WaffoPublicCert
	originalSandboxAPIKey := setting.WaffoSandboxApiKey
	originalSandboxPrivateKey := setting.WaffoSandboxPrivateKey
	originalSandboxPublicCert := setting.WaffoSandboxPublicCert
	t.Cleanup(func() {
		setting.WaffoEnabled = originalEnabled
		setting.WaffoSandbox = originalSandbox
		setting.WaffoApiKey = originalAPIKey
		setting.WaffoPrivateKey = originalPrivateKey
		setting.WaffoPublicCert = originalPublicCert
		setting.WaffoSandboxApiKey = originalSandboxAPIKey
		setting.WaffoSandboxPrivateKey = originalSandboxPrivateKey
		setting.WaffoSandboxPublicCert = originalSandboxPublicCert
	})

	setting.WaffoEnabled = true
	setting.WaffoSandbox = false
	setting.WaffoApiKey = ""
	setting.WaffoPrivateKey = "private"
	setting.WaffoPublicCert = "public"
	require.False(t, isWaffoWebhookEnabled())

	setting.WaffoApiKey = "api"
	require.True(t, isWaffoWebhookEnabled())

	setting.WaffoEnabled = false
	require.True(t, isWaffoWebhookEnabled(), "pausing new Waffo sales must not disable in-flight callbacks")

	setting.WaffoEnabled = true
	setting.WaffoSandbox = true
	setting.WaffoSandboxApiKey = ""
	setting.WaffoSandboxPrivateKey = "sandbox_private"
	setting.WaffoSandboxPublicCert = "sandbox_public"
	require.False(t, isWaffoWebhookEnabled())

	setting.WaffoSandboxApiKey = "sandbox_api"
	require.True(t, isWaffoWebhookEnabled())
}

func TestWaffoPancakeWebhookEnabledRequiresTopUpAndWebhookConfig(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	originalMerchantID := setting.WaffoPancakeMerchantID
	originalPrivateKey := setting.WaffoPancakePrivateKey
	originalProductID := setting.WaffoPancakeProductID
	originalStoreID := setting.WaffoPancakeStoreID
	t.Cleanup(func() {
		setting.WaffoPancakeMerchantID = originalMerchantID
		setting.WaffoPancakePrivateKey = originalPrivateKey
		setting.WaffoPancakeProductID = originalProductID
		setting.WaffoPancakeStoreID = originalStoreID
	})

	// Presence of all three credentials enables the gateway. Webhook public
	// keys are bundled in the SDK and there is no separate Enabled toggle —
	// clear any of the three fields to disable.
	setting.WaffoPancakeMerchantID = ""
	setting.WaffoPancakePrivateKey = "private"
	setting.WaffoPancakeProductID = "product"
	setting.WaffoPancakeStoreID = "store"
	require.False(t, isWaffoPancakeWebhookEnabled())

	setting.WaffoPancakeMerchantID = "merchant"
	require.True(t, isWaffoPancakeWebhookEnabled())

	setting.WaffoPancakeProductID = ""
	require.True(t, isWaffoPancakeWebhookEnabled(), "wallet product can be drained independently")

	setting.WaffoPancakeProductID = "product"
	setting.WaffoPancakePrivateKey = ""
	require.True(t, isWaffoPancakeWebhookEnabled(), "webhook verification uses bundled public keys")
	setting.WaffoPancakeStoreID = ""
	require.False(t, isWaffoPancakeWebhookEnabled())
}

func TestEpayWebhookEnabledIsIndependentFromCheckoutSettings(t *testing.T) {
	confirmPaymentComplianceForTest(t)
	originalPayAddress := operation_setting.PayAddress
	originalEpayID := operation_setting.EpayId
	originalEpayKey := operation_setting.EpayKey
	originalPayMethods := operation_setting.PayMethods
	t.Cleanup(func() {
		operation_setting.PayAddress = originalPayAddress
		operation_setting.EpayId = originalEpayID
		operation_setting.EpayKey = originalEpayKey
		operation_setting.PayMethods = originalPayMethods
	})

	operation_setting.PayAddress = "https://pay.example.com"
	operation_setting.EpayId = "epay_id"
	operation_setting.EpayKey = ""
	operation_setting.PayMethods = []map[string]string{{"type": "alipay"}}
	require.False(t, isEpayWebhookEnabled())

	operation_setting.EpayKey = "epay_key"
	require.True(t, isEpayWebhookEnabled())

	operation_setting.PayMethods = nil
	require.True(t, isEpayWebhookEnabled())
	require.False(t, isEpayCheckoutEnabled())

	// Revoking the sales/compliance acknowledgement drains new checkout but
	// does not strand a valid in-flight provider callback.
	paymentSetting := operation_setting.GetPaymentSetting()
	originalConfirmed := paymentSetting.ComplianceConfirmed
	paymentSetting.ComplianceConfirmed = false
	t.Cleanup(func() { paymentSetting.ComplianceConfirmed = originalConfirmed })
	require.True(t, isEpayWebhookEnabled())
	require.False(t, isEpayCheckoutEnabled())
}

func TestEpayGetWebhookRequiresExplicitCompatibilityOptIn(t *testing.T) {
	t.Setenv("EPAY_NOTIFY_GET_ENABLED", "")
	require.False(t, isEpayGetWebhookEnabled())
	t.Setenv("EPAY_NOTIFY_GET_ENABLED", "true")
	require.True(t, isEpayGetWebhookEnabled())
	t.Setenv("EPAY_NOTIFY_GET_ENABLED", "false")
	require.False(t, isEpayGetWebhookEnabled())
	t.Setenv("EPAY_NOTIFY_GET_ENABLED", "not-a-bool")
	require.False(t, isEpayGetWebhookEnabled())
}

package controller

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/thanhpk/randstr"
)

const CreemSignatureHeader = "creem-signature"

var creemAdaptor = &CreemAdaptor{}

// lookupCreemOrder preserves infrastructure errors at the webhook boundary.
// A pointer-only lookup cannot distinguish an unknown order from a temporary
// database outage; the latter must remain retryable rather than becoming a
// permanent 4xx acknowledgement.
func lookupCreemOrder(tradeNo string) (*model.SubscriptionOrder, *model.TopUp, error) {
	subscriptionOrder, subscriptionErr := model.GetSubscriptionOrderByTradeNoWithError(tradeNo)
	if subscriptionErr != nil && !errors.Is(subscriptionErr, model.ErrSubscriptionOrderNotFound) {
		return nil, nil, subscriptionErr
	}
	if errors.Is(subscriptionErr, model.ErrSubscriptionOrderNotFound) {
		subscriptionOrder = nil
	}
	topUp, topUpErr := model.GetTopUpByTradeNoWithError(tradeNo)
	if topUpErr != nil && !errors.Is(topUpErr, model.ErrTopUpNotFound) {
		return nil, nil, topUpErr
	}
	if errors.Is(topUpErr, model.ErrTopUpNotFound) {
		topUp = nil
	}
	return subscriptionOrder, topUp, nil
}

// 生成HMAC-SHA256签名
func generateCreemSignature(payload string, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(payload))
	return hex.EncodeToString(h.Sum(nil))
}

// 验证Creem webhook签名
func verifyCreemSignature(payload string, signature string, secret string) bool {
	return verifyCreemSignatureWithMode(payload, signature, secret, setting.GetCreemConfig().TestMode)
}

func verifyCreemSignatureWithMode(payload string, signature string, secret string, testMode bool) bool {
	if secret == "" {
		logger.LogWarn(context.Background(), fmt.Sprintf("Creem webhook secret 未配置 test_mode=%t signature_meta=%s body_meta=%s", testMode, common.SensitiveLogMeta(signature), common.SensitiveLogMeta(payload)))
		// Test mode changes the provider endpoint, not the trust model. Never
		// accept an unsigned callback: an attacker can still reach the test
		// webhook route and otherwise forge a payment completion.
		return false
	}

	expectedSignature := generateCreemSignature(payload, secret)
	return hmac.Equal([]byte(signature), []byte(expectedSignature))
}

type CreemPayRequest struct {
	ProductId     string `json:"product_id"`
	PaymentMethod string `json:"payment_method"`
}

type CreemProduct struct {
	ProductId string  `json:"productId"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	Currency  string  `json:"currency"`
	Quota     int64   `json:"quota"`
}

type CreemAdaptor struct {
}

func (*CreemAdaptor) RequestPay(c *gin.Context, req *CreemPayRequest) {
	creemConfig := setting.GetCreemConfig()
	if !isPaymentComplianceConfirmed() || !isCreemTopUpEnabledWithConfig(creemConfig) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Creem 支付暂不可用"})
		return
	}

	if req.PaymentMethod != model.PaymentMethodCreem {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "不支持的支付渠道"})
		return
	}

	if req.ProductId == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "请选择产品"})
		return
	}

	// 解析产品列表 from one immutable runtime snapshot.  This keeps a
	// concurrent hot reload from changing the catalogue halfway through order
	// creation.
	var products []CreemProduct
	err := common.Unmarshal([]byte(creemConfig.Products), &products)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 产品配置解析失败 user_id=%d error=%q", c.GetInt("id"), err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "产品配置错误"})
		return
	}

	// 查找对应的产品
	var selectedProduct *CreemProduct
	for i := range products {
		if products[i].ProductId == req.ProductId {
			selectedProduct = &products[i]
			break
		}
	}

	if selectedProduct == nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "产品不存在"})
		return
	}
	if err := validateCreemProductConfig(selectedProduct); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 产品配置无效 product_id=%s error=%q", req.ProductId, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "产品配置错误"})
		return
	}
	if selectedProduct.Quota <= 0 {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 产品配置无效 product_id=%s error=non_positive_quota", req.ProductId))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "产品配置错误"})
		return
	}

	id := c.GetInt("id")
	creditedQuota, quotaErr := validateCreditedQuota(decimal.NewFromInt(selectedProduct.Quota))
	if quotaErr != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": quotaErr.Error()})
		return
	}
	if err := model.ValidateTopUpQuotaCapacity(id, creditedQuota); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": err.Error()})
		return
	}

	user, err := model.GetUserById(id, false)
	if err != nil || user == nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "用户不存在"})
		return
	}

	// 生成唯一的订单引用ID
	reference := fmt.Sprintf("creem-api-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	referenceId := "ref_" + common.Sha1([]byte(reference))

	// 先创建订单记录，使用产品配置的金额和充值额度
	currency := strings.ToUpper(strings.TrimSpace(selectedProduct.Currency))
	providerAmount := stripeAmountFromMajorUnits(selectedProduct.Price, currency)
	providerAccountID := creemMerchantSnapshotWithConfig(creemConfig)
	providerEnvironment := creemProviderEnvironment(creemConfig)
	providerScopeFingerprint, scopeOK := model.ProviderPaymentScopeFingerprint(model.PaymentProviderCreem, providerAccountID, providerEnvironment)
	if !scopeOK {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 创建充值订单失败 user_id=%d reason=provider_scope_invalid", id))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付配置错误"})
		return
	}
	topUp := &model.TopUp{
		UserId:                 id,
		Amount:                 selectedProduct.Quota,
		Money:                  selectedProduct.Price,
		TradeNo:                referenceId,
		PaymentMethod:          model.PaymentMethodCreem,
		PaymentProvider:        model.PaymentProviderCreem,
		CreateTime:             time.Now().Unix(),
		Status:                 common.TopUpStatusPending,
		CreditedQuota:          creditedQuota,
		ProviderMerchantID:     providerAccountID,
		ProviderOrderName:      selectedProduct.Name,
		ProviderAmount:         providerAmount,
		ProviderProductID:      selectedProduct.ProductId,
		ProviderCurrency:       currency,
		ProviderKeyFingerprint: providerScopeFingerprint,
	}
	err = topUp.Insert()
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 创建充值订单失败 user_id=%d trade_no=%s product_id=%s error=%q", id, referenceId, selectedProduct.ProductId, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}

	// 创建支付链接，传入用户邮箱
	checkout, err := genCreemCheckoutWithConfig(c.Request.Context(), referenceId, selectedProduct, user.Email, user.Username, "topup", int64(creditedQuota), creemConfig)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 创建支付链接失败 user_id=%d trade_no=%s product_id=%s error=%q", id, referenceId, selectedProduct.ProductId, err.Error()))
		if statusErr := model.UpdatePendingTopUpStatus(referenceId, model.PaymentProviderCreem, common.TopUpStatusFailed); statusErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 充值订单失败状态补偿失败 user_id=%d trade_no=%s provider=%s error=%q", id, referenceId, model.PaymentProviderCreem, statusErr.Error()))
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	if err := model.SetTopUpProviderCheckoutID(referenceId, model.PaymentProviderCreem, checkout.Id); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 保存 Checkout ID 失败 user_id=%d trade_no=%s error=%q", id, referenceId, err.Error()))
		if statusErr := model.UpdatePendingTopUpStatus(referenceId, model.PaymentProviderCreem, common.TopUpStatusFailed); statusErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 充值订单失败状态补偿失败 user_id=%d trade_no=%s provider=%s error=%q", id, referenceId, model.PaymentProviderCreem, statusErr.Error()))
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建支付订单失败"})
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem 充值订单创建成功 user_id=%d trade_no=%s product_id=%s product_name=%q quota=%d money=%.2f", id, referenceId, selectedProduct.ProductId, selectedProduct.Name, selectedProduct.Quota, selectedProduct.Price))

	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"checkout_url": checkout.CheckoutUrl,
			"order_id":     referenceId,
		},
	})
}

func RequestCreemPay(c *gin.Context) {
	var req CreemPayRequest

	// 读取body内容用于打印，同时保留原始数据供后续使用
	bodyBytes, err := readPaymentRequestBody(c)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 支付请求读取失败 error=%q", err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "read query error"})
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem 支付请求已收到 user_id=%d body_meta=%s", c.GetInt("id"), common.SensitiveLogBody(bodyBytes)))

	// 重新设置body供后续的ShouldBindJSON使用
	c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	err = c.ShouldBindJSON(&req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	creemAdaptor.RequestPay(c, &req)
}

// 新的Creem Webhook结构体，匹配实际的webhook数据格式
type CreemWebhookEvent struct {
	Id        string `json:"id"`
	EventType string `json:"eventType"`
	CreatedAt int64  `json:"created_at"`
	Object    struct {
		Id        string `json:"id"`
		Object    string `json:"object"`
		RequestId string `json:"request_id"`
		Order     struct {
			Object      string `json:"object"`
			Id          string `json:"id"`
			Customer    string `json:"customer"`
			Product     string `json:"product"`
			Amount      int    `json:"amount"`
			Currency    string `json:"currency"`
			SubTotal    int    `json:"sub_total"`
			TaxAmount   int    `json:"tax_amount"`
			AmountDue   int    `json:"amount_due"`
			AmountPaid  int    `json:"amount_paid"`
			Status      string `json:"status"`
			Type        string `json:"type"`
			Transaction string `json:"transaction"`
			CreatedAt   string `json:"created_at"`
			UpdatedAt   string `json:"updated_at"`
			Mode        string `json:"mode"`
		} `json:"order"`
		Transaction struct {
			Id             string `json:"id"`
			Object         string `json:"object"`
			Amount         int64  `json:"amount"`
			AmountPaid     int64  `json:"amount_paid"`
			Currency       string `json:"currency"`
			Status         string `json:"status"`
			RefundedAmount int64  `json:"refunded_amount"`
			Order          string `json:"order"`
			Subscription   string `json:"subscription"`
			Mode           string `json:"mode"`
		} `json:"transaction"`
		Checkout struct {
			Id        string            `json:"id"`
			Object    string            `json:"object"`
			RequestId string            `json:"request_id"`
			Status    string            `json:"status"`
			Metadata  map[string]string `json:"metadata"`
			Mode      string            `json:"mode"`
		} `json:"checkout"`
		Product struct {
			Id                string  `json:"id"`
			Object            string  `json:"object"`
			Name              string  `json:"name"`
			Description       string  `json:"description"`
			Price             int     `json:"price"`
			Currency          string  `json:"currency"`
			BillingType       string  `json:"billing_type"`
			BillingPeriod     string  `json:"billing_period"`
			Status            string  `json:"status"`
			TaxMode           string  `json:"tax_mode"`
			TaxCategory       string  `json:"tax_category"`
			DefaultSuccessUrl *string `json:"default_success_url"`
			CreatedAt         string  `json:"created_at"`
			UpdatedAt         string  `json:"updated_at"`
			Mode              string  `json:"mode"`
		} `json:"product"`
		Units    int `json:"units"`
		Customer struct {
			Id        string `json:"id"`
			Object    string `json:"object"`
			Email     string `json:"email"`
			Name      string `json:"name"`
			Country   string `json:"country"`
			CreatedAt string `json:"created_at"`
			UpdatedAt string `json:"updated_at"`
			Mode      string `json:"mode"`
		} `json:"customer"`
		// Creem documents subscription as an expanded object, while some older
		// deliveries may omit it (or expose only an id string). Keep the raw JSON
		// so parsing remains backward compatible and settlement can persist an id
		// when one is available.
		Subscription         json.RawMessage   `json:"subscription"`
		Status               string            `json:"status"`
		RefundAmount         int64             `json:"refund_amount"`
		RefundCurrency       string            `json:"refund_currency"`
		Amount               int64             `json:"amount"`
		Currency             string            `json:"currency"`
		Reason               string            `json:"reason"`
		CurrentPeriodEndDate string            `json:"current_period_end_date"`
		CurrentPeriodEnd     int64             `json:"current_period_end"`
		Metadata             map[string]string `json:"metadata"`
		Mode                 string            `json:"mode"`
	} `json:"object"`
}

func CreemWebhook(c *gin.Context) {
	creemConfig := setting.GetCreemConfig()
	if !isCreemWebhookEnabledWithConfig(creemConfig) {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem webhook 被拒绝 reason=webhook_disabled path=%q client_ip=%s", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP()))
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	// 读取body内容用于打印，同时保留原始数据供后续使用
	bodyBytes, err := readPaymentRequestBody(c)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem webhook 读取请求体失败 path=%q client_ip=%s error=%q", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP(), err.Error()))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	// 获取签名头
	signature := c.GetHeader(CreemSignatureHeader)
	bodyMeta := common.SensitiveLogBody(bodyBytes)
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem webhook 收到请求 path=%q client_ip=%s signature_meta=%s body_meta=%s", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP(), common.SensitiveLogMeta(signature), bodyMeta))
	if signature == "" {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem webhook 缺少签名 path=%q client_ip=%s body_meta=%s", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP(), bodyMeta))
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	// 验证签名
	if !verifyCreemSignatureWithMode(string(bodyBytes), signature, creemConfig.WebhookSecret, creemConfig.TestMode) {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem webhook 验签失败 path=%q client_ip=%s signature_meta=%s body_meta=%s", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP(), common.SensitiveLogMeta(signature), bodyMeta))
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem webhook 验签成功 path=%q client_ip=%s", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP()))

	// 重新设置body供后续的ShouldBindJSON使用
	c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// 解析新格式的webhook数据
	var webhookEvent CreemWebhookEvent
	if err := c.ShouldBindJSON(&webhookEvent); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem webhook 解析失败 path=%q client_ip=%s error=%q body_meta=%s", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP(), err.Error(), bodyMeta))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem webhook 解析成功 event_type=%s event_id=%s request_id=%s order_id=%s order_status=%s", webhookEvent.EventType, webhookEvent.Id, webhookEvent.Object.RequestId, webhookEvent.Object.Order.Id, webhookEvent.Object.Order.Status))

	// 根据事件类型处理不同的webhook
	switch webhookEvent.EventType {
	case "checkout.completed":
		handleCheckoutCompletedWithConfig(c, &webhookEvent, creemConfig)
	case "refund.created", "dispute.created":
		if err := handleCreemReversalEventWithConfig(c.Request.Context(), &webhookEvent, string(bodyBytes), creemConfig); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Creem reversal 处理失败 event_id=%s error=%q", webhookEvent.Id, err.Error()))
			c.AbortWithStatus(creemSettlementHTTPStatus(err))
			return
		}
		c.Status(http.StatusOK)
	case "subscription.active", "subscription.renewed", "subscription.paid", "subscription.payment_failed", "subscription.past_due", "subscription.unpaid", "subscription.expired", "subscription.canceled", "subscription.cancelled", "subscription.scheduled_cancel", "subscription.update", "subscription.trialing", "subscription.paused":
		if err := handleCreemSubscriptionLifecycleWithConfig(c, &webhookEvent, creemConfig); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Creem lifecycle 处理失败 event_id=%s error=%q", webhookEvent.Id, err.Error()))
			// A signed lifecycle event with missing/invalid identity or a
			// transient DB failure must not be acknowledged as successful. Keep
			// malformed/contradictory signed payloads at 4xx (retrying cannot make
			// them valid), while infrastructure failures remain 5xx and retryable.
			c.AbortWithStatus(creemSettlementHTTPStatus(err))
		}
	default:
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem webhook 忽略事件 event_type=%s event_id=%s", webhookEvent.EventType, webhookEvent.Id))
		c.Status(http.StatusOK)
	}
}

func handleCreemSubscriptionLifecycle(c *gin.Context, event *CreemWebhookEvent) error {
	return handleCreemSubscriptionLifecycleWithConfig(c, event, setting.GetCreemConfig())
}

// handleCreemSubscriptionLifecycleWithConfig keeps authentication and
// lifecycle settlement on one immutable mode snapshot. A concurrent config
// reload must not make a verified test event execute under live settings (or
// vice versa).
func handleCreemSubscriptionLifecycleWithConfig(c *gin.Context, event *CreemWebhookEvent, creemConfig setting.CreemConfig) error {
	if err := validateCreemEventMode(event, creemConfig); err != nil {
		return err
	}
	if event == nil || strings.TrimSpace(event.Id) == "" || event.CreatedAt <= 0 {
		return errCreemEventInvalid
	}
	subscriptionID := creemSubscriptionID(event)
	if subscriptionID == "" {
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem lifecycle event missing subscription id event_id=%s", event.Id))
		return errCreemEventInvalid
	}
	status := "active"
	providerAccountID := creemMerchantSnapshotWithConfig(creemConfig)
	providerEnvironment := creemProviderEnvironment(creemConfig)
	if providerAccountID == "" || providerEnvironment == "" {
		return errCreemEventInvalid
	}
	options := model.SubscriptionLifecycleOptions{
		ProviderScope: &model.ProviderLifecycleScope{
			ProviderAccountID:   providerAccountID,
			ProviderEnvironment: providerEnvironment,
		},
	}
	periodEnd := creemSubscriptionPeriodEnd(event)
	renewalEvidenceRequired := false
	handled := true
	switch event.EventType {
	case "subscription.active", "subscription.renewed", "subscription.paid":
		// These are the only Creem lifecycle deliveries that can advance a
		// paid period. A future period boundary by itself is not payment proof;
		// require the immutable order/product evidence below before invoking the
		// model, otherwise a status-only callback could reset quota or extend
		// access indefinitely.
		status = "active"
		renewalEvidenceRequired = true
		// Tell the model this is a payment-backed renewal, so a newer period
		// may recover a past_due entitlement. Generic active/update events are
		// deliberately not allowed to do that.
		options.RenewalOnly = true
		options.AllowSamePeriodRecovery = true
	case "subscription.payment_failed", "subscription.past_due", "subscription.unpaid":
		status = "past_due"
	case "subscription.scheduled_cancel":
		// Creem explicitly distinguishes a scheduled cancellation from a
		// terminal cancellation. Preserve access until current_period_end_date.
		status = "active"
		options.CancellationAtPeriodEnd = true
	case "subscription.canceled", "subscription.cancelled":
		status = "cancelled"
		options.ImmediateRevoke = true
	case "subscription.expired":
		status = "expired"
		options.ImmediateRevoke = true
	case "subscription.trialing":
		// Trial status is not a paid renewal. In particular, do not pass its
		// period boundary to the model: doing so would reset a paid period's
		// quota without any payment evidence.
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem trialing lifecycle 不改变本地付费权益 event_id=%s subscription_id=%s", event.Id, subscriptionID))
		c.Status(http.StatusOK)
		return nil
	case "subscription.paused":
		status = "past_due"
	case "subscription.update":
		// The update event carries the provider's current status. Unknown
		// statuses are rejected by the model rather than guessed here.
		status = event.Object.Status
		switch strings.ToLower(strings.TrimSpace(status)) {
		case "scheduled_cancel", "scheduled-cancel", "cancel_at_period_end":
			status = "active"
			options.CancellationAtPeriodEnd = true
		case "canceled", "cancelled":
			options.ImmediateRevoke = true
		case "expired":
			options.ImmediateRevoke = true
		}
		if strings.EqualFold(strings.TrimSpace(status), "active") && !options.CancellationAtPeriodEnd && !options.ImmediateRevoke {
			// An update is a state/configuration notification, not proof that a
			// new billing period was paid. Ignore active updates entirely so they
			// cannot resurrect a past_due row or reset amount_used.
			logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem subscription.update active 不改变本地付费权益 event_id=%s subscription_id=%s", event.Id, subscriptionID))
			c.Status(http.StatusOK)
			return nil
		}
		if strings.EqualFold(strings.TrimSpace(status), "paid") || strings.EqualFold(strings.TrimSpace(status), "trialing") {
			// These statuses normalize to active in the model, but an update is
			// not a payment-success event. Do not let it recover a past_due row.
			logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem subscription.update %s 不改变本地付费权益 event_id=%s subscription_id=%s", status, event.Id, subscriptionID))
			c.Status(http.StatusOK)
			return nil
		}
		// Non-active updates (past_due/terminal) carry no renewal boundary.
		periodEnd = 0
	default:
		handled = false
	}
	if !handled {
		return errCreemEventInvalid
	}

	if renewalEvidenceRequired {
		if periodEnd <= 0 {
			logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem renewal 缺少有效 period end，保持本地权益不变 event_id=%s subscription_id=%s", event.Id, subscriptionID))
			c.Status(http.StatusOK)
			return nil
		}
		evidence, evidenceErr := creemSubscriptionLifecycleEvidence(event)
		if evidenceErr != nil {
			if errors.Is(evidenceErr, errCreemLifecycleEvidenceMissing) {
				logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem renewal 缺少金额/产品/币种证据，保持本地权益不变 event_id=%s subscription_id=%s", event.Id, subscriptionID))
				c.Status(http.StatusOK)
				return nil
			}
			return evidenceErr
		}
		if err := model.ApplySubscriptionLifecycleEventAtWithEvidenceAndOptions(model.PaymentProviderCreem, event.Id, subscriptionID, status, normalizeCreemEventTime(event.CreatedAt), periodEnd, evidence, common.GetJsonString(event), options); err != nil {
			if errors.Is(err, model.ErrSubscriptionOrderNotFound) {
				// A signed event for a subscription not managed by this instance is
				// harmless and should not be retried forever.
				c.Status(http.StatusOK)
				return nil
			}
			return err
		}
		c.Status(http.StatusOK)
		return nil
	}

	if err := model.ApplySubscriptionLifecycleEventAtWithOptions(model.PaymentProviderCreem, event.Id, subscriptionID, status, normalizeCreemEventTime(event.CreatedAt), periodEnd, common.GetJsonString(event), options); err != nil {
		if errors.Is(err, model.ErrSubscriptionOrderNotFound) {
			// A signed event for a subscription not managed by this instance is
			// harmless and should not be retried forever.
			c.Status(http.StatusOK)
			return nil
		}
		return err
	}
	c.Status(http.StatusOK)
	return nil
}

// creemSubscriptionPeriodEnd returns a canonical Unix-seconds boundary from
// the expanded subscription object (or the enclosing object for older
// payloads). A malformed optional boundary is treated as absent; the model
// fails closed for a scheduled-cancel event that has no safe boundary.
func creemSubscriptionPeriodEnd(event *CreemWebhookEvent) int64 {
	if event == nil {
		return 0
	}
	if value := parseCreemTimeValue(event.Object.CurrentPeriodEndDate); value > 0 {
		return value
	}
	if event.Object.CurrentPeriodEnd > 0 {
		return normalizeCreemEventTime(event.Object.CurrentPeriodEnd)
	}
	if len(event.Object.Subscription) == 0 {
		return 0
	}
	var object struct {
		CurrentPeriodEndDate string `json:"current_period_end_date"`
		CurrentPeriodEnd     int64  `json:"current_period_end"`
	}
	if common.Unmarshal(event.Object.Subscription, &object) != nil {
		return 0
	}
	if value := parseCreemTimeValue(object.CurrentPeriodEndDate); value > 0 {
		return value
	}
	if object.CurrentPeriodEnd > 0 {
		return normalizeCreemEventTime(object.CurrentPeriodEnd)
	}
	return 0
}

func parseCreemTimeValue(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed.Unix()
	}
	if parsed, err := strconv.ParseInt(value, 10, 64); err == nil && parsed > 0 {
		return normalizeCreemEventTime(parsed)
	}
	return 0
}

func normalizeCreemEventTime(value int64) int64 {
	// Creem has emitted both Unix seconds and Unix milliseconds in different
	// webhook API versions. Store one canonical seconds value for ordering.
	if value >= 1_000_000_000_000 {
		return value / 1000
	}
	return value
}

// 处理支付完成事件
// handleCheckoutCompleted keeps the historical helper signature for tests and
// integrations that call it directly. HTTP webhook handling uses the explicit
// snapshot variant below so authentication and settlement share one config.
func handleCheckoutCompleted(c *gin.Context, event *CreemWebhookEvent) {
	handleCheckoutCompletedWithConfig(c, event, setting.GetCreemConfig())
}

func handleCheckoutCompletedWithConfig(c *gin.Context, event *CreemWebhookEvent, creemConfig setting.CreemConfig) {
	if event == nil {
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	referenceID := strings.TrimSpace(event.Object.RequestId)
	if referenceID == "" {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem webhook 缺少 request_id event_id=%s", event.Id))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}
	if strings.ToLower(strings.TrimSpace(event.Object.Order.Status)) != "paid" {
		// A signed checkout.completed delivery can still contain a provider-side
		// non-paid state while payment is being finalized. Acknowledge it without
		// granting anything; only a later paid event may settle the order.
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem 订单状态未支付，忽略处理 request_id=%s order_status=%s", referenceID, event.Object.Order.Status))
		c.Status(http.StatusOK)
		return
	}

	// Resolve the local order first. The order type is authoritative for the
	// settlement route; provider-controlled order.type can only be checked
	// against that type, never used to choose a different grant path.
	subscriptionOrder, topUp, lookupErr := lookupCreemOrder(referenceID)
	if lookupErr != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem webhook 查询本地订单失败 trade_no=%s event_id=%s error=%q", referenceID, event.Id, lookupErr.Error()))
		// A signed payment must be retried when the local database is
		// temporarily unavailable; treating a driver error as "not found" and
		// returning 4xx would acknowledge/drop a payment without settlement.
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	if subscriptionOrder != nil && topUp != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem webhook 同一 trade_no 同时存在充值和订阅订单 trade_no=%s event_id=%s", referenceID, event.Id))
		c.AbortWithStatus(http.StatusConflict)
		return
	}
	kind := ""
	expectedAmount := ""
	switch {
	case subscriptionOrder != nil:
		kind = "subscription"
		expectedAmount = subscriptionOrder.ProviderAmount
	case topUp != nil:
		kind = "topup"
		expectedAmount = topUp.ProviderAmount
	default:
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem webhook 本地订单不存在 trade_no=%s event_id=%s", referenceID, event.Id))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	settlement, err := buildCreemProviderSettlementWithConfig(event, referenceID, kind, expectedAmount, creemConfig)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem webhook 与订单快照不匹配 trade_no=%s kind=%s event_id=%s error=%q", referenceID, kind, event.Id, err.Error()))
		c.AbortWithStatus(creemSettlementHTTPStatus(err))
		return
	}

	LockOrder(referenceID)
	defer UnlockOrder(referenceID)
	if kind == "subscription" {
		if strings.TrimSpace(settlement.ProviderSubscriptionID) == "" {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem 订阅 Checkout 缺少 provider subscription id，已完成首次结算但未启用续费同步 trade_no=%s event_id=%s", referenceID, event.Id))
		}
		outcome, err := model.CompleteSubscriptionOrderVerifiedWithOutcome(settlement)
		if err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 订阅订单结算失败 trade_no=%s event_id=%s error=%q", referenceID, event.Id, err.Error()))
			c.AbortWithStatus(creemSettlementHTTPStatus(err))
			return
		}
		if outcome == model.EpaySettlementPaidUncredited {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem 订阅已付款但未开通，订单进入 paid_uncredited trade_no=%s event_id=%s", referenceID, event.Id))
			c.Status(http.StatusOK)
			return
		}
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem 订阅订单处理成功 trade_no=%s provider_trade_no=%s event_id=%s", referenceID, settlement.ProviderTradeNo, settlement.ProviderEventID))
		c.Status(http.StatusOK)
		return
	}

	result, err := model.SettleTopUpProvider(settlement, c.ClientIP(), nil)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Creem 充值结算失败 trade_no=%s event_id=%s error=%q", referenceID, event.Id, err.Error()))
		c.AbortWithStatus(creemSettlementHTTPStatus(err))
		return
	}
	if result.PaidUncredited {
		// Payment evidence is committed, but the wallet mutation could not be
		// completed (for example because the wallet ceiling is full). Keep the
		// provider ACK idempotent while making the operator-visible state
		// explicit; logging this as an ordinary success hides money owed to the
		// customer.
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Creem 充值已付款但额度未入账，订单进入 paid_uncredited trade_no=%s provider_trade_no=%s event_id=%s quota=%d already_completed=%t", referenceID, settlement.ProviderTradeNo, settlement.ProviderEventID, result.CreditedQuota, result.AlreadyCompleted))
	} else {
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Creem 充值处理成功 trade_no=%s provider_trade_no=%s event_id=%s quota=%d already_completed=%t", referenceID, settlement.ProviderTradeNo, settlement.ProviderEventID, result.CreditedQuota, result.AlreadyCompleted))
	}
	c.Status(http.StatusOK)
}

func creemSettlementHTTPStatus(err error) int {
	switch {
	case errors.Is(err, errCreemEventInvalid), errors.Is(err, errCreemEventMismatch),
		errors.Is(err, model.ErrProviderSettlementInvalid), errors.Is(err, model.ErrProviderSnapshotMissing),
		errors.Is(err, model.ErrProviderSnapshotMismatch), errors.Is(err, model.ErrProviderEventConflict),
		errors.Is(err, model.ErrProviderRefundInvalid), errors.Is(err, model.ErrProviderRefundConflict),
		errors.Is(err, model.ErrProviderPaymentBindingInvalid), errors.Is(err, model.ErrProviderPaymentBindingConflict), errors.Is(err, model.ErrProviderPaymentBindingNotFound),
		errors.Is(err, model.ErrTopUpNotFound), errors.Is(err, model.ErrTopUpStatusInvalid),
		errors.Is(err, model.ErrInvalidTopUpQuota), errors.Is(err, model.ErrTopUpQuotaLimitExceeded), errors.Is(err, model.ErrWalletQuotaLimitExceeded),
		errors.Is(err, model.ErrSubscriptionOrderNotFound), errors.Is(err, model.ErrSubscriptionOrderStatusInvalid), errors.Is(err, model.ErrSubscriptionOrderConflict),
		errors.Is(err, model.ErrSubscriptionPurchaseLimitExceeded),
		errors.Is(err, model.ErrSubscriptionEntitlementSnapshotMissing), errors.Is(err, model.ErrSubscriptionEntitlementSnapshotInvalid),
		errors.Is(err, model.ErrSubscriptionPreConsumeConflict),
		errors.Is(err, model.ErrPaymentMethodMismatch):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

type CreemCheckoutRequest struct {
	ProductId string `json:"product_id"`
	RequestId string `json:"request_id"`
	Customer  struct {
		Email string `json:"email"`
	} `json:"customer"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type CreemCheckoutResponse struct {
	CheckoutUrl string `json:"checkout_url"`
	Id          string `json:"id"`
}

func genCreemLink(ctx context.Context, referenceId string, product *CreemProduct, email string, username string) (string, error) {
	checkout, err := genCreemCheckout(ctx, referenceId, product, email, username, "topup", product.Quota)
	if err != nil {
		return "", err
	}
	return checkout.CheckoutUrl, nil
}

func genCreemCheckout(ctx context.Context, referenceId string, product *CreemProduct, email string, username string, kind string, creditedQuota int64) (*CreemCheckoutResponse, error) {
	return genCreemCheckoutWithConfig(ctx, referenceId, product, email, username, kind, creditedQuota, setting.GetCreemConfig())
}

func genCreemCheckoutWithConfig(ctx context.Context, referenceId string, product *CreemProduct, email string, username string, kind string, creditedQuota int64, creemConfig setting.CreemConfig) (*CreemCheckoutResponse, error) {
	if creemConfig.ApiKey == "" {
		return nil, fmt.Errorf("未配置Creem API密钥")
	}
	if err := validateCreemProductConfig(product); err != nil {
		return nil, err
	}
	if kind == "topup" && creditedQuota <= 0 {
		return nil, fmt.Errorf("invalid Creem top-up quota")
	}

	// 根据测试模式选择 API 端点
	apiUrl := "https://api.creem.io/v1/checkouts"
	if creemConfig.TestMode {
		apiUrl = "https://test-api.creem.io/v1/checkouts"
		logger.LogInfo(ctx, fmt.Sprintf("Creem 使用测试环境 api_url=%s", apiUrl))
	}

	// 构建请求数据，确保包含用户邮箱
	requestData := CreemCheckoutRequest{
		ProductId: product.ProductId,
		RequestId: referenceId, // 这个作为订单ID传递给Creem
		Customer: struct {
			Email string `json:"email"`
		}{
			Email: email, // 用户邮箱会在支付页面预填充
		},
		Metadata: map[string]string{
			"username":     username,
			"reference_id": referenceId,
			"product_name": product.Name,
			"product_id":   product.ProductId,
			"order_kind":   kind,
			"currency":     strings.ToUpper(strings.TrimSpace(product.Currency)),
			"amount":       stripeAmountFromMajorUnits(product.Price, product.Currency),
			"quota":        fmt.Sprintf("%d", creditedQuota),
		},
	}

	// 序列化请求数据
	jsonData, err := common.Marshal(requestData)
	if err != nil {
		return nil, fmt.Errorf("序列化请求数据失败: %v", err)
	}

	// 创建 HTTP 请求
	req, err := http.NewRequest("POST", apiUrl, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("创建HTTP请求失败: %v", err)
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", creemConfig.ApiKey)

	logger.LogInfo(ctx, fmt.Sprintf("Creem 支付请求已发送 api_url=%s product_id=%s email=%s trade_no=%s", common.SanitizeRequestURIForLog(apiUrl), product.ProductId, common.MaskEmail(email), referenceId))

	// 发送请求
	client, err := service.GetHttpClientWithProxy("")
	if err != nil {
		return nil, fmt.Errorf("创建HTTP客户端失败: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送HTTP请求失败: %v", err)
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := service.ReadProviderResponseBody(resp, service.DefaultProviderResponseBodyLimitBytes)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %v", err)
	}

	logger.LogInfo(ctx, fmt.Sprintf("Creem API 响应已收到 trade_no=%s status_code=%d body_meta=%s", referenceId, resp.StatusCode, common.SensitiveLogBody(body)))

	// 检查响应状态
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("Creem API http status %d ", resp.StatusCode)
	}
	// 解析响应
	var checkoutResp CreemCheckoutResponse
	err = common.Unmarshal(body, &checkoutResp)
	if err != nil {
		return nil, fmt.Errorf("解析响应失败: %v", err)
	}

	if checkoutResp.CheckoutUrl == "" {
		return nil, fmt.Errorf("Creem API resp no checkout url ")
	}
	if strings.TrimSpace(checkoutResp.Id) == "" {
		return nil, fmt.Errorf("Creem API response missing checkout id")
	}

	logger.LogInfo(ctx, fmt.Sprintf("Creem 支付链接创建成功 trade_no=%s response_id=%s checkout_url_meta=%s", referenceId, checkoutResp.Id, common.SensitiveLogMeta(checkoutResp.CheckoutUrl)))
	return &checkoutResp, nil
}

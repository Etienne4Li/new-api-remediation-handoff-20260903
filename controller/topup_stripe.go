package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/checkout/session"
	"github.com/stripe/stripe-go/v81/webhook"
	"github.com/thanhpk/randstr"
)

var stripeAdaptor = &StripeAdaptor{}

// StripePayRequest represents a payment request for Stripe checkout.
type StripePayRequest struct {
	// Amount is the quantity of units to purchase.
	Amount int64 `json:"amount"`
	// PaymentMethod specifies the payment method (e.g., "stripe").
	PaymentMethod string `json:"payment_method"`
	// SuccessURL is the optional custom URL to redirect after successful payment.
	// If empty, defaults to the server's console log page.
	SuccessURL string `json:"success_url,omitempty"`
	// CancelURL is the optional custom URL to redirect when payment is canceled.
	// If empty, defaults to the server's console topup page.
	CancelURL string `json:"cancel_url,omitempty"`
}

type StripeAdaptor struct {
}

// stripeInvoiceLine is the small, deliberately-typed subset of an invoice
// line that is needed to bind a lifecycle event to the immutable local plan
// snapshot.  Keep this as a named type so the parser and evidence validator
// cannot silently drift apart (anonymous struct types with pointer-vs-value
// fields are not assignment-compatible in Go).
type stripeInvoiceLine struct {
	Price    json.RawMessage `json:"price"`
	Quantity *int64          `json:"quantity"`
	// Amount is the signed line total in the invoice currency.  It must be
	// present and equal to unit_amount*quantity for the one-unit recurring
	// checkout this application creates.
	Amount *int64 `json:"amount"`
	Period struct {
		End *int64 `json:"end"`
	} `json:"period"`
}

func (*StripeAdaptor) RequestAmount(c *gin.Context, req *StripePayRequest) {
	stripeConfig := setting.GetStripeConfig()
	paymentSetting := operation_setting.GetPaymentSettingSnapshot()
	generalSetting := operation_setting.GetGeneralSettingSnapshot()
	if req.Amount < getStripeMinTopupWithConfig(stripeConfig, generalSetting) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", getStripeMinTopupWithConfig(stripeConfig, generalSetting))})
		return
	}
	if req.Amount > 10000 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值数量不能大于 10000"})
		return
	}
	id := c.GetInt("id")
	group, err := model.GetUserGroup(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}
	if rejectInvalidCreditedQuota(c, id, getStripeCreditedQuota(req.Amount, group)) {
		return
	}
	payMoney := getStripePayMoneyWithConfig(float64(req.Amount), group, stripeConfig, paymentSetting, generalSetting)
	if payMoney <= 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": strconv.FormatFloat(payMoney, 'f', 2, 64)})
}

func (*StripeAdaptor) RequestPay(c *gin.Context, req *StripePayRequest) {
	stripeConfig := setting.GetStripeConfig()
	paymentSetting := operation_setting.GetPaymentSettingSnapshot()
	generalSetting := operation_setting.GetGeneralSettingSnapshot()
	if req.PaymentMethod != model.PaymentMethodStripe {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "不支持的支付渠道"})
		return
	}
	if req.Amount < getStripeMinTopupWithConfig(stripeConfig, generalSetting) {
		c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("充值数量不能小于 %d", getStripeMinTopupWithConfig(stripeConfig, generalSetting)), "data": 10})
		return
	}
	if req.Amount > 10000 {
		c.JSON(http.StatusOK, gin.H{"message": "充值数量不能大于 10000", "data": 10})
		return
	}

	if req.SuccessURL != "" && common.ValidateRedirectURL(req.SuccessURL) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "支付成功重定向URL不在可信任域名列表中", "data": ""})
		return
	}

	if req.CancelURL != "" && common.ValidateRedirectURL(req.CancelURL) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "支付取消重定向URL不在可信任域名列表中", "data": ""})
		return
	}

	id := c.GetInt("id")
	user, err := model.GetUserById(id, false)
	if err != nil || user == nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "用户不存在"})
		return
	}
	chargedMoney := GetChargedAmount(float64(req.Amount), *user)
	creditedQuota, quotaErr := validateCreditedQuota(getStripeCreditedQuota(req.Amount, user.Group))
	if quotaErr != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": quotaErr.Error()})
		return
	}
	if err := model.ValidateTopUpQuotaCapacity(id, creditedQuota); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": err.Error()})
		return
	}

	reference := fmt.Sprintf("new-api-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	referenceId := "ref_" + common.Sha1([]byte(reference))
	currency := getStripeCurrencyWithConfig(stripeConfig)
	providerAmount := stripeAmountFromMajorUnits(getStripePayMoneyWithConfig(float64(req.Amount), user.Group, stripeConfig, paymentSetting, generalSetting), currency)
	providerAccountID := stripeStandardAccountScope(stripeConfig)
	live, liveErr := stripeLiveModeWithConfig(stripeConfig)
	providerEnvironment := stripeProviderEnvironment(live)
	providerScopeFingerprint := stripeProviderScopeFingerprint(providerAccountID, providerEnvironment)
	if liveErr != nil || providerAccountID == "" || providerScopeFingerprint == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Stripe 未配置或密钥无效"})
		return
	}
	topUp := &model.TopUp{
		UserId:                 id,
		Amount:                 req.Amount,
		Money:                  chargedMoney,
		TradeNo:                referenceId,
		PaymentMethod:          model.PaymentMethodStripe,
		PaymentProvider:        model.PaymentProviderStripe,
		CreateTime:             time.Now().Unix(),
		Status:                 common.TopUpStatusPending,
		CreditedQuota:          creditedQuota,
		ProviderMerchantID:     providerAccountID,
		ProviderOrderName:      "New API credits",
		ProviderAmount:         providerAmount,
		ProviderProductID:      strings.TrimSpace(stripeConfig.PriceID),
		ProviderCurrency:       currency,
		ProviderKeyFingerprint: providerScopeFingerprint,
	}
	err = topUp.Insert()
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe 创建充值订单失败 user_id=%d trade_no=%s amount=%d error=%q", id, referenceId, req.Amount, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	metadata := stripeMetadata(referenceId, "topup", stripeConfig.PriceID, topUp.ProviderOrderName, providerAccountID, creditedQuota)
	metadata["new_api_currency"] = currency
	metadata["new_api_provider_amount"] = providerAmount
	checkout, err := genStripeCheckoutSessionWithConfig(referenceId, user.StripeCustomer, user.Email, req.Amount, req.SuccessURL, req.CancelURL, metadata, stripeConfig)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe 创建 Checkout Session 失败 user_id=%d trade_no=%s amount=%d error=%q", id, referenceId, req.Amount, err.Error()))
		if statusErr := model.UpdatePendingTopUpStatus(referenceId, model.PaymentProviderStripe, common.TopUpStatusFailed); statusErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe 充值订单失败状态补偿失败 user_id=%d trade_no=%s provider=%s error=%q", id, referenceId, model.PaymentProviderStripe, statusErr.Error()))
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	if err := model.SetTopUpProviderCheckoutID(referenceId, model.PaymentProviderStripe, checkout.ID); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe 保存 Checkout Session 失败 user_id=%d trade_no=%s error=%q", id, referenceId, err.Error()))
		if statusErr := model.UpdatePendingTopUpStatus(referenceId, model.PaymentProviderStripe, common.TopUpStatusFailed); statusErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe 充值订单失败状态补偿失败 user_id=%d trade_no=%s provider=%s error=%q", id, referenceId, model.PaymentProviderStripe, statusErr.Error()))
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建支付订单失败"})
		return
	}
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Stripe 充值订单创建成功 user_id=%d trade_no=%s amount=%d money=%.2f", id, referenceId, req.Amount, chargedMoney))
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"pay_link": checkout.URL,
		},
	})
}

func RequestStripeAmount(c *gin.Context) {
	var req StripePayRequest
	err := c.ShouldBindJSON(&req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	stripeAdaptor.RequestAmount(c, &req)
}

func RequestStripePay(c *gin.Context) {
	var req StripePayRequest
	err := c.ShouldBindJSON(&req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	stripeAdaptor.RequestPay(c, &req)
}

func StripeWebhook(c *gin.Context) {
	ctx := c.Request.Context()
	stripeConfig := setting.GetStripeConfig()
	if strings.TrimSpace(stripeConfig.WebhookSecret) == "" {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe webhook 被拒绝 reason=webhook_disabled path=%q client_ip=%s", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP()))
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	payload, err := readPaymentRequestBody(c)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("Stripe webhook 读取请求体失败 path=%q client_ip=%s error=%q", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP(), err.Error()))
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}

	signature := c.GetHeader("Stripe-Signature")
	logger.LogInfo(ctx, fmt.Sprintf("Stripe webhook 收到请求 path=%q client_ip=%s signature_meta=%s body_meta=%s", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP(), common.SensitiveLogMeta(signature), common.SensitiveLogBody(payload)))
	event, err := webhook.ConstructEventWithOptions(payload, signature, stripeConfig.WebhookSecret, webhook.ConstructEventOptions{
		IgnoreAPIVersionMismatch: true,
	})

	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe webhook 验签失败 path=%q client_ip=%s error=%q", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP(), err.Error()))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	callerIp := c.ClientIP()
	logger.LogInfo(ctx, fmt.Sprintf("Stripe webhook 验签成功 event_type=%s client_ip=%s path=%q", string(event.Type), callerIp, common.SanitizeRequestURIForLog(c.Request.RequestURI)))
	var handlerErr error
	switch event.Type {
	case stripe.EventTypeCheckoutSessionCompleted:
		handlerErr = sessionCompleted(ctx, event, callerIp)
	case stripe.EventTypeCheckoutSessionExpired:
		handlerErr = sessionExpired(ctx, event)
	case stripe.EventTypeCheckoutSessionAsyncPaymentSucceeded:
		handlerErr = sessionAsyncPaymentSucceeded(ctx, event, callerIp)
	case stripe.EventTypeCheckoutSessionAsyncPaymentFailed:
		handlerErr = sessionAsyncPaymentFailed(ctx, event, callerIp)
	case stripe.EventTypeRefundCreated,
		stripe.EventTypeRefundUpdated,
		stripe.EventTypeRefundFailed,
		stripe.EventTypeChargeRefunded,
		stripe.EventTypeChargeRefundUpdated,
		stripe.EventTypeChargeDisputeFundsWithdrawn,
		stripe.EventTypeChargeDisputeFundsReinstated:
		handlerErr = handleStripeReversalEventWithConfig(ctx, event, string(payload), stripeConfig)
	case "invoice.payment_succeeded", "invoice.payment_failed", "customer.subscription.updated", "customer.subscription.deleted":
		handlerErr = handleStripeSubscriptionLifecycle(ctx, event, stripeConfig)
	default:
		logger.LogInfo(ctx, fmt.Sprintf("Stripe webhook 忽略事件 event_type=%s client_ip=%s", string(event.Type), callerIp))
	}
	if handlerErr != nil {
		logger.LogError(ctx, fmt.Sprintf("Stripe webhook 结算失败 event_type=%s client_ip=%s error=%q", string(event.Type), callerIp, handlerErr.Error()))
		c.AbortWithStatus(stripeSettlementHTTPStatus(handlerErr))
		return
	}

	c.Status(http.StatusOK)
}

func handleStripeSubscriptionLifecycle(ctx context.Context, event stripe.Event, stripeConfig setting.StripeConfig) error {
	subscriptionID, status, periodEnd, evidence, options, err := parseStripeSubscriptionLifecyclePayloadWithDetailsAndConfig(event, stripeConfig)
	if err != nil {
		return err
	}
	if subscriptionID == "" {
		logger.LogInfo(ctx, fmt.Sprintf("Stripe lifecycle event missing subscription id event_id=%s", event.ID))
		return errStripeCheckoutInvalid
	}
	if event.Type == "customer.subscription.updated" &&
		strings.EqualFold(strings.TrimSpace(status), "active") &&
		!options.CancellationAtPeriodEnd && !options.ImmediateRevoke {
		// A subscription.updated event is a configuration/state notification,
		// not proof that a new invoice was paid. In particular, its period end can
		// be newer while the invoice is still unpaid; forwarding it would grant a
		// free renewal or resurrect past_due access.
		logger.LogInfo(ctx, fmt.Sprintf("Stripe subscription.updated active 不改变本地付费权益 event_id=%s subscription_id=%s", event.ID, subscriptionID))
		return nil
	}
	if err := model.ApplySubscriptionLifecycleEventAtWithEvidenceAndOptions(model.PaymentProviderStripe, event.ID, subscriptionID, status, event.Created, periodEnd, evidence, string(event.Data.Raw), options); err != nil {
		if errors.Is(err, model.ErrSubscriptionOrderNotFound) {
			logger.LogInfo(ctx, fmt.Sprintf("Stripe lifecycle event has no local subscription event_id=%s subscription_id=%s", event.ID, subscriptionID))
			return nil
		}
		return err
	}
	return nil
}

func parseStripeSubscriptionLifecyclePayload(event stripe.Event, stripeConfig setting.StripeConfig) (subscriptionID string, status string, periodEnd int64, err error) {
	subscriptionID, status, periodEnd, _, err = parseStripeSubscriptionLifecyclePayloadWithEvidence(event, stripeConfig)
	return subscriptionID, status, periodEnd, err
}

// parseStripeSubscriptionLifecyclePayloadWithEvidence extracts the immutable
// recurring price evidence carried by Stripe invoices. Invoice IDs and
// subscription IDs are different provider objects; an invoice event must have
// an explicit subscription field and is never allowed to fall back to the
// invoice's own id. The evidence is later compared with the local checkout
// snapshot inside the settlement transaction.
func parseStripeSubscriptionLifecyclePayloadWithEvidence(event stripe.Event, stripeConfig setting.StripeConfig) (subscriptionID string, status string, periodEnd int64, evidence *model.SubscriptionLifecycleEvidence, err error) {
	subscriptionID, status, periodEnd, evidence, _, err = parseStripeSubscriptionLifecyclePayloadWithDetails(event, stripeConfig)
	return subscriptionID, status, periodEnd, evidence, err
}

// stripeSubscriptionLifecycleDetails is kept separate from the historical
// parser return values so callers that only need status/period information do
// not have to know about cancellation intent. Cancellation metadata is
// security-sensitive: a future period boundary must never be inferred merely
// from a generic canceled status.
func parseStripeSubscriptionLifecyclePayloadWithDetails(event stripe.Event, stripeConfig setting.StripeConfig) (subscriptionID string, status string, periodEnd int64, evidence *model.SubscriptionLifecycleEvidence, options model.SubscriptionLifecycleOptions, err error) {
	return parseStripeSubscriptionLifecyclePayloadWithDetailsAndConfig(event, stripeConfig)
}

func stripeSubscriptionLifecycleScope(event stripe.Event, stripeConfig setting.StripeConfig) (*model.ProviderLifecycleScope, error) {
	live, err := stripeLiveModeWithConfig(stripeConfig)
	if err != nil || event.Livemode != live {
		// A signed event from Stripe's test account must never mutate a live
		// subscription (and vice versa). Derive the persisted environment from the
		// verified configuration snapshot, using event.Livemode only as a check.
		return nil, errStripeCheckoutMode
	}

	eventAccountID := strings.TrimSpace(event.Account)
	if event.Account != "" && eventAccountID == "" {
		return nil, errStripeCheckoutInvalid
	}
	providerAccountID := eventAccountID
	if eventAccountID != "" {
		// Stripe Connect event.account values are account object IDs, never the
		// credential namespace used for standard-account fallback.
		if !strings.HasPrefix(eventAccountID, "acct_") {
			return nil, errStripeCheckoutInvalid
		}
	} else {
		providerAccountID = stripeStandardAccountScope(stripeConfig)
		validStandardScope := strings.HasPrefix(providerAccountID, "acct_") || isStripeCredentialAccountScope(providerAccountID)
		if providerAccountID == "" || !validStandardScope {
			return nil, errStripeCheckoutInvalid
		}
	}
	providerEnvironment := stripeProviderEnvironment(live)
	if stripeProviderScopeFingerprint(providerAccountID, providerEnvironment) == "" {
		return nil, errStripeCheckoutInvalid
	}
	return &model.ProviderLifecycleScope{
		ProviderAccountID:   providerAccountID,
		ProviderEnvironment: providerEnvironment,
	}, nil
}

func parseStripeSubscriptionLifecyclePayloadWithDetailsAndConfig(event stripe.Event, stripeConfig setting.StripeConfig) (subscriptionID string, status string, periodEnd int64, evidence *model.SubscriptionLifecycleEvidence, options model.SubscriptionLifecycleOptions, err error) {
	if event.Data == nil || len(event.Data.Raw) == 0 || strings.TrimSpace(event.ID) == "" || event.Created <= 0 {
		return "", "", 0, nil, options, errStripeCheckoutInvalid
	}
	providerScope, err := stripeSubscriptionLifecycleScope(event, stripeConfig)
	if err != nil {
		return "", "", 0, nil, options, err
	}
	options.ProviderScope = providerScope
	var payload struct {
		ID           string          `json:"id"`
		Subscription json.RawMessage `json:"subscription"`
		Status       string          `json:"status"`
		AmountDue    int64           `json:"amount_due"`
		AmountPaid   int64           `json:"amount_paid"`
		Currency     string          `json:"currency"`
		// Keep optional provider timestamps as pointers so an explicitly
		// negative value cannot be confused with an omitted field and silently
		// replaced by a locally-derived fallback.
		CurrentPeriodEnd  *int64 `json:"current_period_end"`
		CancelAtPeriodEnd bool   `json:"cancel_at_period_end"`
		CancelAt          *int64 `json:"cancel_at"`
		CanceledAt        *int64 `json:"canceled_at"`
		EndedAt           *int64 `json:"ended_at"`
		BillingReason     string `json:"billing_reason"`
		Lines             struct {
			Data []stripeInvoiceLine `json:"data"`
		} `json:"lines"`
	}
	if err := common.Unmarshal(event.Data.Raw, &payload); err != nil {
		// The event has already passed Stripe signature verification, but a
		// payload that does not match the expected lifecycle schema is still a
		// permanent, non-actionable delivery. Wrap the decoder error in the
		// Stripe validation sentinel so the webhook returns 4xx instead of
		// retrying the same malformed event as an infrastructure failure.
		return "", "", 0, nil, options, fmt.Errorf("%w: decode lifecycle payload: %v", errStripeCheckoutInvalid, err)
	}
	subscriptionID = stripeSubscriptionIDFromRaw(payload.Subscription)
	isInvoice := event.Type == "invoice.payment_succeeded" || event.Type == "invoice.payment_failed"
	if subscriptionID == "" && !isInvoice {
		// customer.subscription.* payloads use the enclosing object's id.
		subscriptionID = strings.TrimSpace(payload.ID)
	}
	if subscriptionID == "" {
		return "", "", 0, nil, options, errStripeCheckoutInvalid
	}
	if !strings.HasPrefix(subscriptionID, "sub_") {
		return "", "", 0, nil, options, errStripeCheckoutInvalid
	}
	providerAccountID := providerScope.ProviderAccountID
	providerEnvironment := providerScope.ProviderEnvironment
	if payload.CurrentPeriodEnd != nil && *payload.CurrentPeriodEnd < 0 {
		return "", "", 0, nil, options, errStripeCheckoutInvalid
	}
	currentPeriodEnd := int64(0)
	if payload.CurrentPeriodEnd != nil {
		currentPeriodEnd = *payload.CurrentPeriodEnd
	}
	for _, line := range payload.Lines.Data {
		if line.Period.End != nil && *line.Period.End < 0 {
			return "", "", 0, nil, options, errStripeCheckoutInvalid
		}
	}
	if currentPeriodEnd <= 0 {
		for _, line := range payload.Lines.Data {
			if line.Period.End != nil && *line.Period.End > currentPeriodEnd {
				currentPeriodEnd = *line.Period.End
			}
		}
	}
	if payload.CancelAt != nil && *payload.CancelAt < 0 {
		return "", "", 0, nil, options, errStripeCheckoutInvalid
	}
	if payload.CanceledAt != nil && *payload.CanceledAt < 0 {
		return "", "", 0, nil, options, errStripeCheckoutInvalid
	}
	if payload.EndedAt != nil && *payload.EndedAt < 0 {
		return "", "", 0, nil, options, errStripeCheckoutInvalid
	}
	status = payload.Status
	switch event.Type {
	case "invoice.payment_succeeded":
		status = "active"
		// Invoice success is the only Stripe lifecycle signal that carries
		// independently validated amount/product evidence. Mark it explicitly so
		// the model may recover a same-period past_due row without resetting its
		// already-consumed quota; a new period still requires a strict boundary
		// advance.
		options.RenewalOnly = true
		options.AllowSamePeriodRecovery = true
	case "invoice.payment_failed":
		status = "past_due"
	case "customer.subscription.deleted":
		status = "cancelled"
		// A deleted subscription is a terminal provider object. Stripe may
		// still include the old current_period_end in the tombstone; that
		// value must not defer a revoke.
		options.ImmediateRevoke = true
	case "customer.subscription.updated":
		// Stripe keeps a subscription active while cancel_at_period_end is
		// true. Some API versions instead expose status=canceled together
		// with a future cancel_at timestamp; both forms represent a
		// period-end cancellation rather than an immediate revoke.
		cancelAt := int64(0)
		if payload.CancelAt != nil {
			cancelAt = *payload.CancelAt
		}
		if payload.CancelAtPeriodEnd || cancelAt > event.Created {
			options.CancellationAtPeriodEnd = true
			if cancelAt > 0 {
				periodEnd = cancelAt
			}
		} else if cancelAt > 0 && cancelAt <= event.Created &&
			(strings.EqualFold(strings.TrimSpace(payload.Status), "canceled") || strings.EqualFold(strings.TrimSpace(payload.Status), "cancelled")) {
			options.ImmediateRevoke = true
		}
	}
	if strings.TrimSpace(status) == "" {
		return "", "", 0, nil, options, errStripeCheckoutInvalid
	}
	if isInvoice {
		evidence, err = stripeInvoiceLifecycleEvidence(event, payload.Currency, payload.Lines.Data, payload.AmountDue, payload.AmountPaid)
		if err != nil {
			return "", "", 0, nil, options, err
		}
		if event.Type == "invoice.payment_succeeded" {
			paymentObjects, bindingErr := stripeInvoicePaymentObjects(event)
			if bindingErr != nil {
				return "", "", 0, nil, options, bindingErr
			}
			options.PaymentBinding = &model.ProviderPaymentBindingScope{
				ProviderAccountID:   providerAccountID,
				ProviderEnvironment: providerEnvironment,
				Objects:             paymentObjects,
			}
		}
	}
	if currentPeriodEnd > 0 && periodEnd == 0 {
		periodEnd = currentPeriodEnd
	}
	// ended_at/canceled_at are only a fallback signal when Stripe has already
	// materialized a terminal object. Do not turn a normal active update into
	// an immediate revoke solely because canceled_at is populated for a
	// scheduled cancellation.
	if event.Type == "customer.subscription.updated" && !options.CancellationAtPeriodEnd &&
		(strings.EqualFold(strings.TrimSpace(status), "canceled") || strings.EqualFold(strings.TrimSpace(status), "cancelled")) {
		terminalAt := int64(0)
		if payload.EndedAt != nil {
			terminalAt = *payload.EndedAt
		} else if payload.CanceledAt != nil {
			terminalAt = *payload.CanceledAt
		}
		if terminalAt > 0 && terminalAt <= event.Created {
			options.ImmediateRevoke = true
		}
	}
	return subscriptionID, status, periodEnd, evidence, options, nil
}

func stripeSubscriptionIDFromRaw(raw json.RawMessage) string {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" || strings.TrimSpace(string(raw)) == "null" {
		return ""
	}
	var id string
	if common.Unmarshal(raw, &id) == nil && strings.TrimSpace(id) != "" {
		return strings.TrimSpace(id)
	}
	var object struct {
		ID string `json:"id"`
	}
	if common.Unmarshal(raw, &object) == nil {
		return strings.TrimSpace(object.ID)
	}
	return ""
}

func stripeInvoiceLifecycleEvidence(event stripe.Event, invoiceCurrency string, lines []stripeInvoiceLine, amountDue int64, amountPaid int64) (*model.SubscriptionLifecycleEvidence, error) {
	currency := strings.ToUpper(strings.TrimSpace(invoiceCurrency))
	if !isValidStripeCurrencyCode(currency) {
		return nil, errStripeCheckoutInvalid
	}
	var priceID string
	var unitAmount int64
	priceLineCount := 0
	for _, line := range lines {
		if len(line.Price) == 0 || strings.TrimSpace(string(line.Price)) == "null" {
			continue
		}
		priceLineCount++
		// New API creates exactly one recurring checkout unit.  Stripe's
		// invoice line quantity is part of the signed evidence; accepting an
		// omitted, zero, fractional, or multi-unit quantity would let a
		// cheaper/malformed invoice advance a single-unit local entitlement.
		if line.Quantity == nil || *line.Quantity != 1 {
			return nil, errStripeCheckoutInvalid
		}
		var price struct {
			ID         string `json:"id"`
			UnitAmount *int64 `json:"unit_amount"`
			Currency   string `json:"currency"`
		}
		if common.Unmarshal(line.Price, &price) != nil || strings.TrimSpace(price.ID) == "" || price.UnitAmount == nil {
			return nil, errStripeCheckoutInvalid
		}
		lineCurrency := strings.ToUpper(strings.TrimSpace(price.Currency))
		if !isValidStripeCurrencyCode(lineCurrency) {
			return nil, errStripeCheckoutInvalid
		}
		if lineCurrency != currency {
			return nil, errStripeCheckoutMismatch
		}
		if *price.UnitAmount <= 0 || line.Amount == nil || *line.Amount <= 0 || *line.Amount != *price.UnitAmount {
			return nil, errStripeCheckoutInvalid
		}
		if priceID != "" && (priceID != strings.TrimSpace(price.ID) || unitAmount != *price.UnitAmount) {
			// A local plan represents one recurring price. Multiple contradictory
			// invoice line prices cannot be safely attributed to that snapshot.
			return nil, errStripeCheckoutMismatch
		}
		priceID = strings.TrimSpace(price.ID)
		unitAmount = *price.UnitAmount
	}
	if priceLineCount > 1 {
		return nil, errStripeCheckoutMismatch
	}
	if priceLineCount == 0 || priceID == "" || unitAmount <= 0 {
		return nil, errStripeCheckoutInvalid
	}
	if amountDue < 0 || amountPaid < 0 {
		return nil, errStripeCheckoutInvalid
	}
	// Keep amount_due/amount_paid in the parser contract: a paid invoice must
	// not claim a zero/negative total. The recurring base amount itself comes
	// from price.unit_amount so taxes and fees do not alter the local snapshot
	// comparison.
	if event.Type == "invoice.payment_succeeded" {
		if amountPaid <= 0 {
			return nil, errStripeCheckoutInvalid
		}
		if amountPaid < unitAmount {
			return nil, errStripeCheckoutMismatch
		}
		if amountDue > 0 && amountDue < unitAmount {
			return nil, errStripeCheckoutMismatch
		}
	}
	if event.Type == "invoice.payment_failed" && amountDue <= 0 {
		return nil, errStripeCheckoutInvalid
	}
	if event.Type == "invoice.payment_failed" && amountDue < unitAmount {
		return nil, errStripeCheckoutMismatch
	}
	return &model.SubscriptionLifecycleEvidence{
		Amount:    stripeAmountFromMinorUnits(unitAmount, currency),
		Currency:  currency,
		ProductID: priceID,
	}, nil
}

// isValidStripeCurrencyCode accepts the ISO-4217-style three-letter code
// Stripe uses in invoice and price objects.  A length-only check would allow
// control characters or punctuation to flow into amount conversion and local
// snapshot comparisons.
func isValidStripeCurrencyCode(currency string) bool {
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

func sessionCompleted(ctx context.Context, event stripe.Event, callerIp string) error {
	customerId := event.GetObjectValue("customer")
	referenceId := event.GetObjectValue("client_reference_id")
	status := event.GetObjectValue("status")
	if "complete" != status {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe checkout.completed 状态异常，忽略处理 trade_no=%s status=%s client_ip=%s", referenceId, status, callerIp))
		return nil
	}

	paymentStatus := event.GetObjectValue("payment_status")
	if paymentStatus != "paid" {
		logger.LogInfo(ctx, fmt.Sprintf("Stripe Checkout 支付未完成，等待异步结果 trade_no=%s payment_status=%s client_ip=%s", referenceId, paymentStatus, callerIp))
		return nil
	}

	return fulfillOrder(ctx, event, referenceId, customerId, callerIp)
}

// sessionAsyncPaymentSucceeded handles delayed payment methods (bank transfer, SEPA, etc.)
// that confirm payment after the checkout session completes.
func sessionAsyncPaymentSucceeded(ctx context.Context, event stripe.Event, callerIp string) error {
	customerId := event.GetObjectValue("customer")
	referenceId := event.GetObjectValue("client_reference_id")
	logger.LogInfo(ctx, fmt.Sprintf("Stripe 异步支付成功 trade_no=%s client_ip=%s", referenceId, callerIp))

	return fulfillOrder(ctx, event, referenceId, customerId, callerIp)
}

// sessionAsyncPaymentFailed marks orders as failed when delayed payment methods
// ultimately fail (e.g. bank transfer not received, SEPA rejected).
func sessionAsyncPaymentFailed(ctx context.Context, event stripe.Event, callerIp string) error {
	checkout, parseErr := stripeSessionFromEvent(event)
	if parseErr != nil {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe 异步支付失败事件载荷无效 client_ip=%s error=%q", callerIp, parseErr.Error()))
		return parseErr
	}
	referenceId := strings.TrimSpace(checkout.ClientReferenceID)
	logger.LogWarn(ctx, fmt.Sprintf("Stripe 异步支付失败 trade_no=%s client_ip=%s", referenceId, callerIp))

	if len(referenceId) == 0 {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe 异步支付失败事件缺少订单号 client_ip=%s", callerIp))
		return errStripeCheckoutInvalid
	}

	LockOrder(referenceId)
	defer UnlockOrder(referenceId)

	kind, lookupErr := lookupStripeOrderKind(referenceId)
	if lookupErr != nil {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe 异步支付失败但本地订单不存在 trade_no=%s client_ip=%s", referenceId, callerIp))
		return lookupErr
	}

	switch kind {
	case "subscription":
		order, err := model.GetSubscriptionOrderByTradeNoWithError(referenceId)
		if err != nil {
			return err
		}
		if order.PaymentProvider != model.PaymentProviderStripe {
			logger.LogWarn(ctx, fmt.Sprintf("Stripe 异步支付失败但订阅订单支付网关不匹配 trade_no=%s payment_provider=%s client_ip=%s", referenceId, order.PaymentProvider, callerIp))
			return model.ErrPaymentMethodMismatch
		}
		if _, err := validateStripeCheckoutLifecycle(
			event,
			referenceId,
			"subscription",
			order.ProviderCheckoutID,
			order.ProviderProductID,
			order.ProviderMerchantID,
			stripe.CheckoutSessionStatusComplete,
		); err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("Stripe 异步支付失败订阅事件与本地订单不匹配 trade_no=%s client_ip=%s error=%q", referenceId, callerIp, err.Error()))
			return err
		}
		if err := model.ExpireSubscriptionOrder(referenceId, model.PaymentProviderStripe); err != nil {
			logger.LogError(ctx, fmt.Sprintf("Stripe 标记订阅订单失败状态失败 trade_no=%s client_ip=%s error=%q", referenceId, callerIp, err.Error()))
			return err
		}
		logger.LogInfo(ctx, fmt.Sprintf("Stripe 订阅订单异步支付失败，已标记为过期 trade_no=%s client_ip=%s", referenceId, callerIp))
		return nil

	case "topup":
		topUp, err := model.GetTopUpByTradeNoWithError(referenceId)
		if err != nil {
			return err
		}
		if topUp.PaymentProvider != model.PaymentProviderStripe {
			logger.LogWarn(ctx, fmt.Sprintf("Stripe 异步支付失败但订单支付网关不匹配 trade_no=%s payment_provider=%s client_ip=%s", referenceId, topUp.PaymentProvider, callerIp))
			return model.ErrPaymentMethodMismatch
		}
		if _, err := validateStripeCheckoutLifecycle(
			event,
			referenceId,
			"topup",
			topUp.ProviderCheckoutID,
			topUp.ProviderProductID,
			topUp.ProviderMerchantID,
			stripe.CheckoutSessionStatusComplete,
		); err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("Stripe 异步支付失败事件与本地订单不匹配 trade_no=%s client_ip=%s error=%q", referenceId, callerIp, err.Error()))
			return err
		}

		// Use the row-locked transition instead of saving the object loaded above.
		// A delayed failure can race a successful callback on another instance;
		// stale Save would otherwise downgrade a paid order after its quota was
		// credited.
		if err := model.UpdatePendingTopUpStatus(referenceId, model.PaymentProviderStripe, common.TopUpStatusFailed); err != nil {
			if errors.Is(err, model.ErrTopUpStatusInvalid) {
				current, reloadErr := model.GetTopUpByTradeNoWithError(referenceId)
				if reloadErr == nil && current != nil && current.Status != common.TopUpStatusPending {
					logger.LogInfo(ctx, fmt.Sprintf("Stripe 异步支付失败但订单已在其他回调中终态化，忽略处理 trade_no=%s status=%s client_ip=%s", referenceId, current.Status, callerIp))
					return nil
				}
				if reloadErr != nil {
					return reloadErr
				}
			}
			logger.LogError(ctx, fmt.Sprintf("Stripe 标记充值订单失败状态失败 trade_no=%s client_ip=%s error=%q", referenceId, callerIp, err.Error()))
			return err
		}
		logger.LogInfo(ctx, fmt.Sprintf("Stripe 充值订单已标记为失败 trade_no=%s client_ip=%s", referenceId, callerIp))
		return nil
	default:
		return errStripeCheckoutInvalid
	}
}

// fulfillOrder is the shared logic for crediting quota after payment is confirmed.
func fulfillOrder(ctx context.Context, event stripe.Event, referenceId string, customerId string, callerIp string) error {
	if len(referenceId) == 0 {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe 完成订单时缺少订单号 client_ip=%s", callerIp))
		return errStripeCheckoutInvalid
	}

	LockOrder(referenceId)
	defer UnlockOrder(referenceId)
	kind, err := lookupStripeOrderKind(referenceId)
	if err != nil {
		return err
	}
	settlement, err := buildStripeProviderSettlement(event, referenceId, kind)
	if err != nil {
		return err
	}
	if kind == "subscription" {
		if strings.TrimSpace(settlement.ProviderSubscriptionID) == "" {
			// Stripe normally includes the subscription object on a completed
			// subscription Checkout Session. Keep the initial grant compatible
			// with older/partially-expanded payloads, but make the missing
			// lifecycle identity visible for reconciliation; no renewal grant is
			// attempted without this identity.
			logger.LogWarn(ctx, fmt.Sprintf("Stripe 订阅 Checkout 缺少 provider subscription id，已完成首次结算但未启用续费同步 trade_no=%s event_type=%s client_ip=%s", referenceId, string(event.Type), callerIp))
		}
		outcome, err := model.CompleteSubscriptionOrderVerifiedWithOutcome(settlement)
		if err != nil {
			return err
		}
		if outcome == model.EpaySettlementPaidUncredited {
			// The payment and provider evidence are durably recorded, but the
			// entitlement cap blocked granting access. ACK Stripe so it does not
			// retry forever; an operator can retry the grant or refund the order.
			logger.LogWarn(ctx, fmt.Sprintf("Stripe 订阅已付款但未开通，订单进入 paid_uncredited trade_no=%s event_type=%s client_ip=%s", referenceId, string(event.Type), callerIp))
			return nil
		}
		logger.LogInfo(ctx, fmt.Sprintf("Stripe 订阅订单处理成功 trade_no=%s event_type=%s client_ip=%s", referenceId, string(event.Type), callerIp))
		return nil
	}
	updates := map[string]interface{}{}
	if strings.TrimSpace(customerId) != "" {
		updates["stripe_customer"] = customerId
	}
	result, err := model.SettleTopUpProvider(settlement, callerIp, updates)
	if err != nil {
		return err
	}
	if result.PaidUncredited {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe 充值已付款但额度未入账，订单进入 paid_uncredited trade_no=%s amount=%s currency=%s event_type=%s client_ip=%s", referenceId, settlement.Amount, settlement.Currency, string(event.Type), callerIp))
		return nil
	}
	logger.LogInfo(ctx, fmt.Sprintf("Stripe 充值成功 trade_no=%s amount=%s currency=%s event_type=%s client_ip=%s", referenceId, settlement.Amount, settlement.Currency, string(event.Type), callerIp))
	return nil
}

func sessionExpired(ctx context.Context, event stripe.Event) error {
	checkout, parseErr := stripeSessionFromEvent(event)
	if parseErr != nil {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe checkout.expired 事件载荷无效 error=%q", parseErr.Error()))
		return parseErr
	}
	referenceId := strings.TrimSpace(checkout.ClientReferenceID)
	if checkout.Status != stripe.CheckoutSessionStatusExpired {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe checkout.expired 状态异常 trade_no=%s status=%s", referenceId, checkout.Status))
		return errStripeCheckoutInvalid
	}

	if len(referenceId) == 0 {
		logger.LogWarn(ctx, "Stripe checkout.expired 缺少订单号")
		return errStripeCheckoutInvalid
	}

	// Subscription order expiration
	LockOrder(referenceId)
	defer UnlockOrder(referenceId)
	kind, err := lookupStripeOrderKind(referenceId)
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("Stripe 订单过期事件本地订单查询失败 trade_no=%s error=%q", referenceId, err.Error()))
		return err
	}
	if kind == "subscription" {
		order, lookupErr := model.GetSubscriptionOrderByTradeNoWithError(referenceId)
		if lookupErr != nil {
			return lookupErr
		}
		if order.PaymentProvider != model.PaymentProviderStripe {
			return model.ErrPaymentMethodMismatch
		}
		if _, err := validateStripeCheckoutLifecycle(
			event,
			referenceId,
			"subscription",
			order.ProviderCheckoutID,
			order.ProviderProductID,
			order.ProviderMerchantID,
			stripe.CheckoutSessionStatusExpired,
		); err != nil {
			return err
		}
		if err := model.ExpireSubscriptionOrder(referenceId, model.PaymentProviderStripe); err != nil {
			return err
		}
		logger.LogInfo(ctx, fmt.Sprintf("Stripe 订阅订单已过期 trade_no=%s", referenceId))
		return nil
	}

	topUp, lookupErr := model.GetTopUpByTradeNoWithError(referenceId)
	if lookupErr != nil {
		return lookupErr
	}
	if topUp.PaymentProvider != model.PaymentProviderStripe {
		return model.ErrPaymentMethodMismatch
	}
	if _, err := validateStripeCheckoutLifecycle(
		event,
		referenceId,
		"topup",
		topUp.ProviderCheckoutID,
		topUp.ProviderProductID,
		topUp.ProviderMerchantID,
		stripe.CheckoutSessionStatusExpired,
	); err != nil {
		return err
	}

	err = model.UpdatePendingTopUpStatus(referenceId, model.PaymentProviderStripe, common.TopUpStatusExpired)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("Stripe 充值订单过期处理失败 trade_no=%s error=%q", referenceId, err.Error()))
		return err
	}

	logger.LogInfo(ctx, fmt.Sprintf("Stripe 充值订单已过期 trade_no=%s", referenceId))
	return nil
}

// genStripeLink generates a Stripe Checkout session URL for payment.
// It creates a new checkout session with the specified parameters and returns the payment URL.
//
// Parameters:
//   - referenceId: unique reference identifier for the transaction
//   - customerId: existing Stripe customer ID (empty string if new customer)
//   - email: customer email address for new customer creation
//   - amount: quantity of units to purchase
//   - successURL: custom URL to redirect after successful payment (empty for default)
//   - cancelURL: custom URL to redirect when payment is canceled (empty for default)
//
// Returns the checkout session URL or an error if the session creation fails.
func genStripeLink(referenceId string, customerId string, email string, amount int64, successURL string, cancelURL string) (string, error) {
	result, err := genStripeCheckoutSession(referenceId, customerId, email, amount, successURL, cancelURL, nil)
	if err != nil {
		return "", err
	}
	return result.URL, nil
}

func genStripeCheckoutSession(referenceId string, customerId string, email string, amount int64, successURL string, cancelURL string, metadata map[string]string) (*stripe.CheckoutSession, error) {
	stripeConfig := setting.GetStripeConfig()
	return genStripeCheckoutSessionWithConfig(referenceId, customerId, email, amount, successURL, cancelURL, metadata, stripeConfig)
}

func genStripeCheckoutSessionWithConfig(referenceId string, customerId string, email string, amount int64, successURL string, cancelURL string, metadata map[string]string, stripeConfig setting.StripeConfig) (*stripe.CheckoutSession, error) {
	if !strings.HasPrefix(stripeConfig.ApiSecret, "sk_") && !strings.HasPrefix(stripeConfig.ApiSecret, "rk_") {
		return nil, fmt.Errorf("无效的Stripe API密钥")
	}

	// Use custom URLs if provided, otherwise use defaults
	if successURL == "" {
		successURL = paymentReturnPath("/usage-logs")
	}
	if cancelURL == "" {
		cancelURL = paymentReturnPath("/wallet")
	}

	params := &stripe.CheckoutSessionParams{
		ClientReferenceID: stripe.String(referenceId),
		SuccessURL:        stripe.String(successURL),
		CancelURL:         stripe.String(cancelURL),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(stripeConfig.PriceID),
				Quantity: stripe.Int64(amount),
			},
		},
		Mode: stripe.String(string(stripe.CheckoutSessionModePayment)),
		// A top-up's entitlement is fixed before redirecting to Stripe. Allowing
		// customer-entered promotion codes would make amount_total lower than
		// the frozen credit, so promotions are intentionally disabled here.
		AllowPromotionCodes: stripe.Bool(false),
		Metadata:            metadata,
	}

	if "" == customerId {
		if "" != email {
			params.CustomerEmail = stripe.String(email)
		}

		params.CustomerCreation = stripe.String(string(stripe.CheckoutSessionCustomerCreationAlways))
	} else {
		params.Customer = stripe.String(customerId)
	}

	// stripe-go v81's package-level session helper reads stripe.Key. Keep the
	// key assignment and request together under one lock so concurrent checkouts
	// (or a key rotation) cannot cross-wire credentials.
	stripeKeyMu.Lock()
	stripe.Key = stripeConfig.ApiSecret
	result, err := session.New(params)
	stripeKeyMu.Unlock()
	if err != nil {
		return nil, err
	}
	if result == nil || strings.TrimSpace(result.ID) == "" || strings.TrimSpace(result.URL) == "" {
		return nil, fmt.Errorf("Stripe returned an empty checkout session")
	}
	return result, nil
}

func GetChargedAmount(count float64, user model.User) float64 {
	topUpGroupRatio := common.GetTopupGroupRatio(user.Group)
	if topUpGroupRatio == 0 {
		topUpGroupRatio = 1
	}

	return count * topUpGroupRatio
}

func getStripeCreditedQuota(amount int64, group string) decimal.Decimal {
	topUpGroupRatio := common.GetTopupGroupRatio(group)
	if topUpGroupRatio == 0 {
		topUpGroupRatio = 1
	}
	return decimal.NewFromInt(amount).
		Mul(decimal.NewFromFloat(topUpGroupRatio)).
		Mul(decimal.NewFromFloat(common.GetQuotaPerUnit()))
}

func getStripePayMoney(amount float64, group string) float64 {
	stripeConfig := setting.GetStripeConfig()
	return getStripePayMoneyWithConfig(amount, group, stripeConfig, operation_setting.GetPaymentSettingSnapshot(), operation_setting.GetGeneralSettingSnapshot())
}

func getStripePayMoneyWithConfig(amount float64, group string, stripeConfig setting.StripeConfig, paymentSetting operation_setting.PaymentSetting, generalSetting operation_setting.GeneralSetting) float64 {
	originalAmount := amount
	if generalSetting.QuotaDisplayType == operation_setting.QuotaDisplayTypeTokens {
		amount = amount / common.GetQuotaPerUnit()
	}
	// Using float64 for monetary calculations is acceptable here due to the small amounts involved
	topupGroupRatio := common.GetTopupGroupRatio(group)
	if topupGroupRatio == 0 {
		topupGroupRatio = 1
	}
	// apply optional preset discount by the original request amount (if configured), default 1.0
	discount := 1.0
	if ds, ok := paymentSetting.AmountDiscount[int(originalAmount)]; ok {
		if ds > 0 {
			discount = ds
		}
	}
	payMoney := amount * stripeConfig.UnitPrice * topupGroupRatio * discount
	return payMoney
}

func getStripeMinTopup() int64 {
	return getStripeMinTopupWithConfig(setting.GetStripeConfig(), operation_setting.GetGeneralSettingSnapshot())
}

func getStripeMinTopupWithConfig(stripeConfig setting.StripeConfig, generalSetting operation_setting.GeneralSetting) int64 {
	if stripeConfig.MinTopUp <= 0 {
		return 0
	}
	if generalSetting.QuotaDisplayType != operation_setting.QuotaDisplayTypeTokens {
		return int64(stripeConfig.MinTopUp)
	}
	// Do not multiply ints directly: QuotaPerUnit and the configured minimum
	// are operator-controlled and their product can overflow before the request
	// reaches the normal top-up quota validator. Decimal conversion lets us
	// saturate to the wallet's representable ceiling instead.
	quota, err := common.WalletQuotaFromDecimalStrict(
		decimal.NewFromInt(int64(stripeConfig.MinTopUp)).Mul(decimal.NewFromFloat(common.GetQuotaPerUnit())),
	)
	if err != nil || quota <= 0 {
		return common.MaxWalletQuota
	}
	return int64(quota)
}

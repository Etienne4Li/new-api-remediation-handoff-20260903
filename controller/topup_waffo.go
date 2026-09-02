package controller

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/thanhpk/randstr"
	waffo "github.com/waffo-com/waffo-go"
	"github.com/waffo-com/waffo-go/config"
	"github.com/waffo-com/waffo-go/core"
	"github.com/waffo-com/waffo-go/types/order"
)

func getWaffoSDK() (*waffo.Waffo, error) {
	return getWaffoSDKWithConfig(setting.GetWaffoConfig())
}

func getWaffoSDKWithConfig(waffoConfig setting.WaffoConfig) (*waffo.Waffo, error) {
	env := config.Sandbox
	apiKey := waffoConfig.SandboxApiKey
	privateKey := waffoConfig.SandboxPrivateKey
	publicKey := waffoConfig.SandboxPublicCert
	if !waffoConfig.Sandbox {
		env = config.Production
		apiKey = waffoConfig.ApiKey
		privateKey = waffoConfig.PrivateKey
		publicKey = waffoConfig.PublicCert
	}
	builder := config.NewConfigBuilder().
		APIKey(apiKey).
		PrivateKey(privateKey).
		WaffoPublicKey(publicKey).
		Environment(env)
	if waffoConfig.MerchantID != "" {
		builder = builder.MerchantID(waffoConfig.MerchantID)
	}
	cfg, err := builder.Build()
	if err != nil {
		return nil, err
	}
	return waffo.New(cfg), nil
}

func getWaffoUserEmail(user *model.User) string {
	return fmt.Sprintf("%d@examples.com", user.Id)
}

func getWaffoCurrency() string {
	return getWaffoCurrencyWithConfig(setting.GetWaffoConfig())
}

func getWaffoCurrencyWithConfig(waffoConfig setting.WaffoConfig) string {
	if waffoConfig.Currency != "" {
		return strings.ToUpper(strings.TrimSpace(waffoConfig.Currency))
	}
	return "USD"
}

func buildWaffoTopUpGoodsInfo(amount int64) *order.GoodsInfo {
	appName := strings.TrimSpace(common.GetSystemName())
	if appName == "" {
		appName = "New API"
	}
	return &order.GoodsInfo{
		GoodsName: fmt.Sprintf("Recharge %d credits", amount),
		AppName:   appName,
	}
}

// zeroDecimalCurrencies 零小数位币种，金额不能带小数点
var zeroDecimalCurrencies = map[string]bool{
	"IDR": true, "JPY": true, "KRW": true, "VND": true,
}

func formatWaffoAmount(amount float64, currency string) string {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if zeroDecimalCurrencies[currency] {
		return fmt.Sprintf("%.0f", amount)
	}
	return fmt.Sprintf("%.2f", amount)
}

// getWaffoPayMoney converts the user-facing amount to USD for Waffo payment.
// Waffo only accepts USD, so this function handles the conversion from different
// display types (USD/CNY/TOKENS) to the actual USD amount to charge.
func getWaffoPayMoney(amount float64, group string) float64 {
	return getWaffoPayMoneyWithConfigAndPayment(amount, group, setting.GetWaffoConfig(), operation_setting.GetPaymentSettingSnapshot(), operation_setting.GetGeneralSettingSnapshot())
}

func getWaffoPayMoneyWithConfig(amount float64, group string, waffoConfig setting.WaffoConfig) float64 {
	return getWaffoPayMoneyWithConfigAndPayment(amount, group, waffoConfig, operation_setting.GetPaymentSettingSnapshot(), operation_setting.GetGeneralSettingSnapshot())
}

func getWaffoPayMoneyWithConfigAndPayment(amount float64, group string, waffoConfig setting.WaffoConfig, paymentSetting operation_setting.PaymentSetting, generalSetting operation_setting.GeneralSetting) float64 {
	originalAmount := amount
	if generalSetting.QuotaDisplayType == operation_setting.QuotaDisplayTypeTokens {
		amount = amount / common.GetQuotaPerUnit()
	}
	topupGroupRatio := common.GetTopupGroupRatio(group)
	if topupGroupRatio == 0 {
		topupGroupRatio = 1
	}
	discount := 1.0
	if ds, ok := paymentSetting.AmountDiscount[int(originalAmount)]; ok {
		if ds > 0 {
			discount = ds
		}
	}
	return amount * waffoConfig.UnitPrice * topupGroupRatio * discount
}

type WaffoPayRequest struct {
	Amount         int64  `json:"amount"`
	PayMethodIndex *int   `json:"pay_method_index"` // 服务端支付方式列表的索引，nil 表示由 Waffo 自动选择
	PayMethodType  string `json:"pay_method_type"`  // Deprecated: 兼容旧前端，优先使用 pay_method_index
	PayMethodName  string `json:"pay_method_name"`  // Deprecated: 兼容旧前端，优先使用 pay_method_index
}

func RequestWaffoAmount(c *gin.Context) {
	waffoConfig := setting.GetWaffoConfig()
	generalSetting := operation_setting.GetGeneralSettingSnapshot()
	paymentSetting := operation_setting.GetPaymentSettingSnapshot()
	var req WaffoPayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}

	waffoMinTopup := int64(waffoConfig.MinTopUp)
	if req.Amount < waffoMinTopup {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", waffoMinTopup)})
		return
	}
	id := c.GetInt("id")
	creditedQuota, quotaErr := validateTopUpQuotaWithDisplayType(req.Amount, generalSetting.QuotaDisplayType)
	if quotaErr != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": quotaErr.Error()})
		return
	}
	if err := model.ValidateTopUpQuotaCapacity(id, creditedQuota); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": err.Error()})
		return
	}

	group, err := model.GetUserGroup(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}

	payMoney := getWaffoPayMoneyWithConfigAndPayment(float64(req.Amount), group, waffoConfig, paymentSetting, generalSetting)
	if payMoney <= 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "success", "data": strconv.FormatFloat(payMoney, 'f', 2, 64)})
}

// RequestWaffoPay 创建 Waffo 支付订单
func RequestWaffoPay(c *gin.Context) {
	waffoConfig := setting.GetWaffoConfig()
	generalSetting := operation_setting.GetGeneralSettingSnapshot()
	paymentSetting := operation_setting.GetPaymentSettingSnapshot()
	if !waffoConfig.Enabled {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo 支付未启用"})
		return
	}

	var req WaffoPayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	waffoMinTopup := int64(waffoConfig.MinTopUp)
	if req.Amount < waffoMinTopup {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", waffoMinTopup)})
		return
	}
	id := c.GetInt("id")
	creditedQuota, quotaErr := validateTopUpQuotaWithDisplayType(req.Amount, generalSetting.QuotaDisplayType)
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

	// 从服务端配置查找支付方式，客户端只传索引或旧字段
	var resolvedPayMethodType, resolvedPayMethodName string
	methods := setting.GetWaffoPayMethods()
	if req.PayMethodIndex != nil {
		// 新协议：按索引查找
		idx := *req.PayMethodIndex
		if idx < 0 || idx >= len(methods) {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo 支付方式索引无效 user_id=%d pay_method_index=%d method_count=%d", id, idx, len(methods)))
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "不支持的支付方式"})
			return
		}
		resolvedPayMethodType = methods[idx].PayMethodType
		resolvedPayMethodName = methods[idx].PayMethodName
	} else if req.PayMethodType != "" {
		// 兼容旧前端：验证客户端传的值在服务端列表中
		valid := false
		for _, m := range methods {
			if m.PayMethodType == req.PayMethodType && m.PayMethodName == req.PayMethodName {
				valid = true
				resolvedPayMethodType = m.PayMethodType
				resolvedPayMethodName = m.PayMethodName
				break
			}
		}
		if !valid {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo 支付方式无效 user_id=%d pay_method_type=%s pay_method_name=%q", id, req.PayMethodType, req.PayMethodName))
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "不支持的支付方式"})
			return
		}
	}
	// resolvedPayMethodType/Name 为空时，Waffo 自动选择支付方式

	group, err := model.GetUserGroup(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}
	payMoney := getWaffoPayMoneyWithConfigAndPayment(float64(req.Amount), group, waffoConfig, paymentSetting, generalSetting)
	if payMoney < 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}
	merchantID := strings.TrimSpace(waffoConfig.MerchantID)
	if merchantID == "" {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 创建充值订单失败 user_id=%d reason=merchant_id_missing", id))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付配置错误"})
		return
	}

	// 生成唯一订单号，paymentRequestId 与 merchantOrderId 保持一致，简化追踪
	merchantOrderId := fmt.Sprintf("WAFFO-%d-%d-%s", id, time.Now().UnixMilli(), randstr.String(6))
	paymentRequestId := merchantOrderId

	// Token 模式下归一化 Amount（存等价美元/CNY 数量，避免 RechargeWaffo 双重放大）
	amount := req.Amount
	if generalSetting.QuotaDisplayType == operation_setting.QuotaDisplayTypeTokens {
		amount = int64(float64(req.Amount) / common.GetQuotaPerUnit())
		if amount < 1 {
			amount = 1
		}
	}

	currency := getWaffoCurrencyWithConfig(waffoConfig)
	if len(currency) != 3 {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 创建充值订单失败 user_id=%d reason=currency_invalid currency=%q", id, currency))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付配置错误"})
		return
	}
	goodsInfo := buildWaffoTopUpGoodsInfo(req.Amount)
	providerAmount := formatWaffoAmount(payMoney, currency)
	providerProductID := "ONE_TIME_PAYMENT"
	providerOrderName := strings.TrimSpace(goodsInfo.GoodsName)
	providerEnvironment := waffoProviderEnvironment(waffoConfig)
	providerScopeFingerprint, scopeOK := model.ProviderPaymentScopeFingerprint(model.PaymentProviderWaffo, merchantID, providerEnvironment)
	if !scopeOK {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 创建充值订单失败 user_id=%d reason=provider_scope_invalid", id))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付配置错误"})
		return
	}

	// 创建本地订单并冻结所有会影响结算的输入。回调只允许匹配这些快照，
	// 不得根据届时的价格配置或回调载荷重新计算额度。
	topUp := &model.TopUp{
		UserId:                 id,
		Amount:                 amount,
		Money:                  payMoney,
		TradeNo:                merchantOrderId,
		PaymentMethod:          model.PaymentMethodWaffo,
		PaymentProvider:        model.PaymentProviderWaffo,
		CreateTime:             time.Now().Unix(),
		Status:                 common.TopUpStatusPending,
		CreditedQuota:          creditedQuota,
		ProviderMerchantID:     merchantID,
		ProviderOrderName:      providerOrderName,
		ProviderAmount:         providerAmount,
		ProviderProductID:      providerProductID,
		ProviderCurrency:       currency,
		ProviderKeyFingerprint: providerScopeFingerprint,
	}
	if err := topUp.Insert(); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 创建充值订单失败 user_id=%d trade_no=%s amount=%d error=%q", id, merchantOrderId, req.Amount, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	markTopUpFailed := func(reason string) {
		if statusErr := model.UpdatePendingTopUpStatus(merchantOrderId, model.PaymentProviderWaffo, common.TopUpStatusFailed); statusErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 充值订单失败状态补偿失败 user_id=%d trade_no=%s provider=%s reason=%s error=%q", id, merchantOrderId, model.PaymentProviderWaffo, reason, statusErr.Error()))
		}
	}

	sdk, err := getWaffoSDKWithConfig(waffoConfig)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo SDK 初始化失败 user_id=%d trade_no=%s error=%q", id, merchantOrderId, err.Error()))
		markTopUpFailed("sdk_init")
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付配置错误"})
		return
	}

	callbackAddr := service.GetCallbackAddress()
	notifyUrl := callbackAddr + "/api/waffo/webhook"
	if waffoConfig.NotifyURL != "" {
		notifyUrl = waffoConfig.NotifyURL
	}
	returnUrl := paymentReturnPath("/wallet?show_history=true")
	if waffoConfig.ReturnURL != "" {
		returnUrl = waffoConfig.ReturnURL
	}

	createParams := &order.CreateOrderParams{
		PaymentRequestID: paymentRequestId,
		MerchantOrderID:  merchantOrderId,
		OrderAmount:      providerAmount,
		OrderCurrency:    currency,
		OrderDescription: goodsInfo.GoodsName,
		OrderRequestedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		NotifyURL:        notifyUrl,
		MerchantInfo: &order.MerchantInfo{
			MerchantID: merchantID,
		},
		UserInfo: &order.UserInfo{
			UserID:       strconv.Itoa(user.Id),
			UserEmail:    getWaffoUserEmail(user),
			UserTerminal: "WEB",
		},
		PaymentInfo: &order.PaymentInfo{
			ProductName:   "ONE_TIME_PAYMENT",
			PayMethodType: resolvedPayMethodType,
			PayMethodName: resolvedPayMethodName,
		},
		GoodsInfo:          goodsInfo,
		SuccessRedirectURL: returnUrl,
		FailedRedirectURL:  returnUrl,
	}
	resp, err := sdk.Order().Create(c.Request.Context(), createParams, nil)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 创建订单失败 user_id=%d trade_no=%s error=%q", id, merchantOrderId, err.Error()))
		markTopUpFailed("provider_request")
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	if !resp.IsSuccess() {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo 创建订单业务失败 user_id=%d trade_no=%s code=%s message=%q response_meta=%s", id, merchantOrderId, resp.Code, resp.Message, common.SensitiveLogMeta(common.GetJsonString(resp))))
		markTopUpFailed("provider_rejected")
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}

	orderData := resp.GetData()
	if orderData == nil || strings.TrimSpace(orderData.PaymentRequestID) != paymentRequestId ||
		strings.TrimSpace(orderData.MerchantOrderID) != merchantOrderId || strings.TrimSpace(orderData.AcquiringOrderID) == "" {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 创建订单响应校验失败 user_id=%d trade_no=%s", id, merchantOrderId))
		markTopUpFailed("response_validation")
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}

	// 只更新 checkout id，避免在支付回调先到时用旧的 pending 结构体覆盖
	// 已经完成的订单。回调在 checkout id 持久化前会被拒绝并由网关重试。
	acquiringOrderID := strings.TrimSpace(orderData.AcquiringOrderID)
	updateResult := model.DB.Model(&model.TopUp{}).
		Where("id = ? AND trade_no = ? AND status = ?", topUp.Id, merchantOrderId, common.TopUpStatusPending).
		Update("provider_checkout_id", acquiringOrderID)
	if updateResult.Error != nil || updateResult.RowsAffected != 1 {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 保存 AcquiringOrderID 失败 user_id=%d trade_no=%s", id, merchantOrderId))
		// Do not overwrite a concurrently-settled row. A failed persistence means
		// no payment URL is returned; operations can reconcile the provider order.
		if updateResult.Error == nil {
			markTopUpFailed("checkout_id_persistence")
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建支付订单失败"})
		return
	}
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo 充值订单创建成功 user_id=%d trade_no=%s amount=%d money=%.2f pay_method_type=%s pay_method_name=%q", id, merchantOrderId, req.Amount, payMoney, resolvedPayMethodType, resolvedPayMethodName))

	paymentUrl := orderData.FetchRedirectURL()
	if paymentUrl == "" {
		paymentUrl = orderData.OrderAction
	}
	if strings.TrimSpace(paymentUrl) == "" {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 创建订单响应缺少支付动作 user_id=%d trade_no=%s", id, merchantOrderId))
		markTopUpFailed("missing_payment_action")
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"payment_url": paymentUrl,
			"order_id":    merchantOrderId,
		},
	})
}

// webhookPayloadWithSubInfo 扩展 PAYMENT_NOTIFICATION，包含 SDK 未定义的 subscriptionInfo 字段
type webhookPayloadWithSubInfo struct {
	EventType string `json:"eventType"`
	Result    struct {
		core.PaymentNotificationResult
		SubscriptionInfo *webhookSubscriptionInfo `json:"subscriptionInfo,omitempty"`
	} `json:"result"`
}

type webhookSubscriptionInfo struct {
	Period              string `json:"period,omitempty"`
	MerchantRequest     string `json:"merchantRequest,omitempty"`
	SubscriptionID      string `json:"subscriptionId,omitempty"`
	SubscriptionRequest string `json:"subscriptionRequest,omitempty"`
}

var (
	errWaffoCallbackInvalid  = errors.New("invalid Waffo payment notification")
	errWaffoCallbackMismatch = errors.New("Waffo payment notification does not match local order")
)

// waffoWebhookString reads a provider map field without coercing arbitrary
// JSON values. Coercion would allow malformed objects (for example a number
// or nested object) to accidentally pass an identity check.
func waffoWebhookString(values map[string]interface{}, key string) string {
	if values == nil {
		return ""
	}
	var value interface{}
	var ok bool
	for candidateKey, candidateValue := range values {
		if strings.EqualFold(strings.TrimSpace(candidateKey), strings.TrimSpace(key)) {
			value = candidateValue
			ok = true
			break
		}
	}
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func compareWaffoAmounts(expected, actual string) bool {
	expectedDecimal, expectedErr := decimal.NewFromString(strings.TrimSpace(expected))
	actualDecimal, actualErr := decimal.NewFromString(strings.TrimSpace(actual))
	return expectedErr == nil && actualErr == nil && expectedDecimal.IsPositive() && actualDecimal.IsPositive() && expectedDecimal.Equal(actualDecimal)
}

// validateWaffoCloseNotification validates only the identity that is needed
// to abandon a local order.  Waffo's PAYMENT_NOTIFICATION schema makes most
// result fields optional, and ORDER_CLOSE deliveries in particular may omit
// the acquiring order, merchant/product maps, and amount.  Requiring the
// complete payment snapshot for a close event causes a legitimately failed or
// expired order to be retried forever (and also prevents cleanup of legacy
// rows created before the snapshot columns existed).  A close event never
// grants quota, so the signed merchant/payment order reference is the
// security boundary; any optional fields that are present are still compared
// with the local snapshot and contradictions are rejected.
func validateWaffoCloseNotification(result *core.PaymentNotificationResult, topUp *model.TopUp) error {
	if result == nil || topUp == nil {
		return errWaffoCallbackInvalid
	}
	if topUp.PaymentProvider != model.PaymentProviderWaffo {
		return model.ErrPaymentMethodMismatch
	}
	tradeNo := strings.TrimSpace(topUp.TradeNo)
	if tradeNo == "" {
		return errWaffoCallbackInvalid
	}

	// Both identifiers are echoed by current Waffo versions, but older
	// deliveries (and some failure paths) include only one.  At least one must
	// bind to the local merchant order; when both are present they must agree.
	merchantOrderID := strings.TrimSpace(result.MerchantOrderID)
	paymentRequestID := strings.TrimSpace(result.PaymentRequestID)
	if merchantOrderID == "" && paymentRequestID == "" {
		return errWaffoCallbackInvalid
	}
	if merchantOrderID != "" && merchantOrderID != tradeNo {
		return errWaffoCallbackMismatch
	}
	if paymentRequestID != "" && paymentRequestID != tradeNo {
		return errWaffoCallbackMismatch
	}

	// The acquiring id is optional on a close notification.  If the checkout
	// snapshot is available, however, a supplied value must match it.  For an
	// older row whose checkout id is absent, compare against a previously bound
	// provider transaction when one exists.
	if acquiringID := strings.TrimSpace(result.AcquiringOrderID); acquiringID != "" {
		expectedAcquiringID := strings.TrimSpace(topUp.ProviderCheckoutID)
		if expectedAcquiringID == "" && topUp.ProviderTradeNo != nil {
			expectedAcquiringID = strings.TrimSpace(*topUp.ProviderTradeNo)
		}
		if expectedAcquiringID != "" && acquiringID != expectedAcquiringID {
			return errWaffoCallbackMismatch
		}
	}

	// Optional signed copies remain useful tamper/misrouting checks.  Empty
	// values mean the provider omitted the field; they are not synthesized from
	// the callback and therefore do not weaken the close-only path above.
	if merchantID := waffoWebhookString(result.MerchantInfo, "merchantId"); merchantID != "" &&
		strings.TrimSpace(topUp.ProviderMerchantID) != "" && merchantID != strings.TrimSpace(topUp.ProviderMerchantID) {
		return errWaffoCallbackMismatch
	}
	if productID := waffoWebhookString(result.PaymentInfo, "productName"); productID != "" &&
		strings.TrimSpace(topUp.ProviderProductID) != "" && productID != strings.TrimSpace(topUp.ProviderProductID) {
		return errWaffoCallbackMismatch
	}
	if amount := strings.TrimSpace(result.OrderAmount); amount != "" && strings.TrimSpace(topUp.ProviderAmount) != "" &&
		!compareWaffoAmounts(topUp.ProviderAmount, amount) {
		return errWaffoCallbackMismatch
	}
	if currency := strings.ToUpper(strings.TrimSpace(result.OrderCurrency)); currency != "" &&
		strings.TrimSpace(topUp.ProviderCurrency) != "" && currency != strings.ToUpper(strings.TrimSpace(topUp.ProviderCurrency)) {
		return errWaffoCallbackMismatch
	}
	if description := strings.TrimSpace(result.OrderDescription); description != "" &&
		strings.TrimSpace(topUp.ProviderOrderName) != "" && description != strings.TrimSpace(topUp.ProviderOrderName) {
		return errWaffoCallbackMismatch
	}
	if goodsName := waffoWebhookString(result.GoodsInfo, "goodsName"); goodsName != "" &&
		strings.TrimSpace(topUp.ProviderOrderName) != "" && goodsName != strings.TrimSpace(topUp.ProviderOrderName) {
		return errWaffoCallbackMismatch
	}
	return nil
}

func isWaffoPaymentStatus(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case core.OrderStatusPayInProgress,
		core.OrderStatusAuthorizationRequired,
		core.OrderStatusAuthedWaitingCapture,
		"CAPTURE_IN_PROGRESS",
		core.OrderStatusPaySuccess,
		core.OrderStatusOrderClose:
		return true
	default:
		return false
	}
}

func isWaffoPendingPaymentStatus(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case core.OrderStatusPayInProgress,
		core.OrderStatusAuthorizationRequired,
		core.OrderStatusAuthedWaitingCapture,
		"CAPTURE_IN_PROGRESS":
		return true
	default:
		return false
	}
}

func waffoProviderEnvironment(cfg setting.WaffoConfig) string {
	if cfg.Sandbox {
		return "sandbox"
	}
	return "production"
}

// buildWaffoProviderSettlement verifies all provider fields that are needed
// to bind a PAYMENT_NOTIFICATION to an immutable local order snapshot. It is
// intentionally independent of a database transaction so the checks can be
// exercised in unit tests; SettleTopUpProvider performs the final locked,
// idempotent state transition.
func buildWaffoProviderSettlement(result *core.PaymentNotificationResult, topUp *model.TopUp, cfg setting.WaffoConfig) (model.ProviderSettlement, error) {
	if result == nil || topUp == nil {
		return model.ProviderSettlement{}, errWaffoCallbackInvalid
	}
	if topUp.PaymentProvider != model.PaymentProviderWaffo {
		return model.ProviderSettlement{}, model.ErrPaymentMethodMismatch
	}
	// Validate signed close/duplicate notifications too. Terminal local rows
	// are still valid reconciliation targets; rejecting them here would turn a
	// harmless redelivery into an endless provider retry (and, for a late close
	// after success, must never downgrade the paid order).
	if topUp.Status != common.TopUpStatusPending && topUp.Status != common.TopUpStatusSuccess &&
		topUp.Status != common.TopUpStatusFailed && topUp.Status != common.TopUpStatusExpired {
		return model.ProviderSettlement{}, model.ErrTopUpStatusInvalid
	}
	if topUp.CreditedQuota <= 0 || strings.TrimSpace(topUp.ProviderMerchantID) == "" ||
		strings.TrimSpace(topUp.ProviderOrderName) == "" || strings.TrimSpace(topUp.ProviderAmount) == "" ||
		strings.TrimSpace(topUp.ProviderProductID) == "" || strings.TrimSpace(topUp.ProviderCurrency) == "" ||
		strings.TrimSpace(topUp.ProviderCheckoutID) == "" {
		return model.ProviderSettlement{}, model.ErrProviderSnapshotMissing
	}

	tradeNo := strings.TrimSpace(topUp.TradeNo)
	if tradeNo == "" || strings.TrimSpace(result.PaymentRequestID) == "" || strings.TrimSpace(result.MerchantOrderID) == "" {
		return model.ProviderSettlement{}, errWaffoCallbackInvalid
	}
	if strings.TrimSpace(result.PaymentRequestID) != tradeNo || strings.TrimSpace(result.MerchantOrderID) != tradeNo {
		return model.ProviderSettlement{}, errWaffoCallbackMismatch
	}

	acquiringOrderID := strings.TrimSpace(result.AcquiringOrderID)
	if acquiringOrderID == "" {
		return model.ProviderSettlement{}, errWaffoCallbackInvalid
	}
	if acquiringOrderID != strings.TrimSpace(topUp.ProviderCheckoutID) {
		return model.ProviderSettlement{}, errWaffoCallbackMismatch
	}

	merchantID := waffoWebhookString(result.MerchantInfo, "merchantId")
	if merchantID == "" {
		return model.ProviderSettlement{}, errWaffoCallbackInvalid
	}
	if merchantID != strings.TrimSpace(topUp.ProviderMerchantID) {
		return model.ProviderSettlement{}, errWaffoCallbackMismatch
	}
	providerAccountID := strings.TrimSpace(cfg.MerchantID)
	if providerAccountID == "" {
		return model.ProviderSettlement{}, errWaffoCallbackInvalid
	}
	if providerAccountID != merchantID {
		return model.ProviderSettlement{}, errWaffoCallbackMismatch
	}
	providerEnvironment := waffoProviderEnvironment(cfg)
	providerKeyFingerprint, scopeOK := model.ProviderPaymentScopeFingerprint(model.PaymentProviderWaffo, providerAccountID, providerEnvironment)
	if !scopeOK {
		return model.ProviderSettlement{}, errWaffoCallbackInvalid
	}

	productID := waffoWebhookString(result.PaymentInfo, "productName")
	if productID == "" {
		return model.ProviderSettlement{}, errWaffoCallbackInvalid
	}
	if productID != strings.TrimSpace(topUp.ProviderProductID) {
		return model.ProviderSettlement{}, errWaffoCallbackMismatch
	}

	amount := strings.TrimSpace(result.OrderAmount)
	currency := strings.ToUpper(strings.TrimSpace(result.OrderCurrency))
	description := strings.TrimSpace(result.OrderDescription)
	if amount == "" || currency == "" || description == "" {
		return model.ProviderSettlement{}, errWaffoCallbackInvalid
	}
	if !compareWaffoAmounts(topUp.ProviderAmount, amount) ||
		strings.ToUpper(strings.TrimSpace(topUp.ProviderCurrency)) != currency ||
		description != strings.TrimSpace(topUp.ProviderOrderName) {
		return model.ProviderSettlement{}, errWaffoCallbackMismatch
	}
	// goodsInfo is optional in some Waffo API versions, but when present it is
	// another signed copy of the order name and must not contradict the snapshot.
	if goodsName := waffoWebhookString(result.GoodsInfo, "goodsName"); goodsName != "" && goodsName != description {
		return model.ProviderSettlement{}, errWaffoCallbackMismatch
	}

	status := strings.ToUpper(strings.TrimSpace(result.OrderStatus))
	if !isWaffoPaymentStatus(status) {
		return model.ProviderSettlement{}, errWaffoCallbackInvalid
	}

	settlement := model.ProviderSettlement{
		OrderTradeNo:           tradeNo,
		Provider:               model.PaymentProviderWaffo,
		ProviderTradeNo:        acquiringOrderID,
		ProviderAccountID:      providerAccountID,
		ProviderEnvironment:    providerEnvironment,
		ProviderKeyFingerprint: providerKeyFingerprint,
		MerchantID:             merchantID,
		ProductID:              productID,
		CheckoutID:             acquiringOrderID,
		Currency:               currency,
		Amount:                 amount,
		OrderName:              description,
		PaymentObjects: []model.ProviderPaymentObject{{
			ObjectType: model.ProviderPaymentObjectAcquiringOrder,
			ObjectID:   acquiringOrderID,
		}},
	}
	if err := model.ValidateProviderSettlement(settlement); err != nil {
		return model.ProviderSettlement{}, errWaffoCallbackInvalid
	}
	return settlement, nil
}

// WaffoWebhook 处理 Waffo 回调通知（支付/退款/订阅）
func WaffoWebhook(c *gin.Context) {
	waffoConfig := setting.GetWaffoConfig()
	if !isWaffoWebhookEnabledWithConfig(waffoConfig) {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo webhook 被拒绝 reason=webhook_disabled path=%q client_ip=%s", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP()))
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	bodyBytes, err := readPaymentRequestBody(c)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo webhook 读取请求体失败 path=%q client_ip=%s error=%q", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP(), err.Error()))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	sdk, err := getWaffoSDKWithConfig(waffoConfig)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo webhook SDK 初始化失败 path=%q client_ip=%s error=%q", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP(), err.Error()))
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	wh := sdk.Webhook()
	bodyStr := string(bodyBytes)
	signature := c.GetHeader("X-SIGNATURE")
	bodyMeta := common.SensitiveLogBody(bodyBytes)
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo webhook 收到请求 path=%q client_ip=%s signature_meta=%s body_meta=%s", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP(), common.SensitiveLogMeta(signature), bodyMeta))

	// 验证请求签名
	if !wh.VerifySignature(bodyStr, signature) {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo webhook 验签失败 path=%q client_ip=%s signature_meta=%s body_meta=%s", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP(), common.SensitiveLogMeta(signature), bodyMeta))
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	var event core.WebhookEvent
	if err := common.Unmarshal(bodyBytes, &event); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo webhook 解析失败 path=%q client_ip=%s error=%q body_meta=%s", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP(), err.Error(), bodyMeta))
		sendWaffoWebhookResponse(c, wh, false, "invalid payload")
		return
	}

	switch event.EventType {
	case core.EventPayment:
		// 解析为扩展类型，区分普通支付和订阅支付
		var payload webhookPayloadWithSubInfo
		if err := common.Unmarshal(bodyBytes, &payload); err != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 支付回调载荷解析失败 event_type=%s client_ip=%s error=%q body_meta=%s", event.EventType, c.ClientIP(), err.Error(), bodyMeta))
			sendWaffoWebhookResponse(c, wh, false, "invalid payment payload")
			return
		}
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo webhook 验签并解析成功 event_type=%s merchant_order_id=%s order_status=%s client_ip=%s", event.EventType, payload.Result.MerchantOrderID, payload.Result.OrderStatus, c.ClientIP()))
		handleWaffoPayment(c, wh, waffoConfig, &payload.Result.PaymentNotificationResult, bodyStr)
	case core.EventRefund:
		var notification core.RefundNotification
		if err := common.Unmarshal(bodyBytes, &notification); err != nil || notification.Result == nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 退款回调载荷解析失败 event_type=%s client_ip=%s body_meta=%s", event.EventType, c.ClientIP(), bodyMeta))
			sendWaffoWebhookResponse(c, wh, false, "invalid refund payload")
			return
		}
		handleWaffoRefund(c, wh, sdk, waffoConfig, notification.Result, bodyStr)
	default:
		// Do not acknowledge event types that this installation does not process.
		// Waffo treats a signed 200 as durable delivery success; returning it here
		// would silently discard refund/subscription notifications and make later
		// reconciliation impossible. A signed failure keeps the delivery visible
		// and retryable until a handler is added or the operator resolves it.
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo webhook 不支持事件 event_type=%s client_ip=%s", event.EventType, c.ClientIP()))
		sendWaffoWebhookResponse(c, wh, false, "unsupported event")
	}
}

// handleWaffoPayment 处理支付通知。rawPayload 为可选参数，保留三参数
// 调用方式以兼容旧的内部测试/调用方。
func handleWaffoPayment(c *gin.Context, wh *core.WebhookHandler, cfg setting.WaffoConfig, result *core.PaymentNotificationResult, rawPayload ...string) {
	if result == nil {
		sendWaffoWebhookResponse(c, wh, false, "invalid payment notification")
		return
	}
	// Current callbacks contain merchantOrderId, while some historical/failure
	// deliveries contain only paymentRequestId.  Orders created by this adapter
	// intentionally set both to the same value, so either one is a safe lookup
	// key; the validation helpers below still reject a contradictory pair.
	tradeNo := strings.TrimSpace(result.MerchantOrderID)
	if tradeNo == "" {
		tradeNo = strings.TrimSpace(result.PaymentRequestID)
	}
	if tradeNo == "" {
		sendWaffoWebhookResponse(c, wh, false, "invalid payment notification")
		return
	}
	topUp, lookupErr := model.GetTopUpByTradeNoWithError(tradeNo)
	if lookupErr != nil {
		if errors.Is(lookupErr, model.ErrTopUpNotFound) {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo 回调对应订单不存在 trade_no=%s client_ip=%s", tradeNo, c.ClientIP()))
			// A valid provider event for an order that is not owned by this
			// installation is permanently non-actionable. ACK it explicitly so
			// the provider does not retry forever.
			sendWaffoWebhookResponse(c, wh, true, "")
			return
		} else {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 回调查询订单失败 trade_no=%s client_ip=%s error=%q", tradeNo, c.ClientIP(), lookupErr.Error()))
		}
		// A database failure is returned as a signed failure. With the Waffo
		// response contract this is a non-2xx retry signal.
		sendWaffoWebhookResponse(c, wh, false, "settlement unavailable")
		return
	}
	status := strings.ToUpper(strings.TrimSpace(result.OrderStatus))
	// ORDER_CLOSE is a terminal provider notification, not a payment
	// settlement.  Validate its order identity before touching the row, but do
	// not require fields (amount/product/acquiring id) that Waffo is allowed to
	// omit on failure/expiry callbacks.
	if status == core.OrderStatusOrderClose {
		if err := validateWaffoCloseNotification(result, topUp); err != nil {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo 关闭回调与订单不匹配 trade_no=%s client_ip=%s error=%q", tradeNo, c.ClientIP(), err.Error()))
			sendWaffoWebhookResponse(c, wh, false, "invalid payment notification")
			return
		}
		if err := model.UpdatePendingTopUpStatus(tradeNo, model.PaymentProviderWaffo, common.TopUpStatusFailed); err != nil {
			// Re-delivery after any terminal state is harmless. A successful
			// order must never be downgraded by a late close event. Preserve a
			// database error as a failed response so it remains retryable.
			current, lookupErr := model.GetTopUpByTradeNoWithError(tradeNo)
			if lookupErr == nil && current != nil && (current.Status == common.TopUpStatusSuccess || current.Status == common.TopUpStatusFailed || current.Status == common.TopUpStatusExpired) {
				sendWaffoWebhookResponse(c, wh, true, "")
				return
			}
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 标记关闭订单失败 trade_no=%s client_ip=%s error=%q", tradeNo, c.ClientIP(), err.Error()))
			sendWaffoWebhookResponse(c, wh, false, "settlement failed")
			return
		}
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo 订单已关闭 trade_no=%s client_ip=%s", tradeNo, c.ClientIP()))
		sendWaffoWebhookResponse(c, wh, true, "")
		return
	}

	// Payment success and progress events retain the strict immutable snapshot
	// validation. Unknown/intermediate values must remain a signed failure so a
	// future provider status cannot be silently acknowledged as settled.
	settlement, err := buildWaffoProviderSettlement(result, topUp, cfg)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo 回调与订单不匹配 trade_no=%s client_ip=%s error=%q", tradeNo, c.ClientIP(), err.Error()))
		sendWaffoWebhookResponse(c, wh, false, "invalid payment notification")
		return
	}
	if len(rawPayload) > 0 {
		settlement.Payload = rawPayload[0]
	}

	switch {
	case status == core.OrderStatusPaySuccess:
		outcome, settleErr := model.SettleTopUpProvider(settlement, c.ClientIP(), nil)
		if settleErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 充值结算失败 trade_no=%s client_ip=%s error=%q", tradeNo, c.ClientIP(), settleErr.Error()))
			sendWaffoWebhookResponse(c, wh, false, "settlement failed")
			return
		}
		if outcome.PaidUncredited {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo 充值已付款但额度未入账，订单进入 paid_uncredited trade_no=%s provider_trade_no=%s already_completed=%t client_ip=%s", tradeNo, settlement.ProviderTradeNo, outcome.AlreadyCompleted, c.ClientIP()))
		} else {
			logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo 充值成功 trade_no=%s provider_trade_no=%s already_completed=%t client_ip=%s", tradeNo, settlement.ProviderTradeNo, outcome.AlreadyCompleted, c.ClientIP()))
		}
		sendWaffoWebhookResponse(c, wh, true, "")
	case isWaffoPendingPaymentStatus(status):
		// These statuses are progress notifications. Acknowledge them, but keep
		// the local order pending until a separately signed PAY_SUCCESS arrives.
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo 订单仍在处理中 trade_no=%s order_status=%s client_ip=%s", tradeNo, status, c.ClientIP()))
		sendWaffoWebhookResponse(c, wh, true, "")
	default:
		// buildWaffoProviderSettlement currently rejects unknown statuses; keep
		// this branch defensive in case status handling changes independently.
		sendWaffoWebhookResponse(c, wh, false, "invalid payment status")
	}
}

// sendWaffoWebhookResponse 发送签名响应
func sendWaffoWebhookResponse(c *gin.Context, wh *core.WebhookHandler, success bool, msg string) {
	var body, sig string
	if success {
		body, sig = wh.BuildSuccessResponse()
	} else {
		body, sig = wh.BuildFailedResponse(msg)
	}
	c.Header("X-SIGNATURE", sig)
	// Waffo's webhook contract uses HTTP 200 for an accepted notification and
	// a non-2xx response for a failed processing attempt. Keep the signed body
	// and HTTP status aligned so a transient DB/settlement error is retried;
	// callers must never use success=true before settlement commits.
	status := http.StatusOK
	if !success {
		status = http.StatusBadRequest
	}
	c.Data(status, "application/json", []byte(body))
}

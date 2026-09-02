package controller

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

func GetTopUpInfo(c *gin.Context) {
	complianceConfirmed := operation_setting.IsPaymentComplianceConfirmed()
	paymentRuntimeConfig := operation_setting.GetPaymentRuntimeConfig()
	paymentSetting := operation_setting.GetPaymentSettingSnapshot()
	runtimeConfig := common.GetGeneralRuntimeConfig()
	stripeConfig := setting.GetStripeConfig()
	creemConfig := setting.GetCreemConfig()
	waffoConfig := setting.GetWaffoConfig()
	waffoPancakeConfig := setting.GetWaffoPancakeConfig()
	enableCreem := complianceConfirmed && isCreemTopUpEnabledWithConfig(creemConfig)
	enableWaffoPancake := complianceConfirmed && isWaffoPancakeTopUpEnabledWithConfig(waffoPancakeConfig)
	enableWaffo := complianceConfirmed && isWaffoTopUpEnabledWithConfig(waffoConfig)

	// 获取支付方式
	payMethods := paymentRuntimeConfig.PayMethods
	if !complianceConfirmed {
		payMethods = []map[string]string{}
	}

	// 如果启用了 Stripe 支付，添加到支付方法列表
	if isStripeTopUpEnabled() {
		// 检查是否已经包含 Stripe
		hasStripe := false
		for _, method := range payMethods {
			if method["type"] == "stripe" {
				hasStripe = true
				break
			}
		}

		if !hasStripe {
			stripeMethod := map[string]string{
				"name":      "Stripe",
				"type":      "stripe",
				"color":     "#635BFF",
				"min_topup": strconv.Itoa(stripeConfig.MinTopUp),
			}
			payMethods = append(payMethods, stripeMethod)
		}
	}

	// Waffo Pancake is displayed above the standard Waffo gateway.
	if enableWaffoPancake {
		hasWaffoPancake := false
		for _, method := range payMethods {
			if method["type"] == model.PaymentMethodWaffoPancake {
				hasWaffoPancake = true
				break
			}
		}

		if !hasWaffoPancake {
			payMethods = append(payMethods, map[string]string{
				"name":      "Waffo Pancake",
				"type":      model.PaymentMethodWaffoPancake,
				"color":     "#F97316",
				"min_topup": strconv.Itoa(waffoPancakeConfig.MinTopUp),
			})
		}
	}

	// 如果启用了 Waffo 支付，添加到支付方法列表
	if enableWaffo {
		hasWaffo := false
		for _, method := range payMethods {
			if method["type"] == model.PaymentMethodWaffo {
				hasWaffo = true
				break
			}
		}

		if !hasWaffo {
			waffoMethod := map[string]string{
				"name":      "Waffo (Global Payment)",
				"type":      model.PaymentMethodWaffo,
				"color":     "#3B82F6",
				"min_topup": strconv.Itoa(waffoConfig.MinTopUp),
			}
			payMethods = append(payMethods, waffoMethod)
		}
	}

	data := gin.H{
		"enable_online_topup":              isEpayTopUpEnabled(),
		"enable_stripe_topup":              isStripeTopUpEnabled(),
		"enable_creem_topup":               enableCreem,
		"enable_waffo_topup":               enableWaffo,
		"enable_waffo_pancake_topup":       enableWaffoPancake,
		"enable_redemption":                complianceConfirmed,
		"payment_compliance_confirmed":     complianceConfirmed,
		"payment_compliance_terms_version": operation_setting.CurrentComplianceTermsVersion,
		"waffo_pay_methods": func() interface{} {
			if enableWaffo {
				return setting.GetWaffoPayMethods()
			}
			return nil
		}(),
		"creem_products":          creemConfig.Products,
		"pay_methods":             payMethods,
		"min_topup":               paymentRuntimeConfig.MinTopUp,
		"stripe_min_topup":        stripeConfig.MinTopUp,
		"waffo_min_topup":         waffoConfig.MinTopUp,
		"waffo_pancake_min_topup": waffoPancakeConfig.MinTopUp,
		"amount_options":          paymentSetting.AmountOptions,
		"discount":                paymentSetting.AmountDiscount,
		"topup_link":              runtimeConfig.TopUpLink,
	}
	common.ApiSuccess(c, data)
}

type EpayRequest struct {
	Amount        int64  `json:"amount"`
	PaymentMethod string `json:"payment_method"`
}

type AmountRequest struct {
	Amount int64 `json:"amount"`
}

func GetEpayClient() *epay.Client {
	return getEpayClientWithConfig(operation_setting.GetPaymentRuntimeConfig())
}

func getEpayClientWithConfig(paymentConfig operation_setting.PaymentRuntimeConfig) *epay.Client {
	if paymentConfig.PayAddress == "" || paymentConfig.EpayID == "" || paymentConfig.EpayKey == "" {
		return nil
	}
	withUrl, err := epay.NewClient(&epay.Config{
		PartnerID: paymentConfig.EpayID,
		Key:       paymentConfig.EpayKey,
	}, paymentConfig.PayAddress)
	if err != nil {
		return nil
	}
	return withUrl
}

func getPayMoney(amount int64, group string) float64 {
	return getPayMoneyWithConfig(
		amount,
		group,
		operation_setting.GetPaymentRuntimeConfig(),
		operation_setting.GetPaymentSettingSnapshot(),
		operation_setting.GetGeneralSettingSnapshot(),
	)
}

func getPayMoneyWithConfig(amount int64, group string, paymentConfig operation_setting.PaymentRuntimeConfig, paymentSetting operation_setting.PaymentSetting, generalSetting operation_setting.GeneralSetting) float64 {
	dAmount := decimal.NewFromInt(amount)
	quotaPerUnit := common.GetQuotaPerUnit()
	// 充值金额以“展示类型”为准：
	// - USD/CNY: 前端传 amount 为金额单位；TOKENS: 前端传 tokens，需要换成 USD 金额
	if generalSetting.QuotaDisplayType == operation_setting.QuotaDisplayTypeTokens {
		dQuotaPerUnit := decimal.NewFromFloat(quotaPerUnit)
		dAmount = dAmount.Div(dQuotaPerUnit)
	}

	topupGroupRatio := common.GetTopupGroupRatio(group)
	if topupGroupRatio == 0 {
		topupGroupRatio = 1
	}

	dTopupGroupRatio := decimal.NewFromFloat(topupGroupRatio)
	dPrice := decimal.NewFromFloat(paymentConfig.Price)
	// apply optional preset discount by the original request amount (if configured), default 1.0
	discount := 1.0
	if ds, ok := paymentSetting.AmountDiscount[int(amount)]; ok {
		if ds > 0 {
			discount = ds
		}
	}
	dDiscount := decimal.NewFromFloat(discount)

	payMoney := dAmount.Mul(dPrice).Mul(dTopupGroupRatio).Mul(dDiscount)

	return payMoney.InexactFloat64()
}

func getMinTopup() int64 {
	return getMinTopupWithConfig(operation_setting.GetPaymentRuntimeConfig(), operation_setting.GetGeneralSettingSnapshot())
}

func getMinTopupWithConfig(paymentConfig operation_setting.PaymentRuntimeConfig, generalSetting operation_setting.GeneralSetting) int64 {
	minTopup := paymentConfig.MinTopUp
	if generalSetting.QuotaDisplayType == operation_setting.QuotaDisplayTypeTokens {
		dMinTopup := decimal.NewFromInt(int64(minTopup))
		dQuotaPerUnit := decimal.NewFromFloat(common.GetQuotaPerUnit())
		quota, err := common.WalletQuotaFromDecimalStrict(dMinTopup.Mul(dQuotaPerUnit))
		if err != nil {
			return common.MaxWalletQuota
		}
		minTopup = quota
	}
	return int64(minTopup)
}

func getTopUpQuota(amount int64) (int, error) {
	return getTopUpQuotaWithDisplayType(amount, operation_setting.GetGeneralSettingSnapshot().QuotaDisplayType)
}

func validateTopUpQuotaWithDisplayType(amount int64, displayType string) (int, error) {
	quota, err := getTopUpQuotaWithDisplayType(amount, displayType)
	if err == nil && quota > 0 {
		return quota, nil
	}
	maxAmount := getMaxTopUpAmountWithDisplayType(displayType)
	if maxAmount > 0 && amount > maxAmount {
		return 0, fmt.Errorf("单笔充值数量不能大于 %d", maxAmount)
	}
	return 0, errors.New("充值数量无效")
}

func getTopUpQuotaWithDisplayType(amount int64, displayType string) (int, error) {
	quota := decimal.NewFromInt(amount)
	quotaPerUnit := common.GetQuotaPerUnit()
	if displayType == operation_setting.QuotaDisplayTypeTokens {
		dQuotaPerUnit := decimal.NewFromFloat(quotaPerUnit)
		quota = decimal.NewFromInt(quota.Div(dQuotaPerUnit).IntPart()).Mul(dQuotaPerUnit)
	} else {
		quota = quota.Mul(decimal.NewFromFloat(quotaPerUnit))
	}
	return common.WalletQuotaFromDecimalStrict(quota)
}

func getMaxTopUpAmount() int64 {
	return getMaxTopUpAmountWithDisplayType(operation_setting.GetGeneralSettingSnapshot().QuotaDisplayType)
}

func getMaxTopUpAmountWithDisplayType(displayType string) int64 {
	quotaPerUnit := common.GetQuotaPerUnit()
	if quotaPerUnit <= 0 {
		return 0
	}
	dQuotaPerUnit := decimal.NewFromFloat(quotaPerUnit)
	maxStoredAmount := decimal.NewFromInt(common.MaxWalletQuota).
		Div(dQuotaPerUnit).
		Floor()
	if displayType == operation_setting.QuotaDisplayTypeTokens {
		return maxStoredAmount.Add(decimal.NewFromInt(1)).
			Mul(dQuotaPerUnit).
			Ceil().
			Sub(decimal.NewFromInt(1)).
			IntPart()
	}
	return maxStoredAmount.IntPart()
}

func validateCreditedQuota(quota decimal.Decimal) (int, error) {
	value, err := common.WalletQuotaFromDecimalStrict(quota)
	if err != nil {
		return 0, errors.New("充值额度超出系统可表示范围")
	}
	if value <= 0 {
		return 0, errors.New("充值额度必须大于 0")
	}
	return value, nil
}

func validateTopUpQuota(amount int64) (int, error) {
	return validateTopUpQuotaWithDisplayType(amount, operation_setting.GetGeneralSettingSnapshot().QuotaDisplayType)
}

func containsPayMethod(methods []map[string]string, method string) bool {
	for _, payMethod := range methods {
		if payMethod != nil && payMethod["type"] == method {
			return true
		}
	}
	return false
}

func rejectInvalidCreditedQuota(c *gin.Context, userId int, quota decimal.Decimal) bool {
	creditedQuota, err := validateCreditedQuota(quota)
	if err == nil {
		err = model.ValidateTopUpQuotaCapacity(userId, creditedQuota)
	}
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": err.Error()})
		return true
	}
	return false
}

func rejectInvalidTopUpQuota(c *gin.Context, userId int, amount int64) bool {
	creditedQuota, err := validateTopUpQuota(amount)
	if err == nil {
		err = model.ValidateTopUpQuotaCapacity(userId, creditedQuota)
	}
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": err.Error()})
		return true
	}
	return false
}

func RequestEpay(c *gin.Context) {
	paymentConfig := operation_setting.GetPaymentRuntimeConfig()
	paymentSetting := operation_setting.GetPaymentSettingSnapshot()
	generalSetting := operation_setting.GetGeneralSettingSnapshot()
	if !isEpayCheckoutEnabledWithConfig(paymentConfig, paymentSetting) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "易支付暂不可用"})
		return
	}
	var req EpayRequest
	err := c.ShouldBindJSON(&req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	minTopup := getMinTopupWithConfig(paymentConfig, generalSetting)
	if req.Amount < minTopup {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", minTopup)})
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
	payMoney := getPayMoneyWithConfig(req.Amount, group, paymentConfig, paymentSetting, generalSetting)
	if payMoney < 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}

	if !containsPayMethod(paymentConfig.PayMethods, req.PaymentMethod) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付方式不存在"})
		return
	}

	callBackAddress := service.GetCallbackAddress()
	returnUrl, _ := url.Parse(paymentReturnPath("/usage-logs"))
	notifyUrl, _ := url.Parse(callBackAddress + "/api/user/epay/notify")
	client := getEpayClientWithConfig(paymentConfig)
	if client == nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "当前管理员未配置支付信息"})
		return
	}
	amount := req.Amount
	if generalSetting.QuotaDisplayType == operation_setting.QuotaDisplayTypeTokens {
		dAmount := decimal.NewFromInt(amount)
		dQuotaPerUnit := decimal.NewFromFloat(common.GetQuotaPerUnit())
		amount = dAmount.Div(dQuotaPerUnit).IntPart()
	}
	tradeNo := fmt.Sprintf("%s%d", common.GetRandomString(6), time.Now().Unix())
	tradeNo = fmt.Sprintf("USR%dNO%s", id, tradeNo)
	providerName := fmt.Sprintf("TUC%d", req.Amount)
	providerAmount := strconv.FormatFloat(payMoney, 'f', 2, 64)
	// Persist the pending order and all settlement inputs before contacting the
	// provider. This closes the paid-before-insert race and freezes pricing.
	topUp := &model.TopUp{
		UserId:                 id,
		Amount:                 amount,
		Money:                  payMoney,
		TradeNo:                tradeNo,
		PaymentMethod:          req.PaymentMethod,
		PaymentProvider:        model.PaymentProviderEpay,
		CreateTime:             time.Now().Unix(),
		Status:                 common.TopUpStatusPending,
		CreditedQuota:          creditedQuota,
		ProviderMerchantID:     paymentConfig.EpayID,
		ProviderOrderName:      providerName,
		ProviderAmount:         providerAmount,
		ProviderKeyFingerprint: model.EpayKeyFingerprint(paymentConfig.EpayKey),
	}
	if err := topUp.Insert(); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("易支付 创建充值订单失败 user_id=%d trade_no=%s payment_method=%s amount=%d error=%q", id, tradeNo, req.PaymentMethod, req.Amount, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	uri, params, err := client.Purchase(&epay.PurchaseArgs{
		Type:           req.PaymentMethod,
		ServiceTradeNo: tradeNo,
		Name:           providerName,
		Money:          providerAmount,
		Device:         epay.PC,
		NotifyUrl:      notifyUrl,
		ReturnUrl:      returnUrl,
	})
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("易支付 拉起支付失败 user_id=%d trade_no=%s payment_method=%s amount=%d error=%q", id, tradeNo, req.PaymentMethod, req.Amount, err.Error()))
		if statusErr := model.UpdatePendingTopUpStatus(tradeNo, model.PaymentProviderEpay, common.TopUpStatusFailed); statusErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("易支付充值订单失败状态补偿失败 user_id=%d trade_no=%s provider=%s error=%q", id, tradeNo, model.PaymentProviderEpay, statusErr.Error()))
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	// Purchase params contain the EPay signature. Never put the signed query
	// string (or the provider URL) into application logs; the trade number and
	// local amount are sufficient for operational correlation.
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("易支付 充值订单创建成功 user_id=%d trade_no=%s payment_method=%s amount=%d money=%.2f", id, tradeNo, req.PaymentMethod, req.Amount, payMoney))
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": params, "url": uri})
}

// tradeNo lock
var orderLocks sync.Map
var createLock sync.Mutex

// refCountedMutex 带引用计数的互斥锁，确保最后一个使用者才从 map 中删除
type refCountedMutex struct {
	mu       sync.Mutex
	refCount int
}

// LockOrder 尝试对给定订单号加锁
func LockOrder(tradeNo string) {
	createLock.Lock()
	var rcm *refCountedMutex
	if v, ok := orderLocks.Load(tradeNo); ok {
		rcm = v.(*refCountedMutex)
	} else {
		rcm = &refCountedMutex{}
		orderLocks.Store(tradeNo, rcm)
	}
	rcm.refCount++
	createLock.Unlock()
	rcm.mu.Lock()
}

// UnlockOrder 释放给定订单号的锁
func UnlockOrder(tradeNo string) {
	v, ok := orderLocks.Load(tradeNo)
	if !ok {
		return
	}
	rcm := v.(*refCountedMutex)
	rcm.mu.Unlock()

	createLock.Lock()
	rcm.refCount--
	if rcm.refCount == 0 {
		orderLocks.Delete(tradeNo)
	}
	createLock.Unlock()
}

func EpayNotify(c *gin.Context) {
	if c.Request.Method == http.MethodGet && !isEpayGetWebhookEnabled() {
		// A signed GET carries payment fields in the URL.  Refuse it by default
		// so a browser/proxy cannot turn the notification endpoint into a
		// credential-bearing URL.  Operators with a legacy provider must opt in
		// explicitly via EPAY_NOTIFY_GET_ENABLED and should scrub query strings at
		// the reverse proxy before doing so.
		c.Header("Allow", http.MethodPost)
		c.AbortWithStatus(http.StatusMethodNotAllowed)
		return
	}
	if !isEpayWebhookEnabled() {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("易支付 webhook 被拒绝 reason=webhook_disabled path=%q client_ip=%s", c.Request.URL.Path, c.ClientIP()))
		if _, writeErr := c.Writer.Write([]byte("fail")); writeErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("易支付 webhook 响应写入失败 path=%q client_ip=%s error=%q", c.Request.URL.Path, c.ClientIP(), writeErr.Error()))
		}
		return
	}
	callback, verifyInfo, err := verifyEpayCallback(c)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("易支付 webhook 拒绝 path=%q client_ip=%s error=%q", c.Request.URL.Path, c.ClientIP(), err.Error()))
		writeEpayFailure(c)
		return
	}
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("易支付 webhook 验签成功 trade_no=%s callback_type=%s trade_status=%s client_ip=%s", callback.ServiceTradeNo, callback.PaymentMethod, callback.TradeStatus, c.ClientIP()))

	if verifyInfo.TradeStatus == epay.StatusTradeSuccess {
		// 进程内锁只是优化；重复/并发回调的正确性由 RechargeEpay 的
		// 数据库行锁 + 事务内状态校验保证（多实例部署下同样安全）。
		LockOrder(verifyInfo.ServiceTradeNo)
		defer UnlockOrder(verifyInfo.ServiceTradeNo)
		outcome, err := model.RechargeEpayVerifiedWithOutcome(callback, c.ClientIP())
		if err != nil {
			switch {
			case errors.Is(err, model.ErrTopUpNotFound):
				logger.LogWarn(c.Request.Context(), fmt.Sprintf("易支付 回调订单不存在 trade_no=%s callback_type=%s provider_trade_no=%s client_ip=%s", verifyInfo.ServiceTradeNo, verifyInfo.Type, verifyInfo.TradeNo, c.ClientIP()))
			case errors.Is(err, model.ErrPaymentMethodMismatch):
				logger.LogWarn(c.Request.Context(), fmt.Sprintf("易支付 订单支付网关不匹配 trade_no=%s callback_type=%s client_ip=%s", verifyInfo.ServiceTradeNo, verifyInfo.Type, c.ClientIP()))
			case errors.Is(err, model.ErrTopUpStatusInvalid):
				logger.LogWarn(c.Request.Context(), fmt.Sprintf("易支付 订单状态非法 trade_no=%s callback_type=%s client_ip=%s", verifyInfo.ServiceTradeNo, verifyInfo.Type, c.ClientIP()))
			case errors.Is(err, model.ErrEpayOrderSnapshotMissing):
				logger.LogWarn(c.Request.Context(), fmt.Sprintf("易支付 订单缺少不可变快照，转人工对账 trade_no=%s callback_type=%s client_ip=%s", verifyInfo.ServiceTradeNo, verifyInfo.Type, c.ClientIP()))
			default:
				logger.LogError(c.Request.Context(), fmt.Sprintf("易支付 充值处理失败 trade_no=%s client_ip=%s error=%q", verifyInfo.ServiceTradeNo, c.ClientIP(), err.Error()))
			}
			if _, writeErr := c.Writer.Write([]byte("fail")); writeErr != nil {
				logger.LogError(c.Request.Context(), fmt.Sprintf("易支付 webhook 响应写入失败 trade_no=%s client_ip=%s error=%q", verifyInfo.ServiceTradeNo, c.ClientIP(), writeErr.Error()))
			}
			return
		}
		if outcome == model.EpaySettlementLegacySuccessAcknowledged {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("易支付 allowlist 历史成功单仅确认回调，未执行入账 trade_no=%s callback_type=%s client_ip=%s", verifyInfo.ServiceTradeNo, verifyInfo.Type, c.ClientIP()))
		} else if outcome == model.EpaySettlementPaidUncredited {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("易支付已付款但充值额度未入账，订单进入 paid_uncredited trade_no=%s callback_type=%s client_ip=%s", verifyInfo.ServiceTradeNo, verifyInfo.Type, c.ClientIP()))
		} else if outcome == model.EpaySettlementAlreadyCompleted {
			logger.LogInfo(c.Request.Context(), fmt.Sprintf("易支付 重复回调幂等忽略 trade_no=%s callback_type=%s client_ip=%s", verifyInfo.ServiceTradeNo, verifyInfo.Type, c.ClientIP()))
		} else {
			logger.LogInfo(c.Request.Context(), fmt.Sprintf("易支付 充值成功 trade_no=%s callback_type=%s client_ip=%s", verifyInfo.ServiceTradeNo, verifyInfo.Type, c.ClientIP()))
		}
	} else {
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("易支付 webhook 忽略事件 trade_no=%s callback_type=%s provider_trade_no=%s trade_status=%s client_ip=%s", verifyInfo.ServiceTradeNo, verifyInfo.Type, verifyInfo.TradeNo, verifyInfo.TradeStatus, c.ClientIP()))
	}
	if _, writeErr := c.Writer.Write([]byte("success")); writeErr != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("易支付 webhook 响应写入失败 trade_no=%s client_ip=%s error=%q", verifyInfo.ServiceTradeNo, c.ClientIP(), writeErr.Error()))
	}
}

func RequestAmount(c *gin.Context) {
	paymentConfig := operation_setting.GetPaymentRuntimeConfig()
	paymentSetting := operation_setting.GetPaymentSettingSnapshot()
	generalSetting := operation_setting.GetGeneralSettingSnapshot()
	var req AmountRequest
	err := c.ShouldBindJSON(&req)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}

	minTopup := getMinTopupWithConfig(paymentConfig, generalSetting)
	if req.Amount < minTopup {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", minTopup)})
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
	payMoney := getPayMoneyWithConfig(req.Amount, group, paymentConfig, paymentSetting, generalSetting)
	if payMoney <= 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": strconv.FormatFloat(payMoney, 'f', 2, 64)})
}

func GetUserTopUps(c *gin.Context) {
	userId := c.GetInt("id")
	pageInfo := common.GetPageQuery(c)
	keyword := c.Query("keyword")

	var (
		topups []*model.TopUp
		total  int64
		err    error
	)
	if keyword != "" {
		topups, total, err = model.SearchUserTopUps(userId, keyword, pageInfo)
	} else {
		topups, total, err = model.GetUserTopUps(userId, pageInfo)
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}

	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(topups)
	common.ApiSuccess(c, pageInfo)
}

// GetAllTopUps 管理员获取全平台充值记录
func GetAllTopUps(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	keyword := c.Query("keyword")

	var (
		topups []*model.TopUp
		total  int64
		err    error
	)
	if keyword != "" {
		topups, total, err = model.SearchAllTopUpsForRole(keyword, pageInfo, c.GetInt("role"))
	} else {
		topups, total, err = model.GetAllTopUpsForRole(pageInfo, c.GetInt("role"))
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}

	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(topups)
	common.ApiSuccess(c, pageInfo)
}

type AdminCompleteTopupRequest struct {
	TradeNo string `json:"trade_no"`
}

// AdminCompleteTopUp 管理员补单接口
func AdminCompleteTopUp(c *gin.Context) {
	var req AdminCompleteTopupRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.TradeNo == "" {
		common.ApiErrorMsg(c, "参数错误")
		return
	}

	// 订单级互斥，防止并发补单
	LockOrder(req.TradeNo)
	defer UnlockOrder(req.TradeNo)
	topUp, err := model.GetTopUpByTradeNoWithError(req.TradeNo)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !requireManageableUser(c, topUp.UserId) {
		return
	}

	if err := model.ManualCompleteTopUp(req.TradeNo, c.ClientIP()); err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, nil)
}

// AdminRetryPaidUncreditedTopUp retries only the wallet side of a provider
// payment whose evidence is already durable.  It intentionally shares the
// model-layer order lock with webhook settlement and checks the target user's
// role before allowing a financial mutation.
func AdminRetryPaidUncreditedTopUp(c *gin.Context) {
	tradeNo := strings.TrimSpace(c.Param("trade_no"))
	if tradeNo == "" {
		common.ApiErrorMsg(c, "无效的订单号")
		return
	}

	LockOrder(tradeNo)
	defer UnlockOrder(tradeNo)

	topUp, err := model.GetTopUpByTradeNoWithError(tradeNo)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !requireManageableUser(c, topUp.UserId) {
		return
	}

	if err := model.RetryPaidUncreditedTopUp(tradeNo); err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "topup.paid_uncredited_retry", map[string]interface{}{
		"trade_no": tradeNo,
		"provider": topUp.PaymentProvider,
	})
	common.ApiSuccess(c, nil)
}

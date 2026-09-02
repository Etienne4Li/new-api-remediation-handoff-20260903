package controller

import (
	"errors"
	"fmt"
	"net/http"
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
)

type WaffoPancakePayRequest struct {
	Amount int64 `json:"amount"`
}

func RequestWaffoPancakeAmount(c *gin.Context) {
	waffoPancakeConfig := setting.GetWaffoPancakeConfig()
	paymentSetting := operation_setting.GetPaymentSettingSnapshot()
	generalSetting := operation_setting.GetGeneralSettingSnapshot()
	var req WaffoPancakePayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}

	if req.Amount < int64(waffoPancakeConfig.MinTopUp) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", waffoPancakeConfig.MinTopUp)})
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

	payMoney := getWaffoPancakePayMoneyWithConfigAndPayment(req.Amount, group, waffoPancakeConfig, paymentSetting, generalSetting)
	if payMoney <= 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "success", "data": fmt.Sprintf("%.2f", payMoney)})
}

func getWaffoPancakePayMoney(amount int64, group string) float64 {
	return getWaffoPancakePayMoneyWithConfigAndPayment(amount, group, setting.GetWaffoPancakeConfig(), operation_setting.GetPaymentSettingSnapshot(), operation_setting.GetGeneralSettingSnapshot())
}

func getWaffoPancakePayMoneyWithConfig(amount int64, group string, waffoPancakeConfig setting.WaffoPancakeConfig) float64 {
	return getWaffoPancakePayMoneyWithConfigAndPayment(amount, group, waffoPancakeConfig, operation_setting.GetPaymentSettingSnapshot(), operation_setting.GetGeneralSettingSnapshot())
}

func getWaffoPancakePayMoneyWithConfigAndPayment(amount int64, group string, waffoPancakeConfig setting.WaffoPancakeConfig, paymentSetting operation_setting.PaymentSetting, generalSetting operation_setting.GeneralSetting) float64 {
	dAmount := decimal.NewFromInt(amount)
	if generalSetting.QuotaDisplayType == operation_setting.QuotaDisplayTypeTokens {
		dAmount = dAmount.Div(decimal.NewFromFloat(common.GetQuotaPerUnit()))
	}

	topupGroupRatio := common.GetTopupGroupRatio(group)
	if topupGroupRatio == 0 {
		topupGroupRatio = 1
	}

	discount := 1.0
	if ds, ok := paymentSetting.AmountDiscount[int(amount)]; ok && ds > 0 {
		discount = ds
	}

	payMoney := dAmount.
		Mul(decimal.NewFromFloat(waffoPancakeConfig.UnitPrice)).
		Mul(decimal.NewFromFloat(topupGroupRatio)).
		Mul(decimal.NewFromFloat(discount))

	return payMoney.InexactFloat64()
}

func normalizeWaffoPancakeTopUpAmount(amount int64) int64 {
	if operation_setting.GetGeneralSettingSnapshot().QuotaDisplayType != operation_setting.QuotaDisplayTypeTokens {
		return amount
	}

	normalized := decimal.NewFromInt(amount).
		Div(decimal.NewFromFloat(common.GetQuotaPerUnit())).
		IntPart()
	if normalized < 1 {
		return 1
	}
	return normalized
}

func formatWaffoPancakeAmount(payMoney float64) string {
	return formatWaffoPancakeAmountForCurrency(payMoney, "USD")
}

func formatWaffoPancakeAmountForCurrency(payMoney float64, currency string) string {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if zeroDecimalCurrencies[currency] {
		return decimal.NewFromFloat(payMoney).StringFixed(0)
	}
	return decimal.NewFromFloat(payMoney).StringFixed(2)
}

func getWaffoPancakeBuyerEmail(user *model.User) string {
	if user != nil && strings.TrimSpace(user.Email) != "" {
		return user.Email
	}
	return ""
}

// The admin config endpoints below accept typed-but-not-yet-saved creds in
// the body and fall back to persisted creds when the body is blank (see
// resolveWaffoPancakeAdminCreds). Only SaveWaffoPancake writes to OptionMap.

type saveWaffoPancakeRequest struct {
	MerchantID string `json:"merchant_id"`
	PrivateKey string `json:"private_key"`
	ReturnURL  string `json:"return_url"`
	StoreID    string `json:"store_id"`
	ProductID  string `json:"product_id"`
}

type createWaffoPancakePairRequest struct {
	MerchantID string `json:"merchant_id"`
	PrivateKey string `json:"private_key"`
	ReturnURL  string `json:"return_url"`
}

type listWaffoPancakeCatalogRequest struct {
	MerchantID string `json:"merchant_id"`
	PrivateKey string `json:"private_key"`
}

// SaveWaffoPancake atomically persists all five operator-controlled fields.
// Catalog / pair endpoints are transient — only this one writes the OptionMap.
func SaveWaffoPancake(c *gin.Context) {
	var req saveWaffoPancakeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	if err := service.SaveWaffoPancakeConfig(
		c.Request.Context(),
		req.MerchantID,
		req.PrivateKey,
		req.ReturnURL,
		req.StoreID,
		req.ProductID,
	); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake 保存配置失败 store_id=%q product_id=%q error=%q",
			req.StoreID, req.ProductID, err.Error(),
		))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "保存配置失败"})
		return
	}
	waffoPancakeConfig := setting.GetWaffoPancakeConfig()
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"product_id": waffoPancakeConfig.ProductID,
			"store_id":   waffoPancakeConfig.StoreID,
		},
	})
}

// resolveWaffoPancakeAdminCreds prefers body creds (typed-but-not-yet-saved
// values, for verification) and falls back to persisted creds when the body
// is blank (so returning admins don't have to re-paste the private key,
// which is stripped from GET /api/option/).
func resolveWaffoPancakeAdminCreds(bodyMerchantID, bodyPrivateKey string) (string, string) {
	m := strings.TrimSpace(bodyMerchantID)
	k := strings.TrimSpace(bodyPrivateKey)
	if m == "" && k == "" {
		cfg := setting.GetWaffoPancakeConfig()
		return cfg.MerchantID, cfg.PrivateKey
	}
	return m, k
}

// CreateWaffoPancakePair mints a Store + OnetimeProduct pair in one round-
// trip. Surfaces an orphan-store flag when the product half fails so the
// frontend can preselect / retry without losing context.
func CreateWaffoPancakePair(c *gin.Context) {
	var req createWaffoPancakePairRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
			return
		}
	}
	merchantID, privateKey := resolveWaffoPancakeAdminCreds(req.MerchantID, req.PrivateKey)
	if merchantID == "" || privateKey == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo Pancake 凭证未配置"})
		return
	}
	result, err := service.CreateWaffoPancakePrimaryPair(
		c.Request.Context(), merchantID, privateKey, req.ReturnURL,
	)
	if err != nil {
		orphan := result != nil && result.OrphanStore
		logger.LogError(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake 创建店铺与产品失败 orphan_store=%t store_id=%q error=%q",
			orphan, func() string {
				if result == nil {
					return ""
				}
				return result.StoreID
			}(), err.Error(),
		))
		data := gin.H{"error": err.Error()}
		if orphan {
			data["store_id"] = result.StoreID
			data["store_name"] = result.StoreName
			data["orphan_store"] = true
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": data})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"store_id":     result.StoreID,
			"store_name":   result.StoreName,
			"product_id":   result.ProductID,
			"product_name": result.ProductName,
		},
	})
}

// ListWaffoPancakeCatalog returns the merchant's Stores + OnetimeProducts.
// Doubles as a credential probe (a successful 200 proves the resolved creds
// authenticate). See resolveWaffoPancakeAdminCreds for credential resolution.
func ListWaffoPancakeCatalog(c *gin.Context) {
	var req listWaffoPancakeCatalogRequest
	if c.Request.Method == http.MethodPost && c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
			return
		}
	}
	// Credentials are accepted only in a POST body. Never read them from a
	// query string: URLs are routinely copied into access logs, browser history,
	// tracing spans, and referrer headers. GET remains available for a returning
	// admin who wants to inspect the catalog using the persisted credentials.
	merchantID, privateKey := resolveWaffoPancakeAdminCreds(req.MerchantID, req.PrivateKey)
	if merchantID == "" || privateKey == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo Pancake 凭证未配置"})
		return
	}
	catalog, err := service.ListWaffoPancakeCatalog(c.Request.Context(), merchantID, privateKey)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake 拉取店铺与产品目录失败 error=%q", err.Error(),
		))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉取目录失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": catalog})
}

type createWaffoPancakeSubscriptionProductRequest struct {
	Name   string `json:"name"`
	Amount string `json:"amount"`
}

// CreateWaffoPancakeSubscriptionProduct mints a one-time Pancake product
// (never a recurring SubscriptionProduct) sized to a plan's `name` + `amount`.
// The historical endpoint name is retained for clients, but the response
// includes product_type=onetime and the product is safe only for a single
// local entitlement period. Reads from the form, not the plan row, so
// newly-typed unsaved plans can mint a product too.
func CreateWaffoPancakeSubscriptionProduct(c *gin.Context) {
	var req createWaffoPancakeSubscriptionProductRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
			return
		}
	}
	if strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "套餐名称不能为空"})
		return
	}
	if strings.TrimSpace(req.Amount) == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "套餐价格不能为空"})
		return
	}
	merchantID, privateKey := resolveWaffoPancakeAdminCreds("", "")
	waffoPancakeConfig := setting.GetWaffoPancakeConfig()
	storeID := strings.TrimSpace(waffoPancakeConfig.StoreID)
	if merchantID == "" || privateKey == "" || storeID == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo Pancake 未完成配置，请先在支付设置中完成网关绑定"})
		return
	}
	productID, err := service.CreateWaffoPancakeOneTimeProductForPlan(
		c.Request.Context(),
		merchantID,
		privateKey,
		storeID,
		req.Name,
		req.Amount,
		waffoPancakeConfig.ReturnURL,
	)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake 创建套餐产品失败 store_id=%q name=%q amount=%q error=%q",
			storeID, req.Name, req.Amount, err.Error(),
		))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建套餐产品失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"product_id":   productID,
			"product_name": req.Name,
			"product_type": service.WaffoPancakeProductTypeOneTime,
			"store_id":     storeID,
		},
	})
}

// ListWaffoPancakeSubscriptionProductOptions returns only active one-time
// products in the saved Pancake store. The historical endpoint name reflects
// new-api's plan concept; under the hood no recurring product is exposed.
func ListWaffoPancakeSubscriptionProductOptions(c *gin.Context) {
	merchantID, privateKey := resolveWaffoPancakeAdminCreds("", "")
	storeID := strings.TrimSpace(setting.GetWaffoPancakeConfig().StoreID)
	if merchantID == "" || privateKey == "" || storeID == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo Pancake 未完成配置，请先在支付设置中完成网关绑定"})
		return
	}
	catalog, err := service.ListWaffoPancakeCatalog(c.Request.Context(), merchantID, privateKey)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake 拉取订阅产品列表失败 store_id=%q error=%q", storeID, err.Error(),
		))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉取产品列表失败"})
		return
	}
	products := []service.WaffoPancakeCatalogProduct{}
	for _, store := range catalog.Stores {
		if store.ID == storeID {
			products = store.OnetimeProducts
			break
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"store_id": storeID,
			"products": products,
		},
	})
}

func getWaffoPancakeBuyerIdentity(user *model.User) string {
	if user == nil {
		return ""
	}
	return service.WaffoPancakeBuyerIdentityFromUserID(user.Id)
}

func RequestWaffoPancakePay(c *gin.Context) {
	waffoPancakeConfig := setting.GetWaffoPancakeConfig()
	generalSetting := operation_setting.GetGeneralSettingSnapshot()
	paymentSetting := operation_setting.GetPaymentSettingSnapshot()
	if !paymentSetting.ComplianceConfirmed || paymentSetting.ComplianceTermsVersion != operation_setting.CurrentComplianceTermsVersion || !isWaffoPancakeTopUpEnabledWithConfig(waffoPancakeConfig) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo Pancake 配置不完整"})
		return
	}
	merchantID := strings.TrimSpace(waffoPancakeConfig.MerchantID)
	storeID := strings.TrimSpace(waffoPancakeConfig.StoreID)
	productID := strings.TrimSpace(waffoPancakeConfig.ProductID)
	if merchantID == "" || strings.TrimSpace(waffoPancakeConfig.PrivateKey) == "" || storeID == "" || productID == "" {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo Pancake 配置不完整"})
		return
	}
	// Re-check the provider-side catalog at payment time. A product can be
	// deactivated or replaced after the admin saved the configuration, and a
	// local metadata marker cannot prove that an arbitrary PROD_* ID is a
	// one-time product.
	if err := service.ValidateWaffoPancakeBinding(c.Request.Context(), merchantID, waffoPancakeConfig.PrivateKey, storeID, productID); err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo Pancake 充值商品绑定校验失败 store_id=%q product_id=%q error=%q", storeID, productID, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "Waffo Pancake 商品配置无效或已下架"})
		return
	}

	var req WaffoPancakePayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	if req.Amount < int64(waffoPancakeConfig.MinTopUp) {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", waffoPancakeConfig.MinTopUp)})
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

	group, err := model.GetUserGroup(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}

	payMoney := getWaffoPancakePayMoneyWithConfigAndPayment(req.Amount, group, waffoPancakeConfig, paymentSetting, generalSetting)
	if payMoney < 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}
	providerAmount := formatWaffoPancakeAmount(payMoney)
	providerCurrency := "USD"
	providerOrderName := service.WaffoPancakePrimaryProductName()
	// Checkout requests are always sent to the production Pancake endpoint.
	// Freeze the account/environment scope on the local order so a later
	// webhook cannot settle it after credentials are switched to another
	// merchant or test mode.
	providerEnvironment := waffoPancakeProviderEnvironment("prod")
	providerScopeFingerprint := waffoPancakeProviderScopeFingerprint(merchantID, providerEnvironment)
	if providerScopeFingerprint == "" {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 创建充值订单失败 user_id=%d reason=provider_scope_invalid", id))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "支付配置错误"})
		return
	}

	tradeNo := fmt.Sprintf("WAFFO_PANCAKE-%d-%d-%s", id, time.Now().UnixMilli(), randstr.String(6))
	topUp := &model.TopUp{
		UserId:                 id,
		Amount:                 normalizeWaffoPancakeTopUpAmount(req.Amount),
		Money:                  payMoney,
		TradeNo:                tradeNo,
		PaymentMethod:          model.PaymentMethodWaffoPancake,
		PaymentProvider:        model.PaymentProviderWaffoPancake,
		CreateTime:             time.Now().Unix(),
		Status:                 common.TopUpStatusPending,
		CreditedQuota:          creditedQuota,
		ProviderMerchantID:     merchantID,
		ProviderOrderName:      providerOrderName,
		ProviderAmount:         providerAmount,
		ProviderProductID:      productID,
		ProviderStoreID:        storeID,
		ProviderCurrency:       providerCurrency,
		ProviderKeyFingerprint: providerScopeFingerprint,
	}
	if err := topUp.Insert(); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 创建充值订单失败 user_id=%d trade_no=%s amount=%d error=%q", id, tradeNo, req.Amount, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	markTopUpFailed := func(reason string) {
		if statusErr := model.UpdatePendingTopUpStatus(tradeNo, model.PaymentProviderWaffoPancake, common.TopUpStatusFailed); statusErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 充值订单失败状态补偿失败 user_id=%d trade_no=%s provider=%s reason=%s error=%q", id, tradeNo, model.PaymentProviderWaffoPancake, reason, statusErr.Error()))
		}
	}

	expiresInSeconds := 45 * 60
	session, err := service.CreateWaffoPancakeCheckoutSessionWithConfig(c.Request.Context(), &service.WaffoPancakeCreateSessionParams{
		ProductID:     productID,
		ProductType:   service.WaffoPancakeProductTypeOneTime,
		Currency:      providerCurrency,
		BuyerIdentity: getWaffoPancakeBuyerIdentity(user),
		PriceSnapshot: &service.WaffoPancakePriceSnapshot{
			Amount:      providerAmount,
			TaxCategory: "saas",
		},
		BuyerEmail:              getWaffoPancakeBuyerEmail(user),
		ExpiresInSeconds:        &expiresInSeconds,
		OrderMerchantExternalID: tradeNo,
		Metadata: newWaffoPancakeOrderMetadata(
			tradeNo, "topup", productID, merchantID, storeID,
			providerCurrency, providerAmount, providerOrderName, user.Id,
		),
	}, waffoPancakeConfig)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 创建结账会话失败 user_id=%d trade_no=%s error=%q", id, tradeNo, err.Error()))
		markTopUpFailed("provider_request")
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	if session == nil || strings.TrimSpace(session.SessionID) == "" || strings.TrimSpace(session.CheckoutURL) == "" {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 返回空结账会话 user_id=%d trade_no=%s", id, tradeNo))
		markTopUpFailed("response_validation")
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	updateResult := model.DB.Model(&model.TopUp{}).
		Where("id = ? AND trade_no = ? AND status = ?", topUp.Id, tradeNo, common.TopUpStatusPending).
		Update("provider_checkout_id", strings.TrimSpace(session.SessionID))
	if updateResult.Error != nil || updateResult.RowsAffected != 1 {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 保存结账会话失败 user_id=%d trade_no=%s", id, tradeNo))
		if updateResult.Error == nil {
			markTopUpFailed("checkout_id_persistence")
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建支付订单失败"})
		return
	}
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo Pancake 充值订单创建成功 user_id=%d trade_no=%s session_id=%s amount=%d money=%.2f", id, tradeNo, session.SessionID, req.Amount, payMoney))

	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"checkout_url":     session.CheckoutURL,
			"session_id":       session.SessionID,
			"expires_at":       session.ExpiresAt,
			"order_id":         tradeNo,
			"token":            session.Token,
			"token_expires_at": session.TokenExpiresAt,
		},
	})
}

func WaffoPancakeWebhook(c *gin.Context) {
	waffoPancakeConfig := setting.GetWaffoPancakeConfig()
	if !isWaffoPancakeWebhookEnabledWithConfig(waffoPancakeConfig) {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 被拒绝 reason=webhook_disabled path=%q client_ip=%s", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP()))
		c.String(http.StatusForbidden, "webhook disabled")
		return
	}

	// :env splits test vs prod traffic at the routing layer — operator
	// registers each URL in the matching webhook slot in Pancake's dashboard.
	// We then enforce event.mode == expectedEnv to catch mis-registrations.
	expectedEnv := strings.TrimSpace(c.Param("env"))
	if expectedEnv != "test" && expectedEnv != "prod" {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake webhook 路径环境段无效 env=%q path=%q client_ip=%s",
			expectedEnv, common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP(),
		))
		c.String(http.StatusNotFound, "unknown env")
		return
	}

	bodyBytes, err := readPaymentRequestBody(c)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 读取请求体失败 path=%q client_ip=%s error=%q", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP(), err.Error()))
		c.String(http.StatusBadRequest, "bad request")
		return
	}

	signature := c.GetHeader("X-Waffo-Signature")
	bodyMeta := common.SensitiveLogBody(bodyBytes)
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 收到请求 path=%q client_ip=%s signature_meta=%s body_meta=%s", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP(), common.SensitiveLogMeta(signature), bodyMeta))

	event, err := service.VerifyConfiguredWaffoPancakeWebhook(string(bodyBytes), signature)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 验签失败 path=%q client_ip=%s signature_meta=%s body_meta=%s error=%q", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP(), common.SensitiveLogMeta(signature), bodyMeta, err.Error()))
		c.String(http.StatusUnauthorized, "invalid signature")
		return
	}
	if event == nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 验签返回空事件 path=%q client_ip=%s", common.SanitizeRequestURIForLog(c.Request.RequestURI), c.ClientIP()))
		c.String(http.StatusBadRequest, "invalid event")
		return
	}

	if !strings.EqualFold(strings.TrimSpace(event.Mode), expectedEnv) {
		rejectWaffoPancakeEnvironmentMismatch(c, expectedEnv, event)
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 验签成功 event_type=%s event_id=%s order_id=%s client_ip=%s", event.NormalizedEventType(), event.ID, event.Data.OrderID, c.ClientIP()))
	eventType := event.NormalizedEventType()
	if eventType == "refund.succeeded" || eventType == "refund.failed" {
		refundInput, buildErr := buildWaffoPancakeRefundEventInput(event, string(bodyBytes))
		if buildErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf(
				"Waffo Pancake 退款回调校验失败 event_id=%s order_id=%s client_ip=%s error=%q",
				event.ID, event.Data.OrderID, c.ClientIP(), buildErr.Error(),
			))
			c.String(http.StatusBadRequest, "invalid refund event")
			return
		}
		result, processErr := model.ProcessProviderRefundEvent(refundInput)
		if processErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf(
				"Waffo Pancake 退款回调处理失败 delivery_id=%s business_event_id=%s order_trade_no=%s client_ip=%s error=%q",
				refundInput.DeliveryID, refundInput.BusinessEventID, refundInput.OrderTradeNo, c.ClientIP(), processErr.Error(),
			))
			if errors.Is(processErr, model.ErrProviderRefundInvalid) || errors.Is(processErr, model.ErrProviderRefundConflict) {
				c.String(http.StatusBadRequest, "invalid refund event")
			} else {
				c.String(http.StatusInternalServerError, "retry")
			}
			return
		}
		logger.LogInfo(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake 退款回调处理成功 delivery_id=%s business_event_id=%s order_trade_no=%s status=%s already_processed=%t client_ip=%s",
			refundInput.DeliveryID, refundInput.BusinessEventID, result.OrderTradeNo, result.Status, result.AlreadyProcessed, c.ClientIP(),
		))
		c.String(http.StatusOK, "OK")
		return
	}
	if eventType != "order.completed" {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake webhook 不支持的事件类型 event_type=%s event_id=%s order_id=%s client_ip=%s",
			eventType, event.ID, event.Data.OrderID, c.ClientIP(),
		))
		c.String(http.StatusBadRequest, "unsupported event")
		return
	}

	// The external id is the only provider-controlled lookup key. Resolve the
	// local order type from the database; a naming prefix is merely cosmetic and
	// must never decide whether a payment grants a subscription or wallet quota.
	tradeNo := strings.TrimSpace(event.Data.OrderMerchantExternalID)
	if tradeNo == "" {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 缺少本地订单号 event_id=%s order_id=%s client_ip=%s", event.ID, event.Data.OrderID, c.ClientIP()))
		c.String(http.StatusBadRequest, "invalid event")
		return
	}
	topUp, subscriptionOrder, lookupErr := lookupWaffoPancakeOrder(tradeNo)
	if lookupErr != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake webhook 查询本地订单失败 trade_no=%s event_id=%s order_id=%s client_ip=%s error=%q",
			tradeNo, event.ID, event.Data.OrderID, c.ClientIP(), lookupErr.Error(),
		))
		// A provider event must be retried when the local database cannot be
		// consulted. Treating a query error as "not found" and returning 200
		// would acknowledge the payment permanently without settlement.
		c.String(http.StatusInternalServerError, "retry")
		return
	}
	if topUp != nil && subscriptionOrder != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 订单类型冲突 trade_no=%s event_id=%s client_ip=%s", tradeNo, event.ID, c.ClientIP()))
		c.String(http.StatusBadRequest, "order conflict")
		return
	}
	if topUp == nil && subscriptionOrder == nil {
		// A valid event for another product in the same store is not actionable
		// for this installation. Acknowledge it so the provider does not retry
		// forever, while retaining the diagnostic log above.
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo Pancake webhook 未找到本地订单 trade_no=%s event_id=%s order_id=%s client_ip=%s", tradeNo, event.ID, event.Data.OrderID, c.ClientIP()))
		c.String(http.StatusOK, "OK")
		return
	}

	if topUp != nil {
		settlement, buildErr := buildWaffoPancakeTopUpSettlement(event, topUp)
		if buildErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 充值回调校验失败 trade_no=%s event_id=%s order_id=%s client_ip=%s error=%q", tradeNo, event.ID, event.Data.OrderID, c.ClientIP(), buildErr.Error()))
			c.String(http.StatusBadRequest, "invalid event")
			return
		}
		settlement.Payload = string(bodyBytes)
		LockOrder(tradeNo)
		defer UnlockOrder(tradeNo)
		result, settleErr := model.SettleTopUpProvider(settlement, c.ClientIP(), nil)
		if settleErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 充值结算失败 trade_no=%s event_id=%s order_id=%s client_ip=%s error=%q", tradeNo, event.ID, event.Data.OrderID, c.ClientIP(), settleErr.Error()))
			if isPermanentPancakeSettlementError(settleErr) {
				c.String(http.StatusBadRequest, "invalid event")
			} else {
				c.String(http.StatusInternalServerError, "retry")
			}
			return
		}
		if result.PaidUncredited {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo Pancake 充值已付款但额度未入账，订单进入 paid_uncredited trade_no=%s event_id=%s provider_trade_no=%s already_completed=%t client_ip=%s", tradeNo, event.ID, settlement.ProviderTradeNo, result.AlreadyCompleted, c.ClientIP()))
		} else {
			logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo Pancake 充值结算成功 trade_no=%s event_id=%s provider_trade_no=%s already_completed=%t client_ip=%s", tradeNo, event.ID, settlement.ProviderTradeNo, result.AlreadyCompleted, c.ClientIP()))
		}
		c.String(http.StatusOK, "OK")
		return
	}

	settlement, buildErr := buildWaffoPancakeSubscriptionSettlement(event, subscriptionOrder)
	if buildErr != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅回调校验失败 trade_no=%s event_id=%s order_id=%s client_ip=%s error=%q", tradeNo, event.ID, event.Data.OrderID, c.ClientIP(), buildErr.Error()))
		c.String(http.StatusBadRequest, "invalid event")
		return
	}
	settlement.Payload = string(bodyBytes)
	LockOrder(tradeNo)
	defer UnlockOrder(tradeNo)
	outcome, err := model.CompleteSubscriptionOrderVerifiedWithOutcome(settlement)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅结算失败 trade_no=%s event_id=%s order_id=%s client_ip=%s error=%q", tradeNo, event.ID, event.Data.OrderID, c.ClientIP(), err.Error()))
		if isPermanentPancakeSettlementError(err) {
			c.String(http.StatusBadRequest, "invalid event")
		} else {
			c.String(http.StatusInternalServerError, "retry")
		}
		return
	}
	if outcome == model.EpaySettlementPaidUncredited {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅已付款但未开通，订单进入 paid_uncredited trade_no=%s event_id=%s order_id=%s client_ip=%s", tradeNo, event.ID, event.Data.OrderID, c.ClientIP()))
		c.String(http.StatusOK, "OK")
		return
	}
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅结算成功 trade_no=%s event_id=%s order_id=%s client_ip=%s", tradeNo, event.ID, event.Data.OrderID, c.ClientIP()))
	c.String(http.StatusOK, "OK")
}

// rejectWaffoPancakeEnvironmentMismatch deliberately returns a non-2xx
// response.  A signed event sent to the wrong /:env endpoint is usually an
// operator routing mistake; acknowledging it with 200 would make Pancake
// drop the payment permanently before the correctly configured endpoint can
// process it.  A 400 response keeps the delivery visible/retryable while
// avoiding any settlement attempt under the wrong environment.
func rejectWaffoPancakeEnvironmentMismatch(c *gin.Context, expectedEnv string, event *service.WaffoPancakeWebhookEvent) {
	if event == nil {
		c.String(http.StatusBadRequest, "environment mismatch")
		return
	}
	logger.LogError(c.Request.Context(), fmt.Sprintf(
		"Waffo Pancake webhook 环境不匹配 expected=%q actual_mode=%q event_id=%s order_id=%s client_ip=%s",
		expectedEnv, event.Mode, event.ID, event.Data.OrderID, c.ClientIP(),
	))
	c.String(http.StatusBadRequest, "environment mismatch")
}

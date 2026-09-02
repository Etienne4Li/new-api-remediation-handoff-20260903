package controller

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/thanhpk/randstr"
)

type SubscriptionWaffoPancakePayRequest struct {
	PlanId int `json:"plan_id"`
}

func SubscriptionRequestWaffoPancakePay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}
	waffoPancakeConfig := setting.GetWaffoPancakeConfig()

	var req SubscriptionWaffoPancakePayRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.PlanId <= 0 {
		common.ApiErrorMsg(c, "参数错误")
		return
	}

	plan, err := model.GetSubscriptionPlanById(req.PlanId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !plan.Enabled {
		common.ApiErrorMsg(c, "套餐未启用")
		return
	}
	if strings.TrimSpace(plan.WaffoPancakeProductId) == "" {
		common.ApiErrorMsg(c, "该套餐未配置 WaffoPancakeProductId")
		return
	}
	currency := strings.ToUpper(strings.TrimSpace(plan.Currency))
	if len(currency) != 3 {
		common.ApiErrorMsg(c, "套餐币种配置错误")
		return
	}
	// Plan targets its own Pancake product, so we only require credentials
	// here — not the gateway-level WaffoPancakeProductID.
	if strings.TrimSpace(waffoPancakeConfig.MerchantID) == "" ||
		strings.TrimSpace(waffoPancakeConfig.PrivateKey) == "" ||
		strings.TrimSpace(waffoPancakeConfig.StoreID) == "" {
		common.ApiErrorMsg(c, "Waffo Pancake 未配置或密钥无效")
		return
	}
	// The plan's product ID is admin-controlled data and may have been edited
	// directly or deactivated remotely since the plan was saved. Verify the
	// authoritative catalog before creating a charge; otherwise a recurring or
	// cross-store product could be charged while this flow only grants one local
	// entitlement period.
	if err := service.ValidateWaffoPancakeBinding(
		c.Request.Context(),
		waffoPancakeConfig.MerchantID,
		waffoPancakeConfig.PrivateKey,
		waffoPancakeConfig.StoreID,
		plan.WaffoPancakeProductId,
	); err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf(
			"Waffo Pancake 订阅商品绑定校验失败 plan_id=%d store_id=%q product_id=%q error=%q",
			plan.Id, waffoPancakeConfig.StoreID, plan.WaffoPancakeProductId, err.Error(),
		))
		common.ApiErrorMsg(c, "Waffo Pancake 套餐商品配置无效或已下架")
		return
	}

	userId := c.GetInt("id")
	user, err := model.GetUserById(userId, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if user == nil {
		common.ApiErrorMsg(c, "用户不存在")
		return
	}

	if plan.MaxPurchasePerUser > 0 {
		count, err := model.CountUserSubscriptionsByPlan(userId, plan.Id)
		if err != nil {
			common.ApiError(c, err)
			return
		}
		if count >= int64(plan.MaxPurchasePerUser) {
			common.ApiErrorMsg(c, "已达到该套餐购买上限")
			return
		}
	}

	// WAFFO_PANCAKE_SUB- prefix (vs. wallet's WAFFO_PANCAKE-) drives webhook
	// dispatch in WaffoPancakeWebhook.
	tradeNo := fmt.Sprintf("WAFFO_PANCAKE_SUB-%d-%d-%s", userId, time.Now().UnixMilli(), randstr.String(6))
	merchantID := strings.TrimSpace(waffoPancakeConfig.MerchantID)
	storeID := strings.TrimSpace(waffoPancakeConfig.StoreID)
	productID := strings.TrimSpace(plan.WaffoPancakeProductId)
	providerAmount := formatWaffoPancakeAmountForCurrency(plan.PriceAmount, currency)
	// Subscription checkout requests use the production Pancake endpoint.
	// Persist the immutable account/environment scope alongside the order so a
	// later webhook cannot be accepted after credentials are switched.
	providerEnvironment := waffoPancakeProviderEnvironment("prod")
	providerScopeFingerprint := waffoPancakeProviderScopeFingerprint(merchantID, providerEnvironment)
	if providerScopeFingerprint == "" {
		common.ApiErrorMsg(c, "Waffo Pancake 支付配置错误")
		return
	}

	order := &model.SubscriptionOrder{
		UserId:                 userId,
		PlanId:                 plan.Id,
		Money:                  plan.PriceAmount,
		TradeNo:                tradeNo,
		PaymentMethod:          model.PaymentMethodWaffoPancake,
		PaymentProvider:        model.PaymentProviderWaffoPancake,
		CreateTime:             time.Now().Unix(),
		Status:                 common.TopUpStatusPending,
		ProviderMerchantID:     merchantID,
		ProviderStoreID:        storeID,
		ProviderProductID:      productID,
		ProviderOrderName:      strings.TrimSpace(plan.Title),
		ProviderAmount:         providerAmount,
		ProviderCurrency:       currency,
		ProviderKeyFingerprint: providerScopeFingerprint,
	}
	if err := order.SetEntitlementSnapshot(plan); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅订单快照创建失败 trade_no=%s plan_id=%d error=%q", tradeNo, plan.Id, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	if err := order.Insert(); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅订单创建失败 user_id=%d plan_id=%d trade_no=%s error=%q", userId, plan.Id, tradeNo, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}

	expiresInSeconds := 45 * 60
	session, err := service.CreateWaffoPancakeCheckoutSessionWithConfig(c.Request.Context(), &service.WaffoPancakeCreateSessionParams{
		ProductID:     productID,
		ProductType:   service.WaffoPancakeProductTypeOneTime,
		Currency:      currency,
		BuyerIdentity: service.WaffoPancakeBuyerIdentityFromUserID(user.Id),
		PriceSnapshot: &service.WaffoPancakePriceSnapshot{
			Amount:      providerAmount,
			TaxCategory: "saas",
		},
		BuyerEmail:              getWaffoPancakeBuyerEmail(user),
		ExpiresInSeconds:        &expiresInSeconds,
		OrderMerchantExternalID: tradeNo,
		Metadata: newWaffoPancakeOrderMetadata(
			tradeNo, "subscription", productID, merchantID, storeID,
			currency, providerAmount, strings.TrimSpace(plan.Title), user.Id,
		),
	}, waffoPancakeConfig)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅结账会话创建失败 user_id=%d plan_id=%d trade_no=%s error=%q", userId, plan.Id, tradeNo, err.Error()))
		if expireErr := model.ExpireSubscriptionOrder(tradeNo, model.PaymentProviderWaffoPancake); expireErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅订单过期补偿失败 trade_no=%s provider=%s error=%q", tradeNo, model.PaymentProviderWaffoPancake, expireErr.Error()))
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	if session == nil || strings.TrimSpace(session.SessionID) == "" || strings.TrimSpace(session.CheckoutURL) == "" {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅返回空结账会话 user_id=%d plan_id=%d trade_no=%s", userId, plan.Id, tradeNo))
		if expireErr := model.ExpireSubscriptionOrder(tradeNo, model.PaymentProviderWaffoPancake); expireErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅订单过期补偿失败 trade_no=%s provider=%s error=%q", tradeNo, model.PaymentProviderWaffoPancake, expireErr.Error()))
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	updateResult := model.DB.Model(&model.SubscriptionOrder{}).
		Where("id = ? AND trade_no = ? AND status = ?", order.Id, tradeNo, common.TopUpStatusPending).
		Update("provider_checkout_id", strings.TrimSpace(session.SessionID))
	if updateResult.Error != nil || updateResult.RowsAffected != 1 {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 保存订阅结账会话失败 user_id=%d plan_id=%d trade_no=%s", userId, plan.Id, tradeNo))
		if updateResult.Error == nil {
			if expireErr := model.ExpireSubscriptionOrder(tradeNo, model.PaymentProviderWaffoPancake); expireErr != nil {
				logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅订单过期补偿失败 trade_no=%s provider=%s error=%q", tradeNo, model.PaymentProviderWaffoPancake, expireErr.Error()))
			}
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建支付订单失败"})
		return
	}
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo Pancake 订阅订单创建成功 user_id=%d plan_id=%d trade_no=%s session_id=%s money=%.2f", userId, plan.Id, tradeNo, session.SessionID, plan.PriceAmount))

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

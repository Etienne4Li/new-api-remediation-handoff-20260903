package controller

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/checkout/session"
	"github.com/thanhpk/randstr"
)

type SubscriptionStripePayRequest struct {
	PlanId int `json:"plan_id"`
}

func SubscriptionRequestStripePay(c *gin.Context) {
	stripeConfig := setting.GetStripeConfig()
	if !requirePaymentCompliance(c) {
		return
	}

	var req SubscriptionStripePayRequest
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
	if plan.StripePriceId == "" {
		common.ApiErrorMsg(c, "该套餐未配置 StripePriceId")
		return
	}
	if !strings.HasPrefix(stripeConfig.ApiSecret, "sk_") && !strings.HasPrefix(stripeConfig.ApiSecret, "rk_") {
		common.ApiErrorMsg(c, "Stripe 未配置或密钥无效")
		return
	}
	if stripeConfig.WebhookSecret == "" {
		common.ApiErrorMsg(c, "Stripe Webhook 未配置")
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

	reference := fmt.Sprintf("sub-stripe-ref-%d-%d-%s", user.Id, time.Now().UnixMilli(), randstr.String(4))
	referenceId := "sub_ref_" + common.Sha1([]byte(reference))
	currency := strings.ToUpper(strings.TrimSpace(plan.Currency))
	if len(currency) != 3 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "套餐币种配置错误"})
		return
	}
	providerAccountID := stripeConfiguredAccountID(stripeConfig)
	live, liveErr := stripeLiveModeWithConfig(stripeConfig)
	providerEnvironment := stripeProviderEnvironment(live)
	providerScopeFingerprint := stripeProviderScopeFingerprint(providerAccountID, providerEnvironment)
	if liveErr != nil || providerAccountID == "" || providerScopeFingerprint == "" {
		common.ApiErrorMsg(c, "Stripe 未配置、密钥无效或缺少 Account ID")
		return
	}
	order := &model.SubscriptionOrder{
		UserId:                 userId,
		PlanId:                 plan.Id,
		Money:                  plan.PriceAmount,
		TradeNo:                referenceId,
		PaymentMethod:          model.PaymentMethodStripe,
		PaymentProvider:        model.PaymentProviderStripe,
		CreateTime:             time.Now().Unix(),
		Status:                 common.TopUpStatusPending,
		ProviderMerchantID:     providerAccountID,
		ProviderOrderName:      plan.Title,
		ProviderAmount:         stripeAmountFromMajorUnits(plan.PriceAmount, currency),
		ProviderProductID:      strings.TrimSpace(plan.StripePriceId),
		ProviderCurrency:       currency,
		ProviderKeyFingerprint: providerScopeFingerprint,
	}
	if err := order.SetEntitlementSnapshot(plan); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe 订阅订单快照创建失败 trade_no=%s plan_id=%d error=%q", referenceId, plan.Id, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	if err := order.Insert(); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}
	metadata := stripeMetadata(referenceId, "subscription", plan.StripePriceId, plan.Title, providerAccountID, 0)
	metadata["new_api_currency"] = currency
	metadata["new_api_provider_amount"] = order.ProviderAmount
	checkout, err := genStripeSubscriptionCheckoutSessionWithConfig(referenceId, user.StripeCustomer, user.Email, plan.StripePriceId, metadata, stripeConfig)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe 订阅支付链接创建失败 trade_no=%s plan_id=%d error=%q", referenceId, plan.Id, err.Error()))
		if expireErr := model.ExpireSubscriptionOrder(referenceId, model.PaymentProviderStripe); expireErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe 订阅订单过期补偿失败 trade_no=%s provider=%s error=%q", referenceId, model.PaymentProviderStripe, expireErr.Error()))
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}
	if err := model.SetSubscriptionOrderProviderCheckoutID(referenceId, model.PaymentProviderStripe, checkout.ID); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe 保存订阅 Checkout Session 失败 trade_no=%s plan_id=%d error=%q", referenceId, plan.Id, err.Error()))
		if expireErr := model.ExpireSubscriptionOrder(referenceId, model.PaymentProviderStripe); expireErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("Stripe 订阅订单过期补偿失败 trade_no=%s provider=%s error=%q", referenceId, model.PaymentProviderStripe, expireErr.Error()))
		}
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建支付订单失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "success",
		"data": gin.H{
			"pay_link": checkout.URL,
		},
	})
}

func genStripeSubscriptionLink(referenceId string, customerId string, email string, priceId string) (string, error) {
	result, err := genStripeSubscriptionCheckoutSession(referenceId, customerId, email, priceId, nil)
	if err != nil {
		return "", err
	}
	return result.URL, nil
}

func genStripeSubscriptionCheckoutSession(referenceId string, customerId string, email string, priceId string, metadata map[string]string) (*stripe.CheckoutSession, error) {
	return genStripeSubscriptionCheckoutSessionWithConfig(referenceId, customerId, email, priceId, metadata, setting.GetStripeConfig())
}

func genStripeSubscriptionCheckoutSessionWithConfig(referenceId string, customerId string, email string, priceId string, metadata map[string]string, stripeConfig setting.StripeConfig) (*stripe.CheckoutSession, error) {
	params := &stripe.CheckoutSessionParams{
		ClientReferenceID: stripe.String(referenceId),
		SuccessURL:        stripe.String(paymentReturnPath("/wallet")),
		CancelURL:         stripe.String(paymentReturnPath("/wallet")),
		LineItems: []*stripe.CheckoutSessionLineItemParams{
			{
				Price:    stripe.String(priceId),
				Quantity: stripe.Int64(1),
			},
		},
		Mode:     stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		Metadata: metadata,
	}

	if "" == customerId {
		if "" != email {
			params.CustomerEmail = stripe.String(email)
		}
		params.CustomerCreation = stripe.String(string(stripe.CheckoutSessionCustomerCreationAlways))
	} else {
		params.Customer = stripe.String(customerId)
	}

	stripeKeyMu.Lock()
	stripe.Key = stripeConfig.ApiSecret
	result, err := session.New(params)
	stripeKeyMu.Unlock()
	if err != nil {
		return nil, err
	}
	if result == nil || strings.TrimSpace(result.ID) == "" || strings.TrimSpace(result.URL) == "" {
		return nil, fmt.Errorf("Stripe returned an empty subscription checkout session")
	}
	return result, nil
}

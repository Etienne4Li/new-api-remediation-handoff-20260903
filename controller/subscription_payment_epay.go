package controller

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

type SubscriptionEpayPayRequest struct {
	PlanId        int    `json:"plan_id"`
	PaymentMethod string `json:"payment_method"`
}

func SubscriptionRequestEpay(c *gin.Context) {
	if !requirePaymentCompliance(c) {
		return
	}
	paymentConfig := operation_setting.GetPaymentRuntimeConfig()
	paymentSetting := operation_setting.GetPaymentSettingSnapshot()
	if !isEpayCheckoutEnabledWithConfig(paymentConfig, paymentSetting) {
		common.ApiErrorMsg(c, "易支付暂不可用")
		return
	}

	var req SubscriptionEpayPayRequest
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
	if plan.PriceAmount < 0.01 {
		common.ApiErrorMsg(c, "套餐金额过低")
		return
	}
	if !containsPayMethod(paymentConfig.PayMethods, req.PaymentMethod) {
		common.ApiErrorMsg(c, "支付方式不存在")
		return
	}

	userId := c.GetInt("id")
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

	callBackAddress := service.GetCallbackAddress()
	returnUrl, err := url.Parse(callBackAddress + "/api/subscription/epay/return")
	if err != nil {
		common.ApiErrorMsg(c, "回调地址配置错误")
		return
	}
	notifyUrl, err := url.Parse(callBackAddress + "/api/subscription/epay/notify")
	if err != nil {
		common.ApiErrorMsg(c, "回调地址配置错误")
		return
	}

	tradeNo := fmt.Sprintf("%s%d", common.GetRandomString(6), time.Now().Unix())
	tradeNo = fmt.Sprintf("SUBUSR%dNO%s", userId, tradeNo)

	client := getEpayClientWithConfig(paymentConfig)
	if client == nil {
		common.ApiErrorMsg(c, "当前管理员未配置支付信息")
		return
	}

	providerName := fmt.Sprintf("SUB:%s", plan.Title)
	providerAmount := strconv.FormatFloat(plan.PriceAmount, 'f', 2, 64)
	order := &model.SubscriptionOrder{
		UserId:                 userId,
		PlanId:                 plan.Id,
		Money:                  plan.PriceAmount,
		TradeNo:                tradeNo,
		PaymentMethod:          req.PaymentMethod,
		PaymentProvider:        model.PaymentProviderEpay,
		CreateTime:             time.Now().Unix(),
		Status:                 common.TopUpStatusPending,
		ProviderMerchantID:     paymentConfig.EpayID,
		ProviderOrderName:      providerName,
		ProviderAmount:         providerAmount,
		ProviderKeyFingerprint: model.EpayKeyFingerprint(paymentConfig.EpayKey),
	}
	if err := order.SetEntitlementSnapshot(plan); err != nil {
		common.ApiErrorMsg(c, "创建订单失败")
		return
	}
	if err := order.Insert(); err != nil {
		common.ApiErrorMsg(c, "创建订单失败")
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
		if expireErr := model.ExpireSubscriptionOrder(tradeNo, model.PaymentProviderEpay); expireErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("易支付订阅订单过期补偿失败 trade_no=%s provider=%s error=%q", tradeNo, model.PaymentProviderEpay, expireErr.Error()))
		}
		common.ApiErrorMsg(c, "拉起支付失败")
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": params, "url": uri})
}

func SubscriptionEpayNotify(c *gin.Context) {
	if c.Request.Method == http.MethodGet && !isEpayGetWebhookEnabled() {
		c.Header("Allow", http.MethodPost)
		c.AbortWithStatus(http.StatusMethodNotAllowed)
		return
	}
	if !isEpayWebhookEnabled() {
		writeEpayFailure(c)
		return
	}
	callback, _, err := verifyEpayCallback(c)
	if err != nil {
		writeEpayFailure(c)
		return
	}
	if callback.TradeStatus != epay.StatusTradeSuccess {
		// A valid non-success notification is acknowledged so EPay does not
		// retry it forever; no settlement is attempted.
		if _, writeErr := c.Writer.Write([]byte("success")); writeErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("易支付订阅 webhook 响应写入失败 trade_no=%s client_ip=%s error=%q", callback.ServiceTradeNo, c.ClientIP(), writeErr.Error()))
		}
		return
	}

	LockOrder(callback.ServiceTradeNo)
	defer UnlockOrder(callback.ServiceTradeNo)

	outcome, err := model.CompleteSubscriptionOrderEpayWithOutcome(callback)
	if err != nil {
		if _, writeErr := c.Writer.Write([]byte("fail")); writeErr != nil {
			logger.LogError(c.Request.Context(), fmt.Sprintf("易支付订阅 webhook 响应写入失败 trade_no=%s client_ip=%s error=%q", callback.ServiceTradeNo, c.ClientIP(), writeErr.Error()))
		}
		return
	}
	if outcome == model.EpaySettlementLegacySuccessAcknowledged {
		common.SysError(fmt.Sprintf("allowlisted legacy EPay subscription callback acknowledged without settlement trade_no=%s provider_trade_no=%s", callback.ServiceTradeNo, callback.ProviderTradeNo))
	} else if outcome == model.EpaySettlementPaidUncredited {
		// Payment evidence and the paid_uncredited terminal state were committed
		// atomically. ACK EPay so it stops retrying; an administrator can retry the
		// entitlement grant or issue the recorded refund_required action.
		common.SysError(fmt.Sprintf("EPay 订阅已付款但未开通，订单进入 paid_uncredited trade_no=%s provider_trade_no=%s", callback.ServiceTradeNo, callback.ProviderTradeNo))
	}

	if _, writeErr := c.Writer.Write([]byte("success")); writeErr != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("易支付订阅 webhook 响应写入失败 trade_no=%s client_ip=%s error=%q", callback.ServiceTradeNo, c.ClientIP(), writeErr.Error()))
	}
}

// SubscriptionEpayReturn handles browser return after payment. Browser return
// is informational only: settlement is performed exclusively by notify,
// because the return URL is user-agent controlled and providers may omit or
// delay it. This also avoids a return request racing notify with a second
// completion path.
func SubscriptionEpayReturn(c *gin.Context) {
	// Do not verify or settle here. A return request can be forged by a
	// browser, replayed, or arrive before the provider's server-to-server
	// notification. The wallet page obtains the authoritative order status via
	// the authenticated API and can display pending until notify completes.
	c.Redirect(http.StatusFound, paymentReturnPath("/wallet?pay=pending"))
}

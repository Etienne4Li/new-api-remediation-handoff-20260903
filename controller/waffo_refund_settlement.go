package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	waffo "github.com/waffo-com/waffo-go"
	"github.com/waffo-com/waffo-go/core"
	waffoorder "github.com/waffo-com/waffo-go/types/order"
	wafforefund "github.com/waffo-com/waffo-go/types/refund"
)

var (
	errWaffoRefundInvalid  = errors.New("invalid Waffo refund notification")
	errWaffoRefundMismatch = errors.New("Waffo refund evidence does not match local order")
)

// buildWaffoRefundEventInput joins three independent facts before a refund is
// allowed to reach the accounting ledger: the signed notification, a refund
// inquiry, and an original-order inquiry. A cumulative full-refund notification
// whose individual refund is smaller than the original payment remains manual;
// otherwise prior manual adjustments could be debited a second time.
func buildWaffoRefundEventInput(
	notification *core.RefundNotificationResult,
	refundEvidence *wafforefund.InquiryRefundData,
	orderEvidence *waffoorder.InquiryOrderData,
	topUp *model.TopUp,
	cfg setting.WaffoConfig,
	payload string,
) (model.ProviderRefundEventInput, string, error) {
	if notification == nil || refundEvidence == nil || orderEvidence == nil || topUp == nil || strings.TrimSpace(payload) == "" {
		return model.ProviderRefundEventInput{}, "", errWaffoRefundInvalid
	}
	if topUp.PaymentProvider != model.PaymentProviderWaffo {
		return model.ProviderRefundEventInput{}, "", model.ErrPaymentMethodMismatch
	}
	if topUp.Status != common.TopUpStatusSuccess {
		return model.ProviderRefundEventInput{}, "", model.ErrTopUpStatusInvalid
	}

	tradeNo := strings.TrimSpace(topUp.TradeNo)
	providerTradeNo := ""
	if topUp.ProviderTradeNo != nil {
		providerTradeNo = strings.TrimSpace(*topUp.ProviderTradeNo)
	}
	checkoutID := strings.TrimSpace(topUp.ProviderCheckoutID)
	merchantID := strings.TrimSpace(cfg.MerchantID)
	if tradeNo == "" || providerTradeNo == "" || checkoutID == "" || providerTradeNo != checkoutID ||
		strings.TrimSpace(topUp.ProviderAmount) == "" || strings.TrimSpace(topUp.ProviderCurrency) == "" ||
		strings.TrimSpace(topUp.ProviderMerchantID) == "" || merchantID == "" {
		return model.ProviderRefundEventInput{}, "", model.ErrProviderSnapshotMissing
	}
	if merchantID != strings.TrimSpace(topUp.ProviderMerchantID) {
		return model.ProviderRefundEventInput{}, "", errWaffoRefundMismatch
	}

	refundRequestID := strings.TrimSpace(notification.RefundRequestID)
	acquiringRefundID := strings.TrimSpace(notification.AcquiringRefundOrderID)
	acquiringOrderID := strings.TrimSpace(notification.AcquiringOrderID)
	originalPaymentID := strings.TrimSpace(notification.OrigPaymentRequestID)
	refundAmount := strings.TrimSpace(notification.RefundAmount)
	refundStatus := strings.ToUpper(strings.TrimSpace(notification.RefundStatus))
	remainingAmount := strings.TrimSpace(notification.RemainingRefundAmount)
	if refundRequestID == "" || acquiringOrderID == "" || originalPaymentID == "" || refundAmount == "" || refundStatus == "" {
		return model.ProviderRefundEventInput{}, "", errWaffoRefundInvalid
	}
	if acquiringRefundID == "" {
		acquiringRefundID = refundRequestID
	}
	if originalPaymentID != tradeNo || acquiringOrderID != providerTradeNo {
		return model.ProviderRefundEventInput{}, "", errWaffoRefundMismatch
	}

	if strings.TrimSpace(refundEvidence.RefundRequestID) != refundRequestID ||
		strings.TrimSpace(refundEvidence.AcquiringOrderID) != acquiringOrderID ||
		strings.TrimSpace(refundEvidence.OrigPaymentRequestID) != originalPaymentID ||
		strings.ToUpper(strings.TrimSpace(refundEvidence.RefundStatus)) != refundStatus ||
		!compareWaffoAmounts(refundEvidence.RefundAmount, refundAmount) {
		return model.ProviderRefundEventInput{}, "", errWaffoRefundMismatch
	}
	if evidenceRefundID := strings.TrimSpace(refundEvidence.AcquiringRefundOrderID); evidenceRefundID != "" && evidenceRefundID != acquiringRefundID {
		return model.ProviderRefundEventInput{}, "", errWaffoRefundMismatch
	}
	if evidenceMerchantRefundID := strings.TrimSpace(refundEvidence.MerchantRefundOrderID); evidenceMerchantRefundID != "" &&
		strings.TrimSpace(notification.MerchantRefundOrderID) != "" && evidenceMerchantRefundID != strings.TrimSpace(notification.MerchantRefundOrderID) {
		return model.ProviderRefundEventInput{}, "", errWaffoRefundMismatch
	}
	if evidenceRemaining := strings.TrimSpace(refundEvidence.RemainingRefundAmount); evidenceRemaining != "" && remainingAmount != "" &&
		!compareWaffoNonNegativeAmounts(evidenceRemaining, remainingAmount) {
		return model.ProviderRefundEventInput{}, "", errWaffoRefundMismatch
	}

	orderMerchantID := ""
	if orderEvidence.MerchantInfo != nil {
		orderMerchantID = strings.TrimSpace(orderEvidence.MerchantInfo.MerchantID)
	}
	orderCurrency := strings.ToUpper(strings.TrimSpace(orderEvidence.OrderCurrency))
	if strings.TrimSpace(orderEvidence.PaymentRequestID) != tradeNo || strings.TrimSpace(orderEvidence.MerchantOrderID) != tradeNo ||
		strings.TrimSpace(orderEvidence.AcquiringOrderID) != providerTradeNo ||
		strings.ToUpper(strings.TrimSpace(orderEvidence.OrderStatus)) != core.OrderStatusPaySuccess ||
		orderMerchantID != merchantID || !compareWaffoAmounts(orderEvidence.OrderAmount, topUp.ProviderAmount) ||
		orderCurrency != strings.ToUpper(strings.TrimSpace(topUp.ProviderCurrency)) {
		return model.ProviderRefundEventInput{}, "", errWaffoRefundMismatch
	}

	eventType := ""
	normalizedStatus := ""
	manualReason := ""
	switch refundStatus {
	case core.RefundStatusFullyRefunded:
		eventType = "refund.succeeded"
		normalizedStatus = "succeeded"
		if remainingAmount == "" || !compareWaffoNonNegativeAmounts(remainingAmount, "0") {
			return model.ProviderRefundEventInput{}, "", errWaffoRefundMismatch
		}
		if !compareWaffoAmounts(refundAmount, topUp.ProviderAmount) {
			manualReason = model.ProviderRefundManualReasonPartialRefundUnsupported
		}
	case core.RefundStatusPartiallyRefunded:
		eventType = "refund.succeeded"
		normalizedStatus = "succeeded"
		manualReason = model.ProviderRefundManualReasonPartialRefundUnsupported
	case core.RefundStatusFailed:
		eventType = "refund.failed"
		normalizedStatus = "failed"
	default:
		return model.ProviderRefundEventInput{}, "", errWaffoRefundInvalid
	}

	digest := sha256.Sum256([]byte(payload))
	merchantRefundID := strings.TrimSpace(notification.MerchantRefundOrderID)
	if merchantRefundID == "" {
		merchantRefundID = refundRequestID
	}
	environment := "production"
	if cfg.Sandbox {
		environment = "sandbox"
	}
	return model.ProviderRefundEventInput{
		Provider:                       model.PaymentProviderWaffo,
		ProviderAccountID:              merchantID,
		ProviderEnvironment:            environment,
		DeliveryID:                     "sha256:" + hex.EncodeToString(digest[:]),
		BusinessEventID:                refundRequestID,
		RefundTicketMerchantExternalID: merchantRefundID,
		EffectID:                       acquiringRefundID,
		ProviderObjectType:             model.ProviderPaymentObjectAcquiringOrder,
		ProviderTradeNo:                providerTradeNo,
		OrderTradeNo:                   tradeNo,
		OrderKind:                      model.PaymentEventOrderTopUp,
		EventType:                      eventType,
		RefundStatus:                   normalizedStatus,
		Amount:                         refundAmount,
		Currency:                       orderCurrency,
		Reason:                         strings.TrimSpace(notification.RefundReason),
		Payload:                        payload,
	}, manualReason, nil
}

func compareWaffoNonNegativeAmounts(expected, actual string) bool {
	expectedAmount, expectedErr := decimal.NewFromString(strings.TrimSpace(expected))
	actualAmount, actualErr := decimal.NewFromString(strings.TrimSpace(actual))
	return expectedErr == nil && actualErr == nil && !expectedAmount.IsNegative() && !actualAmount.IsNegative() && expectedAmount.Equal(actualAmount)
}

func buildWaffoRefundObservationInput(notification *core.RefundNotificationResult, topUp *model.TopUp, cfg setting.WaffoConfig, payload string) (model.ProviderRefundEventInput, error) {
	if notification == nil || topUp == nil || strings.TrimSpace(payload) == "" {
		return model.ProviderRefundEventInput{}, errWaffoRefundInvalid
	}
	if topUp.PaymentProvider != model.PaymentProviderWaffo {
		return model.ProviderRefundEventInput{}, model.ErrPaymentMethodMismatch
	}
	merchantID := strings.TrimSpace(cfg.MerchantID)
	if merchantID == "" || merchantID != strings.TrimSpace(topUp.ProviderMerchantID) {
		return model.ProviderRefundEventInput{}, errWaffoRefundMismatch
	}
	effectID := strings.TrimSpace(notification.AcquiringRefundOrderID)
	if effectID == "" {
		effectID = strings.TrimSpace(notification.RefundRequestID)
	}
	if effectID == "" || strings.TrimSpace(topUp.TradeNo) == "" {
		return model.ProviderRefundEventInput{}, errWaffoRefundInvalid
	}
	eventType := ""
	refundStatus := ""
	switch strings.ToUpper(strings.TrimSpace(notification.RefundStatus)) {
	case core.RefundStatusFullyRefunded, core.RefundStatusPartiallyRefunded:
		eventType = "refund.succeeded"
		refundStatus = "succeeded"
	case core.RefundStatusFailed:
		eventType = "refund.failed"
		refundStatus = "failed"
	default:
		return model.ProviderRefundEventInput{}, errWaffoRefundInvalid
	}
	digest := sha256.Sum256([]byte(payload))
	merchantRefundID := strings.TrimSpace(notification.MerchantRefundOrderID)
	if merchantRefundID == "" {
		merchantRefundID = strings.TrimSpace(notification.RefundRequestID)
	}
	environment := "production"
	if cfg.Sandbox {
		environment = "sandbox"
	}
	return model.ProviderRefundEventInput{
		Provider:                       model.PaymentProviderWaffo,
		ProviderAccountID:              merchantID,
		ProviderEnvironment:            environment,
		DeliveryID:                     "sha256:" + hex.EncodeToString(digest[:]),
		BusinessEventID:                strings.TrimSpace(notification.RefundRequestID),
		RefundTicketMerchantExternalID: merchantRefundID,
		EffectID:                       effectID,
		ProviderObjectType:             model.ProviderPaymentObjectAcquiringOrder,
		ProviderTradeNo:                strings.TrimSpace(notification.AcquiringOrderID),
		OrderTradeNo:                   strings.TrimSpace(topUp.TradeNo),
		OrderKind:                      model.PaymentEventOrderTopUp,
		EventType:                      eventType,
		RefundStatus:                   refundStatus,
		Amount:                         strings.TrimSpace(notification.RefundAmount),
		// A signed notification has no authoritative payment currency. Keeping
		// this empty makes ProcessProviderRefundEvent durably fail closed.
		Currency: "",
		Reason:   strings.TrimSpace(notification.RefundReason),
		Payload:  payload,
	}, nil
}

func loadWaffoRefundEvidence(ctx context.Context, sdk *waffo.Waffo, notification *core.RefundNotificationResult) (*wafforefund.InquiryRefundData, *waffoorder.InquiryOrderData, error) {
	if sdk == nil || notification == nil {
		return nil, nil, errWaffoRefundInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	refundResponse, err := sdk.Refund().Inquiry(ctx, &wafforefund.InquiryRefundParams{
		RefundRequestID:        strings.TrimSpace(notification.RefundRequestID),
		AcquiringRefundOrderID: strings.TrimSpace(notification.AcquiringRefundOrderID),
	}, nil)
	if err != nil || refundResponse == nil || !refundResponse.IsSuccess() || refundResponse.GetData() == nil {
		if err != nil {
			return nil, nil, fmt.Errorf("query Waffo refund: %w", err)
		}
		return nil, nil, errors.New("query Waffo refund returned no verified evidence")
	}
	orderResponse, err := sdk.Order().Inquiry(ctx, &waffoorder.InquiryOrderParams{
		PaymentRequestID: strings.TrimSpace(notification.OrigPaymentRequestID),
		AcquiringOrderID: strings.TrimSpace(notification.AcquiringOrderID),
	}, nil)
	if err != nil || orderResponse == nil || !orderResponse.IsSuccess() || orderResponse.GetData() == nil {
		if err != nil {
			return nil, nil, fmt.Errorf("query Waffo order: %w", err)
		}
		return nil, nil, errors.New("query Waffo order returned no verified evidence")
	}
	return refundResponse.GetData(), orderResponse.GetData(), nil
}

func handleWaffoRefund(c *gin.Context, wh *core.WebhookHandler, sdk *waffo.Waffo, cfg setting.WaffoConfig, notification *core.RefundNotificationResult, payload string) {
	handleWaffoRefundWithEvidenceLoader(c, wh, sdk, cfg, notification, payload, loadWaffoRefundEvidence)
}

type waffoRefundEvidenceLoader func(context.Context, *waffo.Waffo, *core.RefundNotificationResult) (*wafforefund.InquiryRefundData, *waffoorder.InquiryOrderData, error)

func handleWaffoRefundWithEvidenceLoader(c *gin.Context, wh *core.WebhookHandler, sdk *waffo.Waffo, cfg setting.WaffoConfig, notification *core.RefundNotificationResult, payload string, loadEvidence waffoRefundEvidenceLoader) {
	if notification == nil {
		sendWaffoWebhookResponse(c, wh, false, "invalid refund notification")
		return
	}
	status := strings.ToUpper(strings.TrimSpace(notification.RefundStatus))
	if status == core.RefundStatusInProgress {
		if strings.TrimSpace(notification.RefundRequestID) == "" || strings.TrimSpace(notification.OrigPaymentRequestID) == "" {
			sendWaffoWebhookResponse(c, wh, false, "invalid refund notification")
			return
		}
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo 退款仍在处理中 trade_no=%s refund_request_id=%s client_ip=%s", notification.OrigPaymentRequestID, notification.RefundRequestID, c.ClientIP()))
		sendWaffoWebhookResponse(c, wh, true, "")
		return
	}

	tradeNo := strings.TrimSpace(notification.OrigPaymentRequestID)
	if tradeNo == "" {
		sendWaffoWebhookResponse(c, wh, false, "invalid refund notification")
		return
	}
	topUp, err := model.GetTopUpByTradeNoWithError(tradeNo)
	if err != nil {
		if errors.Is(err, model.ErrTopUpNotFound) {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo 退款对应订单不存在 trade_no=%s client_ip=%s", tradeNo, c.ClientIP()))
			sendWaffoWebhookResponse(c, wh, true, "")
			return
		}
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 退款查询本地订单失败 trade_no=%s client_ip=%s error=%q", tradeNo, c.ClientIP(), err.Error()))
		sendWaffoWebhookResponse(c, wh, false, "refund settlement unavailable")
		return
	}

	if loadEvidence == nil {
		sendWaffoWebhookResponse(c, wh, false, "refund settlement unavailable")
		return
	}
	refundEvidence, orderEvidence, evidenceErr := loadEvidence(c.Request.Context(), sdk, notification)
	input, manualReason, buildErr := buildWaffoRefundEventInput(notification, refundEvidence, orderEvidence, topUp, cfg, payload)
	if evidenceErr != nil || buildErr != nil {
		input, err = buildWaffoRefundObservationInput(notification, topUp, cfg, payload)
		if err != nil {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo 退款证据无效 trade_no=%s client_ip=%s error=%q", tradeNo, c.ClientIP(), err.Error()))
			sendWaffoWebhookResponse(c, wh, false, "invalid refund notification")
			return
		}
		if evidenceErr != nil {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo 退款查询证据不可用，转人工对账 trade_no=%s client_ip=%s error=%q", tradeNo, c.ClientIP(), evidenceErr.Error()))
		} else {
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("Waffo 退款证据冲突，转人工对账 trade_no=%s client_ip=%s error=%q", tradeNo, c.ClientIP(), buildErr.Error()))
		}
	} else {
		input.ForceManualReason = manualReason
	}

	result, err := model.ProcessProviderRefundEvent(input)
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("Waffo 退款账本处理失败 trade_no=%s refund_request_id=%s client_ip=%s error=%q", tradeNo, notification.RefundRequestID, c.ClientIP(), err.Error()))
		sendWaffoWebhookResponse(c, wh, false, "refund settlement failed")
		return
	}
	logger.LogInfo(c.Request.Context(), fmt.Sprintf("Waffo 退款账本处理完成 trade_no=%s refund_request_id=%s status=%s already_processed=%t client_ip=%s", tradeNo, notification.RefundRequestID, result.Status, result.AlreadyProcessed, c.ClientIP()))
	sendWaffoWebhookResponse(c, wh, true, "")
}

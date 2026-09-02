package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	waffo "github.com/waffo-com/waffo-go"
	"github.com/waffo-com/waffo-go/config"
	"github.com/waffo-com/waffo-go/core"
	waffoorder "github.com/waffo-com/waffo-go/types/order"
	wafforefund "github.com/waffo-com/waffo-go/types/refund"
	"gorm.io/gorm"
)

func validWaffoRefundEvidence() (*core.RefundNotificationResult, *wafforefund.InquiryRefundData, *waffoorder.InquiryOrderData, *model.TopUp, setting.WaffoConfig) {
	tradeNo := "WAFFO-42-1-refund"
	providerTradeNo := "ACQ_order_1"
	notification := &core.RefundNotificationResult{
		RefundRequestID:        "refund-request-1",
		MerchantRefundOrderID:  "merchant-refund-1",
		AcquiringOrderID:       providerTradeNo,
		AcquiringRefundOrderID: "ACQ_REFUND_1",
		OrigPaymentRequestID:   tradeNo,
		RefundAmount:           "8.00",
		RefundStatus:           core.RefundStatusFullyRefunded,
		RemainingRefundAmount:  "0.00",
		RefundReason:           "customer requested refund",
	}
	refundEvidence := &wafforefund.InquiryRefundData{
		RefundRequestID:        notification.RefundRequestID,
		MerchantRefundOrderID:  notification.MerchantRefundOrderID,
		AcquiringOrderID:       notification.AcquiringOrderID,
		AcquiringRefundOrderID: notification.AcquiringRefundOrderID,
		OrigPaymentRequestID:   notification.OrigPaymentRequestID,
		RefundAmount:           notification.RefundAmount,
		RefundStatus:           notification.RefundStatus,
		RemainingRefundAmount:  notification.RemainingRefundAmount,
	}
	orderEvidence := &waffoorder.InquiryOrderData{
		PaymentRequestID: notification.OrigPaymentRequestID,
		MerchantOrderID:  notification.OrigPaymentRequestID,
		AcquiringOrderID: notification.AcquiringOrderID,
		OrderStatus:      core.OrderStatusPaySuccess,
		OrderCurrency:    "USD",
		OrderAmount:      "8.00",
		MerchantInfo:     &waffoorder.MerchantInfo{MerchantID: "merchant-test"},
	}
	topUp := &model.TopUp{
		UserId: 42, TradeNo: tradeNo, PaymentMethod: model.PaymentMethodWaffo,
		PaymentProvider: model.PaymentProviderWaffo, Status: common.TopUpStatusSuccess,
		CreditedQuota: 800, ProviderTradeNo: &providerTradeNo,
		ProviderMerchantID: "merchant-test", ProviderAmount: "8.00",
		ProviderCurrency: "USD", ProviderCheckoutID: providerTradeNo,
	}
	cfg := setting.WaffoConfig{Sandbox: true, MerchantID: "merchant-test"}
	return notification, refundEvidence, orderEvidence, topUp, cfg
}

func TestBuildWaffoRefundEventInputRequiresIndependentFullRefundEvidence(t *testing.T) {
	notification, refundEvidence, orderEvidence, topUp, cfg := validWaffoRefundEvidence()
	input, manualReason, err := buildWaffoRefundEventInput(notification, refundEvidence, orderEvidence, topUp, cfg, `{"eventType":"REFUND_NOTIFICATION","result":{"refundRequestId":"refund-request-1"}}`)
	require.NoError(t, err)
	assert.Empty(t, manualReason)
	assert.Equal(t, model.PaymentProviderWaffo, input.Provider)
	assert.Equal(t, "merchant-test", input.ProviderAccountID)
	assert.Equal(t, "sandbox", input.ProviderEnvironment)
	assert.NotEmpty(t, input.DeliveryID)
	assert.Equal(t, "ACQ_REFUND_1", input.EffectID)
	assert.Equal(t, topUp.TradeNo, input.OrderTradeNo)
	assert.Equal(t, model.PaymentEventOrderTopUp, input.OrderKind)
	assert.Equal(t, model.ProviderPaymentObjectAcquiringOrder, input.ProviderObjectType)
	assert.Equal(t, "ACQ_order_1", input.ProviderTradeNo)
	assert.Equal(t, "refund.succeeded", input.EventType)
	assert.Equal(t, "succeeded", input.RefundStatus)
	assert.Equal(t, "8.00", input.Amount)
	assert.Equal(t, "USD", input.Currency)

	replay, replayReason, err := buildWaffoRefundEventInput(notification, refundEvidence, orderEvidence, topUp, cfg, input.Payload)
	require.NoError(t, err)
	assert.Equal(t, input.DeliveryID, replay.DeliveryID)
	assert.Equal(t, manualReason, replayReason)
}

func TestBuildWaffoRefundEventInputKeepsPartialRefundManual(t *testing.T) {
	notification, refundEvidence, orderEvidence, topUp, cfg := validWaffoRefundEvidence()
	notification.RefundStatus = core.RefundStatusPartiallyRefunded
	notification.RefundAmount = "2.00"
	notification.RemainingRefundAmount = "6.00"
	refundEvidence.RefundStatus = notification.RefundStatus
	refundEvidence.RefundAmount = notification.RefundAmount
	refundEvidence.RemainingRefundAmount = notification.RemainingRefundAmount

	input, manualReason, err := buildWaffoRefundEventInput(notification, refundEvidence, orderEvidence, topUp, cfg, `{"eventType":"REFUND_NOTIFICATION","result":{"refundStatus":"ORDER_PARTIALLY_REFUNDED"}}`)
	require.NoError(t, err)
	assert.Equal(t, model.ProviderRefundManualReasonPartialRefundUnsupported, manualReason)
	assert.Equal(t, "refund.succeeded", input.EventType)
	assert.Equal(t, "succeeded", input.RefundStatus)
	assert.Equal(t, "2.00", input.Amount)
	assert.Equal(t, "USD", input.Currency)
}

func TestBuildWaffoRefundEventInputKeepsCumulativeFullRefundManual(t *testing.T) {
	notification, refundEvidence, orderEvidence, topUp, cfg := validWaffoRefundEvidence()
	notification.RefundAmount = "2.00"
	refundEvidence.RefundAmount = notification.RefundAmount

	input, manualReason, err := buildWaffoRefundEventInput(notification, refundEvidence, orderEvidence, topUp, cfg, `{"eventType":"REFUND_NOTIFICATION","result":{"refundStatus":"ORDER_FULLY_REFUNDED"}}`)
	require.NoError(t, err)
	assert.Equal(t, model.ProviderRefundManualReasonPartialRefundUnsupported, manualReason)
	assert.Equal(t, "2.00", input.Amount)
}

func TestBuildWaffoRefundEventInputMapsFailedRefundWithoutReversal(t *testing.T) {
	notification, refundEvidence, orderEvidence, topUp, cfg := validWaffoRefundEvidence()
	notification.RefundStatus = core.RefundStatusFailed
	refundEvidence.RefundStatus = notification.RefundStatus

	input, manualReason, err := buildWaffoRefundEventInput(notification, refundEvidence, orderEvidence, topUp, cfg, `{"eventType":"REFUND_NOTIFICATION","result":{"refundStatus":"ORDER_REFUND_FAILED"}}`)
	require.NoError(t, err)
	assert.Empty(t, manualReason)
	assert.Equal(t, "refund.failed", input.EventType)
	assert.Equal(t, "failed", input.RefundStatus)
}

func TestBuildWaffoRefundObservationInputCannotAutoReverse(t *testing.T) {
	notification, _, _, topUp, cfg := validWaffoRefundEvidence()
	payload := `{"eventType":"REFUND_NOTIFICATION","result":{"refundRequestId":"refund-request-1"}}`
	input, err := buildWaffoRefundObservationInput(notification, topUp, cfg, payload)
	require.NoError(t, err)
	assert.Equal(t, "refund.succeeded", input.EventType)
	assert.Equal(t, "8.00", input.Amount)
	assert.Empty(t, input.Currency, "the signed notification alone has no authoritative payment currency")
	assert.Equal(t, "sandbox", input.ProviderEnvironment)
	assert.NotEmpty(t, input.DeliveryID)

	replay, err := buildWaffoRefundObservationInput(notification, topUp, cfg, payload)
	require.NoError(t, err)
	assert.Equal(t, input.DeliveryID, replay.DeliveryID)
}

func TestBuildWaffoRefundEventInputRejectsContradictoryEvidence(t *testing.T) {
	tests := map[string]func(*core.RefundNotificationResult, *wafforefund.InquiryRefundData, *waffoorder.InquiryOrderData, *model.TopUp, *setting.WaffoConfig){
		"refund request": func(n *core.RefundNotificationResult, r *wafforefund.InquiryRefundData, _ *waffoorder.InquiryOrderData, _ *model.TopUp, _ *setting.WaffoConfig) {
			r.RefundRequestID = n.RefundRequestID + "-other"
		},
		"refund amount": func(_ *core.RefundNotificationResult, r *wafforefund.InquiryRefundData, _ *waffoorder.InquiryOrderData, _ *model.TopUp, _ *setting.WaffoConfig) {
			r.RefundAmount = "7.99"
		},
		"refund status": func(_ *core.RefundNotificationResult, r *wafforefund.InquiryRefundData, _ *waffoorder.InquiryOrderData, _ *model.TopUp, _ *setting.WaffoConfig) {
			r.RefundStatus = core.RefundStatusFailed
		},
		"original payment": func(_ *core.RefundNotificationResult, _ *wafforefund.InquiryRefundData, o *waffoorder.InquiryOrderData, _ *model.TopUp, _ *setting.WaffoConfig) {
			o.PaymentRequestID = "another-order"
		},
		"acquiring order": func(_ *core.RefundNotificationResult, _ *wafforefund.InquiryRefundData, o *waffoorder.InquiryOrderData, _ *model.TopUp, _ *setting.WaffoConfig) {
			o.AcquiringOrderID = "ACQ_other"
		},
		"order amount": func(_ *core.RefundNotificationResult, _ *wafforefund.InquiryRefundData, o *waffoorder.InquiryOrderData, _ *model.TopUp, _ *setting.WaffoConfig) {
			o.OrderAmount = "7.99"
		},
		"order currency": func(_ *core.RefundNotificationResult, _ *wafforefund.InquiryRefundData, o *waffoorder.InquiryOrderData, _ *model.TopUp, _ *setting.WaffoConfig) {
			o.OrderCurrency = "EUR"
		},
		"merchant": func(_ *core.RefundNotificationResult, _ *wafforefund.InquiryRefundData, o *waffoorder.InquiryOrderData, _ *model.TopUp, _ *setting.WaffoConfig) {
			o.MerchantInfo.MerchantID = "merchant-other"
		},
		"runtime account": func(_ *core.RefundNotificationResult, _ *wafforefund.InquiryRefundData, _ *waffoorder.InquiryOrderData, _ *model.TopUp, cfg *setting.WaffoConfig) {
			cfg.MerchantID = "merchant-other"
		},
		"local provider payment": func(_ *core.RefundNotificationResult, _ *wafforefund.InquiryRefundData, _ *waffoorder.InquiryOrderData, topUp *model.TopUp, _ *setting.WaffoConfig) {
			providerTradeNo := "ACQ_other"
			topUp.ProviderTradeNo = &providerTradeNo
		},
		"local payment status": func(_ *core.RefundNotificationResult, _ *wafforefund.InquiryRefundData, _ *waffoorder.InquiryOrderData, topUp *model.TopUp, _ *setting.WaffoConfig) {
			topUp.Status = common.TopUpStatusPending
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			notification, refundEvidence, orderEvidence, topUp, cfg := validWaffoRefundEvidence()
			mutate(notification, refundEvidence, orderEvidence, topUp, &cfg)
			_, _, err := buildWaffoRefundEventInput(notification, refundEvidence, orderEvidence, topUp, cfg, `{"eventType":"REFUND_NOTIFICATION"}`)
			require.Error(t, err)
		})
	}
}

func setupWaffoRefundHandlerTest(t *testing.T) (*model.User, *model.TopUp, *core.RefundNotificationResult, *wafforefund.InquiryRefundData, *waffoorder.InquiryOrderData, setting.WaffoConfig) {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "waffo-refund.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{},
		&model.TopUp{},
		&model.PaymentEvent{},
		&model.ProviderPaymentBinding{},
		&model.ProviderRefundEvent{},
		&model.ProviderReversalEffect{},
		&model.BillingOperation{},
		&model.QuotaCacheRepair{},
	))
	model.DB, model.LOG_DB = db, db
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	notification, refundEvidence, orderEvidence, topUp, cfg := validWaffoRefundEvidence()
	user := &model.User{Id: topUp.UserId, Username: "waffo-refund-user", Quota: topUp.CreditedQuota, Group: "default"}
	require.NoError(t, db.Create(user).Error)
	require.NoError(t, db.Create(topUp).Error)
	require.NoError(t, model.BindProviderPaymentBindings(model.ProviderPaymentBindingBatch{
		Provider:            model.PaymentProviderWaffo,
		ProviderAccountID:   cfg.MerchantID,
		ProviderEnvironment: "sandbox",
		OrderTradeNo:        topUp.TradeNo,
		OrderKind:           model.PaymentEventOrderTopUp,
		Objects: []model.ProviderPaymentObject{{
			ObjectType: model.ProviderPaymentObjectAcquiringOrder,
			ObjectID:   notification.AcquiringOrderID,
		}},
	}))
	return user, topUp, notification, refundEvidence, orderEvidence, cfg
}

func waffoRefundTestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, *core.WebhookHandler) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/waffo/webhook", nil)
	return ctx, recorder, core.NewWebhookHandler(&config.WaffoConfig{})
}

func TestHandleWaffoRefundAppliesVerifiedSingleFullRefund(t *testing.T) {
	user, _, notification, refundEvidence, orderEvidence, cfg := setupWaffoRefundHandlerTest(t)
	ctx, recorder, handler := waffoRefundTestContext(t)
	loadEvidence := func(context.Context, *waffo.Waffo, *core.RefundNotificationResult) (*wafforefund.InquiryRefundData, *waffoorder.InquiryOrderData, error) {
		return refundEvidence, orderEvidence, nil
	}

	handleWaffoRefundWithEvidenceLoader(ctx, handler, nil, cfg, notification, `{"eventType":"REFUND_NOTIFICATION","result":{"refundRequestId":"refund-request-1"}}`, loadEvidence)
	require.Equal(t, http.StatusOK, recorder.Code)
	var gotUser model.User
	require.NoError(t, model.DB.First(&gotUser, user.Id).Error)
	assert.Zero(t, gotUser.Quota)
	var delivery model.ProviderRefundEvent
	require.NoError(t, model.DB.Where("business_event_id = ?", notification.RefundRequestID).First(&delivery).Error)
	assert.Equal(t, model.ProviderRefundEventApplied, delivery.Status)
}

func TestHandleWaffoRefundKeepsPartialRefundManual(t *testing.T) {
	user, _, notification, refundEvidence, orderEvidence, cfg := setupWaffoRefundHandlerTest(t)
	notification.RefundStatus = core.RefundStatusPartiallyRefunded
	notification.RefundAmount = "2.00"
	notification.RemainingRefundAmount = "6.00"
	refundEvidence.RefundStatus = notification.RefundStatus
	refundEvidence.RefundAmount = notification.RefundAmount
	refundEvidence.RemainingRefundAmount = notification.RemainingRefundAmount
	ctx, recorder, handler := waffoRefundTestContext(t)
	loadEvidence := func(context.Context, *waffo.Waffo, *core.RefundNotificationResult) (*wafforefund.InquiryRefundData, *waffoorder.InquiryOrderData, error) {
		return refundEvidence, orderEvidence, nil
	}

	handleWaffoRefundWithEvidenceLoader(ctx, handler, nil, cfg, notification, `{"eventType":"REFUND_NOTIFICATION","result":{"refundStatus":"ORDER_PARTIALLY_REFUNDED"}}`, loadEvidence)
	require.Equal(t, http.StatusOK, recorder.Code)
	var gotUser model.User
	require.NoError(t, model.DB.First(&gotUser, user.Id).Error)
	assert.Equal(t, user.Quota, gotUser.Quota)
	var delivery model.ProviderRefundEvent
	require.NoError(t, model.DB.Where("business_event_id = ?", notification.RefundRequestID).First(&delivery).Error)
	assert.Equal(t, model.ProviderRefundEventManualReconciliation, delivery.Status)
	assert.Equal(t, model.ProviderRefundManualReasonPartialRefundUnsupported, delivery.DecisionReason)
	var effects int64
	require.NoError(t, model.DB.Model(&model.ProviderReversalEffect{}).Count(&effects).Error)
	assert.Zero(t, effects)
}

func TestHandleWaffoRefundKeepsLegacyOrderWithoutBindingManual(t *testing.T) {
	user, _, notification, refundEvidence, orderEvidence, cfg := setupWaffoRefundHandlerTest(t)
	require.NoError(t, model.DB.Where("provider = ?", model.PaymentProviderWaffo).Delete(&model.ProviderPaymentBinding{}).Error)
	ctx, recorder, handler := waffoRefundTestContext(t)
	loadEvidence := func(context.Context, *waffo.Waffo, *core.RefundNotificationResult) (*wafforefund.InquiryRefundData, *waffoorder.InquiryOrderData, error) {
		return refundEvidence, orderEvidence, nil
	}

	handleWaffoRefundWithEvidenceLoader(ctx, handler, nil, cfg, notification, `{"eventType":"REFUND_NOTIFICATION","result":{"refundStatus":"ORDER_FULLY_REFUNDED"}}`, loadEvidence)
	require.Equal(t, http.StatusOK, recorder.Code)
	var gotUser model.User
	require.NoError(t, model.DB.First(&gotUser, user.Id).Error)
	assert.Equal(t, user.Quota, gotUser.Quota)
	var delivery model.ProviderRefundEvent
	require.NoError(t, model.DB.Where("business_event_id = ?", notification.RefundRequestID).First(&delivery).Error)
	assert.Equal(t, model.ProviderRefundEventManualReconciliation, delivery.Status)
	assert.Equal(t, model.ProviderRefundManualReasonPaymentBindingMissing, delivery.DecisionReason)
	var effects int64
	require.NoError(t, model.DB.Model(&model.ProviderReversalEffect{}).Count(&effects).Error)
	assert.Zero(t, effects)
}

func TestHandleWaffoRefundPersistsManualObservationWhenInquiryFails(t *testing.T) {
	user, _, notification, _, _, cfg := setupWaffoRefundHandlerTest(t)
	ctx, recorder, handler := waffoRefundTestContext(t)
	loadEvidence := func(context.Context, *waffo.Waffo, *core.RefundNotificationResult) (*wafforefund.InquiryRefundData, *waffoorder.InquiryOrderData, error) {
		return nil, nil, errors.New("provider inquiry unavailable")
	}

	handleWaffoRefundWithEvidenceLoader(ctx, handler, nil, cfg, notification, `{"eventType":"REFUND_NOTIFICATION","result":{"refundStatus":"ORDER_FULLY_REFUNDED"}}`, loadEvidence)
	require.Equal(t, http.StatusOK, recorder.Code)
	var gotUser model.User
	require.NoError(t, model.DB.First(&gotUser, user.Id).Error)
	assert.Equal(t, user.Quota, gotUser.Quota)
	var delivery model.ProviderRefundEvent
	require.NoError(t, model.DB.Where("business_event_id = ?", notification.RefundRequestID).First(&delivery).Error)
	assert.Equal(t, model.ProviderRefundEventManualReconciliation, delivery.Status)
	assert.Equal(t, "missing_reversal_evidence", delivery.DecisionReason)
}

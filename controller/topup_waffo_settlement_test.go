package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/require"
	"github.com/waffo-com/waffo-go/core"
)

func validWaffoSettlementConfig() setting.WaffoConfig {
	return setting.WaffoConfig{MerchantID: "merchant_test", Sandbox: true}
}

func validWaffoTopUpAndNotification() (*model.TopUp, *core.PaymentNotificationResult) {
	topUp := &model.TopUp{
		UserId:             42,
		TradeNo:            "WAFFO-42-1-abc123",
		PaymentProvider:    model.PaymentProviderWaffo,
		PaymentMethod:      model.PaymentMethodWaffo,
		Status:             common.TopUpStatusPending,
		CreditedQuota:      500,
		ProviderMerchantID: "merchant_test",
		ProviderOrderName:  "Recharge 500 credits",
		ProviderAmount:     "8.00",
		ProviderProductID:  "ONE_TIME_PAYMENT",
		ProviderCurrency:   "USD",
		ProviderCheckoutID: "ACQ-123",
	}
	notification := &core.PaymentNotificationResult{
		PaymentRequestID: "WAFFO-42-1-abc123",
		MerchantOrderID:  "WAFFO-42-1-abc123",
		AcquiringOrderID: "ACQ-123",
		OrderStatus:      core.OrderStatusPaySuccess,
		OrderCurrency:    "usd",
		OrderAmount:      "8.00",
		OrderDescription: "Recharge 500 credits",
		MerchantInfo:     map[string]interface{}{"merchantId": "merchant_test"},
		PaymentInfo:      map[string]interface{}{"productName": "ONE_TIME_PAYMENT"},
		GoodsInfo:        map[string]interface{}{"goodsName": "Recharge 500 credits"},
	}
	return topUp, notification
}

func TestBuildWaffoProviderSettlementBindsImmutableSnapshot(t *testing.T) {
	topUp, notification := validWaffoTopUpAndNotification()
	settlement, err := buildWaffoProviderSettlement(notification, topUp, validWaffoSettlementConfig())
	require.NoError(t, err)
	require.Equal(t, model.PaymentProviderWaffo, settlement.Provider)
	require.Equal(t, topUp.TradeNo, settlement.OrderTradeNo)
	require.Equal(t, topUp.ProviderCheckoutID, settlement.ProviderTradeNo)
	require.Equal(t, "merchant_test", settlement.ProviderAccountID)
	require.Equal(t, "sandbox", settlement.ProviderEnvironment)
	require.Equal(t, []model.ProviderPaymentObject{{
		ObjectType: model.ProviderPaymentObjectAcquiringOrder,
		ObjectID:   topUp.ProviderCheckoutID,
	}}, settlement.PaymentObjects)
	require.Equal(t, "USD", settlement.Currency)
	require.Equal(t, "8.00", settlement.Amount)
}

func TestBuildWaffoProviderSettlementRejectsMismatches(t *testing.T) {
	mutations := map[string]func(*model.TopUp, *core.PaymentNotificationResult){
		"payment request id": func(_ *model.TopUp, n *core.PaymentNotificationResult) {
			n.PaymentRequestID = "other-order"
		},
		"merchant order id": func(_ *model.TopUp, n *core.PaymentNotificationResult) {
			n.MerchantOrderID = "other-order"
		},
		"acquiring order id": func(_ *model.TopUp, n *core.PaymentNotificationResult) {
			n.AcquiringOrderID = "other-acquiring-order"
		},
		"amount": func(_ *model.TopUp, n *core.PaymentNotificationResult) {
			n.OrderAmount = "7.99"
		},
		"currency": func(_ *model.TopUp, n *core.PaymentNotificationResult) {
			n.OrderCurrency = "EUR"
		},
		"description": func(_ *model.TopUp, n *core.PaymentNotificationResult) {
			n.OrderDescription = "different product"
		},
		"merchant": func(_ *model.TopUp, n *core.PaymentNotificationResult) {
			n.MerchantInfo["merchantId"] = "other-merchant"
		},
		"product": func(_ *model.TopUp, n *core.PaymentNotificationResult) {
			n.PaymentInfo["productName"] = "SUBSCRIPTION"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			topUp, notification := validWaffoTopUpAndNotification()
			mutate(topUp, notification)
			_, err := buildWaffoProviderSettlement(notification, topUp, validWaffoSettlementConfig())
			require.Error(t, err)
		})
	}
}

func TestBuildWaffoProviderSettlementRequiresCompleteSnapshot(t *testing.T) {
	topUp, notification := validWaffoTopUpAndNotification()
	topUp.ProviderCheckoutID = ""
	_, err := buildWaffoProviderSettlement(notification, topUp, validWaffoSettlementConfig())
	require.ErrorIs(t, err, model.ErrProviderSnapshotMissing)

	topUp, notification = validWaffoTopUpAndNotification()
	notification.PaymentInfo = nil
	_, err = buildWaffoProviderSettlement(notification, topUp, validWaffoSettlementConfig())
	require.Error(t, err)
}

func TestBuildWaffoProviderSettlementAllowsSignedTerminalRedelivery(t *testing.T) {
	for _, status := range []string{common.TopUpStatusSuccess, common.TopUpStatusFailed, common.TopUpStatusExpired} {
		t.Run(status, func(t *testing.T) {
			topUp, notification := validWaffoTopUpAndNotification()
			topUp.Status = status
			_, err := buildWaffoProviderSettlement(notification, topUp, validWaffoSettlementConfig())
			require.NoError(t, err)
		})
	}
}

func TestValidateWaffoCloseNotificationAllowsOmittedPaymentSnapshot(t *testing.T) {
	topUp, notification := validWaffoTopUpAndNotification()
	notification.PaymentRequestID = ""
	notification.AcquiringOrderID = ""
	notification.OrderAmount = ""
	notification.OrderCurrency = ""
	notification.OrderDescription = ""
	notification.MerchantInfo = nil
	notification.PaymentInfo = nil
	notification.GoodsInfo = nil
	notification.OrderStatus = core.OrderStatusOrderClose

	// Waffo failure/expiry callbacks may contain only merchantOrderId and
	// orderStatus. Closing an order must not require the payment snapshot that
	// is needed for a successful credit.
	require.NoError(t, validateWaffoCloseNotification(notification, topUp))
}

func TestValidateWaffoCloseNotificationAcceptsPaymentRequestIDFallback(t *testing.T) {
	topUp, notification := validWaffoTopUpAndNotification()
	notification.MerchantOrderID = ""
	notification.AcquiringOrderID = ""
	notification.PaymentRequestID = topUp.TradeNo
	notification.OrderStatus = core.OrderStatusOrderClose

	require.NoError(t, validateWaffoCloseNotification(notification, topUp))
}

func TestValidateWaffoCloseNotificationRejectsContradictoryIdentity(t *testing.T) {
	topUp, notification := validWaffoTopUpAndNotification()
	notification.OrderStatus = core.OrderStatusOrderClose

	tests := map[string]func(*core.PaymentNotificationResult){
		"merchant order": func(result *core.PaymentNotificationResult) {
			result.MerchantOrderID = "other-order"
		},
		"payment request": func(result *core.PaymentNotificationResult) {
			result.PaymentRequestID = "other-order"
		},
		"acquiring order": func(result *core.PaymentNotificationResult) {
			result.AcquiringOrderID = "other-acquiring"
		},
		"amount": func(result *core.PaymentNotificationResult) {
			result.OrderAmount = "0.01"
		},
		"currency": func(result *core.PaymentNotificationResult) {
			result.OrderCurrency = "EUR"
		},
		"description": func(result *core.PaymentNotificationResult) {
			result.OrderDescription = "other product"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			_, result := validWaffoTopUpAndNotification()
			result.OrderStatus = core.OrderStatusOrderClose
			mutate(result)
			require.Error(t, validateWaffoCloseNotification(result, topUp))
		})
	}
}

func TestValidateWaffoCloseNotificationRequiresAnOrderReference(t *testing.T) {
	topUp, notification := validWaffoTopUpAndNotification()
	notification.MerchantOrderID = ""
	notification.PaymentRequestID = ""
	notification.OrderStatus = core.OrderStatusOrderClose
	require.ErrorIs(t, validateWaffoCloseNotification(notification, topUp), errWaffoCallbackInvalid)
}

func TestWaffoPaymentStatusClassificationKeepsProgressPending(t *testing.T) {
	for _, status := range []string{
		core.OrderStatusPayInProgress,
		core.OrderStatusAuthorizationRequired,
		core.OrderStatusAuthedWaitingCapture,
		"CAPTURE_IN_PROGRESS",
	} {
		require.True(t, isWaffoPendingPaymentStatus(status), status)
		require.True(t, isWaffoPaymentStatus(status), status)
	}
	require.False(t, isWaffoPendingPaymentStatus(core.OrderStatusOrderClose))
	require.False(t, isWaffoPaymentStatus("UNKNOWN_STATUS"))
}

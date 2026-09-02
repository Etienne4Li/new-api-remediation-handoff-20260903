package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests model the provider request/webhook interleaving that used to
// let a stale full-row Save roll a settled order back to pending.
func TestSetTopUpProviderCheckoutIDPreservesConcurrentSettlement(t *testing.T) {
	truncateTables(t)
	user := &User{Id: 811, Username: "checkout-race-topup", Status: common.UserStatusEnabled}
	require.NoError(t, DB.Create(user).Error)
	order := &TopUp{
		UserId:             user.Id,
		TradeNo:            "checkout-race-topup",
		PaymentMethod:      PaymentMethodStripe,
		PaymentProvider:    PaymentProviderStripe,
		Status:             common.TopUpStatusPending,
		CreateTime:         time.Now().Unix(),
		CreditedQuota:      100,
		ProviderCheckoutID: "",
	}
	require.NoError(t, DB.Create(order).Error)

	// Hold the stale object exactly as the checkout handler does. A webhook
	// then commits the terminal state and provider transaction.
	stale := GetTopUpByTradeNo(order.TradeNo)
	require.NotNil(t, stale)
	require.NoError(t, DB.Model(&TopUp{}).Where("trade_no = ?", order.TradeNo).Updates(map[string]interface{}{
		"status":            common.TopUpStatusSuccess,
		"provider_trade_no": "pi_race_1",
	}).Error)

	require.NoError(t, SetTopUpProviderCheckoutID(order.TradeNo, PaymentProviderStripe, "cs_race_1"))
	stored := GetTopUpByTradeNo(order.TradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, common.TopUpStatusSuccess, stored.Status)
	require.NotNil(t, stored.ProviderTradeNo)
	assert.Equal(t, "pi_race_1", *stored.ProviderTradeNo)
	assert.Equal(t, "cs_race_1", stored.ProviderCheckoutID)
	assert.Equal(t, common.TopUpStatusPending, stale.Status, "fixture must retain the stale pending snapshot")

	assert.ErrorIs(t, SetTopUpProviderCheckoutID(order.TradeNo, PaymentProviderStripe, "cs_other"), ErrProviderCheckoutConflict)
	assert.Equal(t, "cs_race_1", GetTopUpByTradeNo(order.TradeNo).ProviderCheckoutID)
}

func TestSetSubscriptionOrderProviderCheckoutIDPreservesConcurrentSettlement(t *testing.T) {
	truncateTables(t)
	user := &User{Id: 812, Username: "checkout-race-subscription", Status: common.UserStatusEnabled}
	require.NoError(t, DB.Create(user).Error)
	plan := &SubscriptionPlan{
		Id:            813,
		Title:         "Checkout race plan",
		PriceAmount:   10,
		Currency:      "USD",
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		Enabled:       true,
		TotalAmount:   100,
	}
	require.NoError(t, DB.Create(plan).Error)
	order := &SubscriptionOrder{
		UserId:          user.Id,
		PlanId:          plan.Id,
		TradeNo:         "checkout-race-subscription",
		PaymentMethod:   PaymentMethodCreem,
		PaymentProvider: PaymentProviderCreem,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, DB.Create(order).Error)

	stale := GetSubscriptionOrderByTradeNo(order.TradeNo)
	require.NotNil(t, stale)
	require.NoError(t, DB.Model(&SubscriptionOrder{}).Where("trade_no = ?", order.TradeNo).Updates(map[string]interface{}{
		"status":            common.TopUpStatusSuccess,
		"provider_trade_no": "creem_race_1",
	}).Error)

	require.NoError(t, SetSubscriptionOrderProviderCheckoutID(order.TradeNo, PaymentProviderCreem, "creem_checkout_1"))
	stored := GetSubscriptionOrderByTradeNo(order.TradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, common.TopUpStatusSuccess, stored.Status)
	require.NotNil(t, stored.ProviderTradeNo)
	assert.Equal(t, "creem_race_1", *stored.ProviderTradeNo)
	assert.Equal(t, "creem_checkout_1", stored.ProviderCheckoutID)
	assert.Equal(t, common.TopUpStatusPending, stale.Status, "fixture must retain the stale pending snapshot")

	assert.ErrorIs(t, SetSubscriptionOrderProviderCheckoutID(order.TradeNo, PaymentProviderCreem, "creem_checkout_other"), ErrProviderCheckoutConflict)
	assert.Equal(t, "creem_checkout_1", GetSubscriptionOrderByTradeNo(order.TradeNo).ProviderCheckoutID)
}

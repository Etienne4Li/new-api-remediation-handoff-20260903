package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionEntitlementSnapshotFreezesPaidOrderBenefits(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 701, 0)
	allowOverflow := false
	plan := &SubscriptionPlan{
		Id:                      702,
		Title:                   "Snapshot Plan",
		PriceAmount:             19.99,
		Currency:                "USD",
		DurationUnit:            SubscriptionDurationMonth,
		DurationValue:           1,
		Enabled:                 true,
		TotalAmount:             12345,
		QuotaResetPeriod:        SubscriptionResetWeekly,
		QuotaResetCustomSeconds: 0,
		AllowWalletOverflow:     &allowOverflow,
		MaxPurchasePerUser:      2,
		UpgradeGroup:            "vip",
		DowngradeGroup:          "default",
		StripePriceId:           "price_snapshot_v1",
		CreemProductId:          "prod_snapshot_v1",
		WaffoPancakeProductId:   "pancake_snapshot_v1",
		UpdatedAt:               time.Now().Unix(),
	}
	require.NoError(t, DB.Create(plan).Error)

	order := &SubscriptionOrder{
		UserId:          user.Id,
		PlanId:          plan.Id,
		Money:           plan.PriceAmount,
		TradeNo:         "snapshot-order-1",
		PaymentMethod:   PaymentMethodStripe,
		PaymentProvider: PaymentProviderStripe,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, order.SetEntitlementSnapshot(plan))
	require.NoError(t, order.Insert())

	// A later admin edit must not change what this already-created checkout buys.
	require.NoError(t, DB.Model(&SubscriptionPlan{}).Where("id = ?", plan.Id).Updates(map[string]interface{}{
		"title":                 "Changed Plan",
		"price_amount":          1.0,
		"duration_unit":         SubscriptionDurationDay,
		"duration_value":        1,
		"total_amount":          1,
		"quota_reset_period":    SubscriptionResetNever,
		"allow_wallet_overflow": true,
		"upgrade_group":         "changed",
		"downgrade_group":       "changed",
		"stripe_price_id":       "price_changed",
	}).Error)

	require.NoError(t, CompleteSubscriptionOrder(order.TradeNo, "stripe-event", PaymentProviderStripe, PaymentMethodStripe))

	var subscription UserSubscription
	require.NoError(t, DB.Where("user_id = ?", user.Id).First(&subscription).Error)
	assert.Equal(t, int64(12345), subscription.AmountTotal)
	assert.Equal(t, "vip", subscription.UpgradeGroup)
	assert.Equal(t, "default", subscription.DowngradeGroup)
	assert.False(t, subscription.AllowWalletOverflow)
	assert.Equal(t, "active", subscription.Status)

	stored := GetSubscriptionOrderByTradeNo(order.TradeNo)
	require.NotNil(t, stored)
	assert.NotEmpty(t, stored.EntitlementSnapshot)
}

func TestSubscriptionEntitlementSnapshotRejectsMissingAndCorruptVersions(t *testing.T) {
	missing := &SubscriptionOrder{}
	_, err := missing.GetEntitlementSnapshot()
	require.ErrorIs(t, err, ErrSubscriptionEntitlementSnapshotMissing)

	corrupt := &SubscriptionOrder{EntitlementSnapshot: `{"version":99,"plan_id":1}`}
	_, err = corrupt.GetEntitlementSnapshot()
	require.ErrorIs(t, err, ErrSubscriptionEntitlementSnapshotInvalid)
}

func TestCompleteSubscriptionOrderRejectsPendingOrderWithoutEntitlementSnapshot(t *testing.T) {
	truncateTables(t)
	user := insertUserForPaymentGuardTest(t, 703, 0)
	plan := insertSubscriptionPlanForPaymentGuardTest(t, 704)
	order := &SubscriptionOrder{
		UserId:          user.Id,
		PlanId:          plan.Id,
		Money:           plan.PriceAmount,
		TradeNo:         "snapshot-order-missing",
		PaymentMethod:   PaymentMethodStripe,
		PaymentProvider: PaymentProviderStripe,
		Status:          common.TopUpStatusPending,
		CreateTime:      time.Now().Unix(),
	}
	require.NoError(t, order.Insert())

	err := CompleteSubscriptionOrder(order.TradeNo, "provider-event", PaymentProviderStripe, PaymentMethodStripe)
	require.ErrorIs(t, err, ErrSubscriptionEntitlementSnapshotMissing)
	assert.Zero(t, countUserSubscriptionsForPaymentGuardTest(t, user.Id))
}

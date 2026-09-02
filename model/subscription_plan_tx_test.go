package model

import (
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A plan read made through a transaction must observe writes made through that
// transaction and must not leak those writes into the process cache if the
// transaction rolls back.  This is important for balance purchases and admin
// reset operations, both of which derive entitlements from a plan inside a
// database transaction.
func TestGetSubscriptionPlanByIdTxUsesTransactionStateWithoutCachePollution(t *testing.T) {
	truncateTables(t)
	const planID = 98101
	plan := &SubscriptionPlan{
		Id:            planID,
		Title:         "original plan",
		Currency:      "USD",
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		Enabled:       true,
		TotalAmount:   100,
	}
	require.NoError(t, DB.Create(plan).Error)
	InvalidateSubscriptionPlanCache(planID)

	// Prime the non-transactional cache with the committed value.
	cached, err := getSubscriptionPlanByIdTx(nil, planID)
	require.NoError(t, err)
	require.Equal(t, "original plan", cached.Title)

	rollbackErr := errors.New("intentional rollback")
	err = DB.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Model(&SubscriptionPlan{}).
			Where("id = ?", planID).
			Updates(map[string]interface{}{"title": "transaction plan"}).Error)

		inside, readErr := getSubscriptionPlanByIdTx(tx, planID)
		require.NoError(t, readErr)
		require.Equal(t, "transaction plan", inside.Title,
			"transactional reads must not use the stale global cache")
		return rollbackErr
	})
	require.ErrorIs(t, err, rollbackErr)

	// The rollback leaves the committed value intact, and because the
	// transactional read never populated the cache, a subsequent cache hit must
	// still return the original value.
	after, err := GetSubscriptionPlanById(planID)
	require.NoError(t, err)
	require.Equal(t, "original plan", after.Title,
		"uncommitted transactional plan must never survive in cache")
}

func TestGetSubscriptionPlanByIdCheckoutReadBypassesStaleCache(t *testing.T) {
	truncateTables(t)
	const planID = 98110
	plan := &SubscriptionPlan{
		Id:            planID,
		Title:         "cached title",
		Currency:      "USD",
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		Enabled:       true,
		TotalAmount:   100,
	}
	require.NoError(t, DB.Create(plan).Error)
	InvalidateSubscriptionPlanCache(planID)
	// Prime the cache through the explicitly cacheable internal helper.
	_, err := getSubscriptionPlanByIdTx(nil, planID)
	require.NoError(t, err)
	require.NoError(t, DB.Model(&SubscriptionPlan{}).Where("id = ?", planID).
		Updates(map[string]interface{}{"title": "fresh title", "total_amount": int64(200)}).Error)

	fresh, err := GetSubscriptionPlanById(planID)
	require.NoError(t, err)
	require.Equal(t, "fresh title", fresh.Title)
	require.EqualValues(t, 200, fresh.TotalAmount)
}

func TestGetSubscriptionPlanByIdTxRejectsNilDatabase(t *testing.T) {
	previousDB := DB
	DB = nil
	t.Cleanup(func() { DB = previousDB })

	_, err := GetSubscriptionPlanById(98102)
	require.ErrorIs(t, err, ErrDatabase)
}

func TestAdminBindSubscriptionReadsPlanInsideTransaction(t *testing.T) {
	truncateTables(t)
	const (
		userID = 98103
		planID = 98104
	)
	require.NoError(t, DB.Create(&User{Id: userID, Username: "admin-bind-tx-user"}).Error)
	plan := &SubscriptionPlan{
		Id:            planID,
		Title:         "old title",
		Currency:      "USD",
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		Enabled:       true,
		TotalAmount:   100,
	}
	require.NoError(t, DB.Create(plan).Error)
	InvalidateSubscriptionPlanCache(planID)
	// Prime the cache, then update the database directly (as a concurrent admin
	// edit would).  The bind must not use the stale pre-edit cache entry.
	cached, err := GetSubscriptionPlanById(planID)
	require.NoError(t, err)
	require.Equal(t, "old title", cached.Title)
	require.NoError(t, DB.Model(&SubscriptionPlan{}).Where("id = ?", planID).
		Updates(map[string]interface{}{"title": "new title", "total_amount": int64(999)}).Error)

	_, err = AdminBindSubscription(userID, planID, "")
	require.NoError(t, err)
	var sub UserSubscription
	require.NoError(t, DB.Where("user_id = ?", userID).First(&sub).Error)
	require.EqualValues(t, 999, sub.AmountTotal,
		"manual bind must use the transaction-consistent plan")
}

func TestSubscriptionQuotaResetUsesCreationSnapshotAfterPlanEdit(t *testing.T) {
	truncateTables(t)
	const (
		userID = 98105
		planID = 98106
	)
	require.NoError(t, DB.Create(&User{Id: userID, Username: "reset-snapshot-user"}).Error)
	plan := &SubscriptionPlan{
		Id:               planID,
		Title:            "daily reset",
		Currency:         "USD",
		DurationUnit:     SubscriptionDurationMonth,
		DurationValue:    1,
		Enabled:          true,
		TotalAmount:      1000,
		QuotaResetPeriod: SubscriptionResetDaily,
	}
	require.NoError(t, DB.Create(plan).Error)

	// CreateUserSubscriptionFromPlanTx is the same path used by paid/admin
	// settlement and should persist the reset policy at that point in time.
	var created *UserSubscription
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error {
		var err error
		created, err = CreateUserSubscriptionFromPlanTx(tx, userID, plan, "test")
		return err
	}))
	require.NotZero(t, created.Id)
	require.Equal(t, SubscriptionResetDaily, created.QuotaResetPeriod)

	// Changing the plan must not turn an already purchased daily entitlement
	// into a never-reset entitlement.
	require.NoError(t, DB.Model(&SubscriptionPlan{}).Where("id = ?", planID).
		Updates(map[string]interface{}{
			"quota_reset_period":         SubscriptionResetNever,
			"quota_reset_custom_seconds": int64(0),
		}).Error)
	InvalidateSubscriptionPlanCache(planID)

	now := time.Now().Unix()
	require.NoError(t, DB.Model(&UserSubscription{}).Where("id = ?", created.Id).
		Updates(map[string]interface{}{
			"amount_used":     int64(700),
			"last_reset_time": now - 2*24*3600,
			"next_reset_time": now - 1,
			"status":          "active",
			"end_time":        now + 24*3600,
		}).Error)

	count, err := ResetDueSubscriptions(10)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	var got UserSubscription
	require.NoError(t, DB.Where("id = ?", created.Id).First(&got).Error)
	require.Zero(t, got.AmountUsed,
		"reset worker must honor the immutable creation policy")
	require.Equal(t, SubscriptionResetDaily, got.QuotaResetPeriod)
}

func TestCompleteSubscriptionOrderRejectsExistingTopUpTradeNumber(t *testing.T) {
	truncateTables(t)
	const (
		userID = 98108
		planID = 98109
	)
	require.NoError(t, DB.Create(&User{Id: userID, Username: "cross-ledger-user"}).Error)
	plan := &SubscriptionPlan{
		Id:            planID,
		Title:         "cross-ledger plan",
		PriceAmount:   10,
		Currency:      "USD",
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		Enabled:       true,
		TotalAmount:   500,
	}
	require.NoError(t, DB.Create(plan).Error)
	const tradeNo = "cross-ledger-trade"
	order := &SubscriptionOrder{
		UserId:          userID,
		PlanId:          planID,
		Money:           plan.PriceAmount,
		TradeNo:         tradeNo,
		PaymentMethod:   PaymentMethodStripe,
		PaymentProvider: PaymentProviderStripe,
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, order.SetEntitlementSnapshot(plan))
	require.NoError(t, DB.Create(order).Error)
	topUp := &TopUp{
		UserId:          userID + 1,
		Amount:          20,
		CreditedQuota:   200,
		Money:           20,
		TradeNo:         tradeNo,
		PaymentMethod:   PaymentMethodStripe,
		PaymentProvider: PaymentProviderStripe,
		Status:          common.TopUpStatusPending,
	}
	// This models a legacy database where cross-table trade_no uniqueness was
	// not enforced. The subscription settlement must not mutate this row.
	require.NoError(t, DB.Create(topUp).Error)

	err := CompleteSubscriptionOrder(tradeNo, "provider-event", PaymentProviderStripe, PaymentMethodStripe)
	require.ErrorIs(t, err, ErrSubscriptionOrderConflict)
	var gotOrder SubscriptionOrder
	require.NoError(t, DB.Where("trade_no = ?", tradeNo).First(&gotOrder).Error)
	require.Equal(t, common.TopUpStatusPending, gotOrder.Status)
	var gotTopUp TopUp
	require.NoError(t, DB.Where("trade_no = ?", tradeNo).First(&gotTopUp).Error)
	require.Equal(t, common.TopUpStatusPending, gotTopUp.Status)
	require.EqualValues(t, 200, gotTopUp.CreditedQuota)
	var subCount int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("user_id = ?", userID).Count(&subCount).Error)
	require.Zero(t, subCount)
}

func TestSubscriptionReplayBindsLateProviderSubscriptionIDToExactEntitlement(t *testing.T) {
	truncateTables(t)
	const (
		userID = 98111
		planID = 98112
	)
	require.NoError(t, DB.Create(&User{Id: userID, Username: "late-provider-id-user"}).Error)
	plan := &SubscriptionPlan{
		Id:            planID,
		Title:         "late provider id plan",
		PriceAmount:   10,
		Currency:      "USD",
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		Enabled:       true,
		TotalAmount:   500,
	}
	require.NoError(t, DB.Create(plan).Error)
	const tradeNo = "late-provider-id-order"
	order := &SubscriptionOrder{
		UserId:                 userID,
		PlanId:                 planID,
		Money:                  10,
		TradeNo:                tradeNo,
		PaymentMethod:          PaymentMethodStripe,
		PaymentProvider:        PaymentProviderStripe,
		Status:                 common.TopUpStatusPending,
		ProviderMerchantID:     "acct_test",
		ProviderOrderName:      plan.Title,
		ProviderAmount:         "10.00",
		ProviderProductID:      "price_late",
		ProviderCheckoutID:     "cs_late_provider",
		ProviderCurrency:       "USD",
		ProviderKeyFingerprint: providerSettlementScopeFingerprint(),
	}
	require.NoError(t, order.SetEntitlementSnapshot(plan))
	require.NoError(t, DB.Create(order).Error)
	settlement := ProviderSettlement{
		OrderTradeNo:           tradeNo,
		Provider:               PaymentProviderStripe,
		ProviderTradeNo:        "cs_late_provider",
		ProviderAccountID:      "acct_test",
		ProviderEnvironment:    "test",
		ProviderKeyFingerprint: providerSettlementScopeFingerprint(),
		MerchantID:             "acct_test",
		ProductID:              "price_late",
		CheckoutID:             "cs_late_provider",
		Currency:               "USD",
		Amount:                 "10.00",
		OrderName:              plan.Title,
		Payload:                `{ "id": "evt_checkout_late" }`,
		PaymentObjects: []ProviderPaymentObject{
			{ObjectType: ProviderPaymentObjectCheckoutSession, ObjectID: "cs_late_provider"},
			{ObjectType: ProviderPaymentObjectPaymentIntent, ObjectID: "pi_late_provider"},
		},
	}
	// First delivery lacks the expanded recurring object but still grants the
	// initial entitlement.
	require.NoError(t, CompleteSubscriptionOrderVerified(settlement))
	var sub UserSubscription
	require.NoError(t, DB.Where("user_id = ?", userID).First(&sub).Error)
	require.Equal(t, tradeNo, sub.SubscriptionOrderTradeNo)
	require.Empty(t, sub.ProviderSubscriptionID)

	// A later idempotent delivery includes the recurring ID. It must bind to the
	// exact order-linked row, not merely any same-user/same-plan subscription.
	settlement.ProviderSubscriptionID = "sub_late_provider"
	settlement.PaymentObjects = append(settlement.PaymentObjects, ProviderPaymentObject{
		ObjectType: ProviderPaymentObjectSubscription,
		ObjectID:   settlement.ProviderSubscriptionID,
	})
	require.NoError(t, CompleteSubscriptionOrderVerified(settlement))
	require.NoError(t, DB.Where("id = ?", sub.Id).First(&sub).Error)
	require.Equal(t, "sub_late_provider", sub.ProviderSubscriptionID)
	require.Equal(t, PaymentProviderStripe, sub.ProviderSubscriptionProvider)
	var paymentBindingCount int64
	require.NoError(t, DB.Model(&ProviderPaymentBinding{}).Where("order_trade_no = ?", tradeNo).Count(&paymentBindingCount).Error)
	require.EqualValues(t, 3, paymentBindingCount)

	// The newly bound ID is now discoverable by lifecycle webhooks.
	require.NoError(t, ApplySubscriptionLifecycleEventAtWithOptions(
		PaymentProviderStripe,
		"evt_late_provider_lifecycle",
		"sub_late_provider",
		"past_due",
		time.Now().Unix()+1,
		0,
		"{}",
		SubscriptionLifecycleOptions{ProviderScope: &ProviderLifecycleScope{
			ProviderAccountID:   settlement.ProviderAccountID,
			ProviderEnvironment: settlement.ProviderEnvironment,
		}},
	))
	require.NoError(t, DB.Where("id = ?", sub.Id).First(&sub).Error)
	require.Equal(t, "past_due", sub.Status)
}

func TestSubscriptionSettlementDoesNotBindLegacyStripeOrderWithoutCheckoutID(t *testing.T) {
	truncateTables(t)
	const (
		userID  = 98121
		planID  = 98122
		tradeNo = "subscription-missing-checkout-order"
	)
	require.NoError(t, DB.Create(&User{Id: userID, Username: "subscription-missing-checkout-user"}).Error)
	plan := &SubscriptionPlan{
		Id:            planID,
		Title:         "missing checkout plan",
		PriceAmount:   10,
		Currency:      "USD",
		DurationUnit:  SubscriptionDurationMonth,
		DurationValue: 1,
		Enabled:       true,
		TotalAmount:   500,
	}
	require.NoError(t, DB.Create(plan).Error)
	order := &SubscriptionOrder{
		UserId:             userID,
		PlanId:             planID,
		Money:              10,
		TradeNo:            tradeNo,
		PaymentMethod:      PaymentMethodStripe,
		PaymentProvider:    PaymentProviderStripe,
		Status:             common.TopUpStatusPending,
		ProviderMerchantID: "acct_test",
		ProviderOrderName:  plan.Title,
		ProviderAmount:     "10.00",
		ProviderProductID:  "price_missing_checkout",
		ProviderCurrency:   "USD",
	}
	require.NoError(t, order.SetEntitlementSnapshot(plan))
	require.NoError(t, DB.Create(order).Error)

	settlement := ProviderSettlement{
		OrderTradeNo:           tradeNo,
		Provider:               PaymentProviderStripe,
		ProviderTradeNo:        "cs_missing_checkout",
		ProviderEventID:        "evt_missing_checkout",
		ProviderAccountID:      "acct_test",
		ProviderEnvironment:    "test",
		ProviderKeyFingerprint: providerSettlementScopeFingerprint(),
		MerchantID:             "acct_test",
		ProductID:              "price_missing_checkout",
		CheckoutID:             "cs_missing_checkout",
		Currency:               "USD",
		Amount:                 "10.00",
		OrderName:              plan.Title,
		Payload:                `{"id":"evt_missing_checkout"}`,
		PaymentObjects: []ProviderPaymentObject{{
			ObjectType: ProviderPaymentObjectCheckoutSession,
			ObjectID:   "cs_missing_checkout",
		}},
	}
	require.ErrorIs(t, CompleteSubscriptionOrderVerified(settlement), ErrProviderSnapshotMissing)
	assert.Equal(t, common.TopUpStatusPending, GetSubscriptionOrderByTradeNo(tradeNo).Status)

	var entitlementCount int64
	require.NoError(t, DB.Model(&UserSubscription{}).Where("subscription_order_trade_no = ?", tradeNo).Count(&entitlementCount).Error)
	assert.Zero(t, entitlementCount)
	var eventCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).Where("order_trade_no = ?", tradeNo).Count(&eventCount).Error)
	assert.Zero(t, eventCount)
	var bindingCount int64
	require.NoError(t, DB.Model(&ProviderPaymentBinding{}).Where("order_trade_no = ?", tradeNo).Count(&bindingCount).Error)
	assert.Zero(t, bindingCount)
}

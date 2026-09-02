package model

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type paidUncreditedFixture struct {
	order      *SubscriptionOrder
	settlement ProviderSettlement
	existing   *UserSubscription
}

func newPaidUncreditedFixture(t *testing.T) paidUncreditedFixture {
	t.Helper()
	truncateTables(t)
	const (
		userID = 98201
		planID = 98202
	)
	user := insertUserForPaymentGuardTest(t, userID, 0)
	plan := &SubscriptionPlan{
		Id:                 planID,
		Title:              "paid-uncredited plan",
		PriceAmount:        10,
		Currency:           "USD",
		DurationUnit:       SubscriptionDurationMonth,
		DurationValue:      1,
		Enabled:            true,
		TotalAmount:        1000,
		MaxPurchasePerUser: 1,
		StripePriceId:      "price-paid-uncredited",
	}
	require.NoError(t, DB.Create(plan).Error)

	// Occupy the lifetime purchase slot.  The second authenticated payment must
	// be retained as paid, even though this entitlement cannot be created yet.
	existing := &UserSubscription{
		UserId: user.Id, PlanId: plan.Id, AmountTotal: plan.TotalAmount,
		StartTime: time.Now().Add(-time.Hour).Unix(), EndTime: time.Now().Add(time.Hour).Unix(),
		Status: "active", Source: "order",
		SubscriptionOrderTradeNo: "existing-entitlement",
	}
	require.NoError(t, DB.Create(existing).Error)

	order := &SubscriptionOrder{
		UserId: user.Id, PlanId: plan.Id, Money: plan.PriceAmount,
		TradeNo: "paid-uncredited-order", PaymentMethod: PaymentMethodStripe,
		PaymentProvider: PaymentProviderStripe, Status: common.TopUpStatusPending,
		CreateTime: time.Now().Unix(), ProviderMerchantID: "acct_test",
		ProviderOrderName: plan.Title, ProviderAmount: "10.00",
		ProviderProductID: plan.StripePriceId, ProviderCurrency: "USD",
		ProviderCheckoutID:     "cs_paid_uncredited",
		ProviderKeyFingerprint: providerSettlementScopeFingerprint(),
	}
	require.NoError(t, order.SetEntitlementSnapshot(plan))
	require.NoError(t, DB.Create(order).Error)

	settlement := ProviderSettlement{
		OrderTradeNo: order.TradeNo, Provider: PaymentProviderStripe,
		ProviderTradeNo: "pi_paid_uncredited", ProviderEventID: "evt_paid_uncredited",
		ProviderAccountID: "acct_test", ProviderEnvironment: "test",
		ProviderKeyFingerprint: providerSettlementScopeFingerprint(),
		MerchantID:             "acct_test", ProductID: plan.StripePriceId,
		CheckoutID: order.ProviderCheckoutID, Currency: "USD", Amount: "10.00",
		OrderName: plan.Title, Payload: `{"id":"evt_paid_uncredited"}`,
		PaymentObjects: []ProviderPaymentObject{{ObjectType: ProviderPaymentObjectCheckoutSession, ObjectID: order.ProviderCheckoutID}},
	}
	return paidUncreditedFixture{order: order, settlement: settlement, existing: existing}
}

func TestCompleteSubscriptionOrderPaidUncreditedPersistsPaymentAndIsIdempotent(t *testing.T) {
	fixture := newPaidUncreditedFixture(t)

	outcome, err := CompleteSubscriptionOrderVerifiedWithOutcome(fixture.settlement)
	require.NoError(t, err)
	require.Equal(t, EpaySettlementPaidUncredited, outcome)

	stored := GetSubscriptionOrderByTradeNo(fixture.order.TradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, SubscriptionOrderStatusPaidUncredited, stored.Status)
	assert.Equal(t, SubscriptionSettlementResolutionRefundRequired, stored.SettlementResolution)
	assert.Equal(t, "max_purchase_per_user", stored.SettlementErrorCode)
	assert.NotZero(t, stored.CompleteTime)
	require.NotNil(t, stored.ProviderTradeNo)
	assert.Equal(t, fixture.settlement.ProviderTradeNo, *stored.ProviderTradeNo)
	assert.Equal(t, fixture.settlement.ProviderEventID, stored.ProviderEventID)
	payloadMatches, payloadErr := PaymentPayloadAuditMatches(stored.ProviderPayload, fixture.settlement.Payload)
	require.NoError(t, payloadErr)
	assert.True(t, payloadMatches)
	assert.NotContains(t, stored.ProviderPayload, fixture.settlement.Payload)

	var subscriptionCount int64
	require.NoError(t, DB.Model(&UserSubscription{}).
		Where("user_id = ? AND plan_id = ?", stored.UserId, stored.PlanId).
		Count(&subscriptionCount).Error)
	assert.EqualValues(t, 1, subscriptionCount, "the blocked payment must not create a second entitlement")

	var eventCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).
		Where("provider = ? AND provider_trade_no = ?", fixture.settlement.Provider, fixture.settlement.ProviderTradeNo).
		Count(&eventCount).Error)
	assert.EqualValues(t, 1, eventCount)

	replayOutcome, replayErr := CompleteSubscriptionOrderVerifiedWithOutcome(fixture.settlement)
	require.NoError(t, replayErr)
	assert.Equal(t, EpaySettlementPaidUncredited, replayOutcome)
	require.NoError(t, DB.Model(&PaymentEvent{}).
		Where("provider = ? AND provider_trade_no = ?", fixture.settlement.Provider, fixture.settlement.ProviderTradeNo).
		Count(&eventCount).Error)
	assert.EqualValues(t, 1, eventCount, "duplicate webhook must not create another event")
}

func TestCompleteSubscriptionOrderPaidUncreditedReplayRepairsMissingPaymentEvent(t *testing.T) {
	fixture := newPaidUncreditedFixture(t)

	_, err := CompleteSubscriptionOrderVerifiedWithOutcome(fixture.settlement)
	require.NoError(t, err)

	// A deployment upgrade/reconciliation job may have persisted the terminal
	// order state before the shared payment-event ledger existed.  A verified
	// replay must repair that missing fence without creating a second event or
	// attempting entitlement creation.
	require.NoError(t, DB.Where("provider = ? AND provider_trade_no = ?", fixture.settlement.Provider, fixture.settlement.ProviderTradeNo).
		Delete(&PaymentEvent{}).Error)

	outcome, err := CompleteSubscriptionOrderVerifiedWithOutcome(fixture.settlement)
	require.NoError(t, err)
	assert.Equal(t, EpaySettlementPaidUncredited, outcome)

	var eventCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).
		Where("provider = ? AND provider_trade_no = ?", fixture.settlement.Provider, fixture.settlement.ProviderTradeNo).
		Count(&eventCount).Error)
	assert.EqualValues(t, 1, eventCount, "verified replay should restore the missing idempotency event")

	var entitlementCount int64
	require.NoError(t, DB.Model(&UserSubscription{}).
		Where("subscription_order_trade_no = ?", fixture.order.TradeNo).
		Count(&entitlementCount).Error)
	assert.Zero(t, entitlementCount, "replay must not grant an entitlement after paid_uncredited")
}

func TestPaidUncreditedLateProviderIDSyncsMirrorAndAllowsRetry(t *testing.T) {
	fixture := newPaidUncreditedFixture(t)
	_, err := CompleteSubscriptionOrderVerifiedWithOutcome(fixture.settlement)
	require.NoError(t, err)

	// The initial checkout callback can be missing the expanded recurring ID.
	// A later verified replay reveals it while the order is still
	// paid_uncredited; that identity must be propagated to the history mirror so
	// an eventual entitlement retry does not hit a false cross-ledger conflict.
	late := fixture.settlement
	late.ProviderSubscriptionID = "sub_paid_uncredited_late"
	outcome, err := CompleteSubscriptionOrderVerifiedWithOutcome(late)
	require.NoError(t, err)
	assert.Equal(t, EpaySettlementPaidUncredited, outcome)

	stored := GetSubscriptionOrderByTradeNo(fixture.order.TradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, late.ProviderSubscriptionID, stored.ProviderSubscriptionID)
	var mirror TopUp
	require.NoError(t, DB.Where("trade_no = ?", fixture.order.TradeNo).First(&mirror).Error)
	assert.Equal(t, late.ProviderSubscriptionID, mirror.ProviderSubscriptionID,
		"a verified late recurring ID must be copied to the history mirror")

	// Once an operator frees the cap, retry must be able to complete the same
	// paid order and update the mirror rather than treating the missing field as
	// a conflicting financial row.
	require.NoError(t, DB.Delete(&UserSubscription{}, fixture.existing.Id).Error)
	require.NoError(t, RetryPaidUncreditedSubscriptionOrder(fixture.order.TradeNo))
	assert.Equal(t, common.TopUpStatusSuccess, GetSubscriptionOrderByTradeNo(fixture.order.TradeNo).Status)
	require.NoError(t, DB.Where("trade_no = ?", fixture.order.TradeNo).First(&mirror).Error)
	assert.Equal(t, late.ProviderSubscriptionID, mirror.ProviderSubscriptionID)
}

func TestRetryPaidUncreditedBindsProviderIdentityThroughLedger(t *testing.T) {
	fixture := newPaidUncreditedFixture(t)
	_, err := CompleteSubscriptionOrderVerifiedWithOutcome(fixture.settlement)
	require.NoError(t, err)

	// The recurring object may be revealed after the payment was recorded.  A
	// verified replay stores that identity on the paid order while the purchase
	// cap is still full; the eventual operator retry must create the durable
	// provider-binding row together with the entitlement.
	late := fixture.settlement
	late.ProviderSubscriptionID = "sub_paid_uncredited_ledger"
	_, err = CompleteSubscriptionOrderVerifiedWithOutcome(late)
	require.NoError(t, err)
	require.NoError(t, DB.Delete(&UserSubscription{}, fixture.existing.Id).Error)

	require.NoError(t, RetryPaidUncreditedSubscriptionOrder(fixture.order.TradeNo))

	var granted UserSubscription
	require.NoError(t, DB.Where("subscription_order_trade_no = ?", fixture.order.TradeNo).First(&granted).Error)
	assert.Equal(t, late.ProviderSubscriptionID, granted.ProviderSubscriptionID)
	assert.Equal(t, PaymentProviderStripe, granted.ProviderSubscriptionProvider)

	var binding SubscriptionProviderBindingRecord
	require.NoError(t, DB.Where("user_subscription_id = ?", granted.Id).First(&binding).Error)
	assert.Equal(t, granted.Id, binding.UserSubscriptionID)
	assert.Equal(t, PaymentProviderStripe, binding.Provider)
	assert.Equal(t, late.ProviderSubscriptionID, binding.ProviderSubscriptionID)
	require.NotNil(t, binding.OrderTradeNo)
	assert.Equal(t, fixture.order.TradeNo, *binding.OrderTradeNo)
}

func TestRetryPaidUncreditedRejectsProviderIdentityBoundToAnotherEntitlement(t *testing.T) {
	fixture := newPaidUncreditedFixture(t)
	_, err := CompleteSubscriptionOrderVerifiedWithOutcome(fixture.settlement)
	require.NoError(t, err)

	late := fixture.settlement
	late.ProviderSubscriptionID = "sub_paid_uncredited_bound_elsewhere"
	_, err = CompleteSubscriptionOrderVerifiedWithOutcome(late)
	require.NoError(t, err)

	// Simulate a different already-bound entitlement.  The retry must hit the
	// same database uniqueness fence as webhook settlement and roll back the new
	// entitlement instead of assigning the identity directly.
	otherUser := &User{
		Id: 98205, Username: "paid-uncredited-bound-owner",
		AffCode: "paid-uncredited-bound-owner-aff", Status: common.UserStatusEnabled,
	}
	require.NoError(t, DB.Create(otherUser).Error)
	other := &UserSubscription{
		UserId: otherUser.Id, PlanId: fixture.order.PlanId, AmountTotal: 1000,
		StartTime: time.Now().Unix(), EndTime: time.Now().Add(time.Hour).Unix(),
		Status: "active", Source: "order",
		SubscriptionOrderTradeNo:     "other-bound-order",
		ProviderSubscriptionID:       late.ProviderSubscriptionID,
		ProviderSubscriptionProvider: PaymentProviderStripe,
	}
	require.NoError(t, DB.Create(other).Error)
	tradeNo := "other-bound-order"
	require.NoError(t, DB.Create(&SubscriptionProviderBindingRecord{
		UserSubscriptionID: other.Id, OrderTradeNo: &tradeNo,
		Provider: PaymentProviderStripe, ProviderSubscriptionID: late.ProviderSubscriptionID,
	}).Error)

	require.NoError(t, DB.Delete(&UserSubscription{}, fixture.existing.Id).Error)
	err = RetryPaidUncreditedSubscriptionOrder(fixture.order.TradeNo)
	require.ErrorIs(t, err, ErrProviderEventConflict)

	var linkedCount int64
	require.NoError(t, DB.Model(&UserSubscription{}).
		Where("subscription_order_trade_no = ?", fixture.order.TradeNo).Count(&linkedCount).Error)
	assert.Zero(t, linkedCount, "provider binding conflict must roll back the new entitlement")
	stored := GetSubscriptionOrderByTradeNo(fixture.order.TradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, SubscriptionOrderStatusPaidUncredited, stored.Status)
}

func TestPaidUncreditedLateProviderIDRejectsDanglingExistingBinding(t *testing.T) {
	fixture := newPaidUncreditedFixture(t)
	_, err := CompleteSubscriptionOrderVerifiedWithOutcome(fixture.settlement)
	require.NoError(t, err)

	late := fixture.settlement
	late.ProviderSubscriptionID = "sub_paid_uncredited_dangling_binding"
	key, ok := SubscriptionProviderBindingKey(PaymentProviderStripe, late.ProviderSubscriptionID)
	require.True(t, ok)
	// The ledger row points at a subscription that no longer exists.  It is still
	// an occupied identity fence and must not be silently bypassed by a replay
	// that has no order-linked entitlement yet.
	require.NoError(t, DB.Exec("INSERT INTO subscription_provider_bindings (user_subscription_id, provider, provider_subscription_id, provider_subscription_key, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		999999, PaymentProviderStripe, late.ProviderSubscriptionID, key, common.GetTimestamp(), common.GetTimestamp()).Error)

	_, err = CompleteSubscriptionOrderVerifiedWithOutcome(late)
	require.ErrorIs(t, err, ErrProviderEventConflict)
	stored := GetSubscriptionOrderByTradeNo(fixture.order.TradeNo)
	require.NotNil(t, stored)
	assert.Empty(t, stored.ProviderSubscriptionID)
	assert.Equal(t, SubscriptionOrderStatusPaidUncredited, stored.Status)
}

func TestPaidUncreditedLateProviderIDRejectsWrongLinkedEntitlement(t *testing.T) {
	fixture := newPaidUncreditedFixture(t)
	_, err := CompleteSubscriptionOrderVerifiedWithOutcome(fixture.settlement)
	require.NoError(t, err)

	wrongUser := &User{
		Id: 98204, Username: "paid-uncredited-binding-other-user",
		AffCode: "paid-uncredited-binding-other-aff", Status: common.UserStatusEnabled,
	}
	require.NoError(t, DB.Create(wrongUser).Error)
	wrong := &UserSubscription{
		UserId: wrongUser.Id, PlanId: fixture.order.PlanId, AmountTotal: 1000,
		StartTime: time.Now().Unix(), EndTime: time.Now().Add(time.Hour).Unix(),
		Status: "active", Source: "order", SubscriptionOrderTradeNo: fixture.order.TradeNo,
	}
	require.NoError(t, DB.Create(wrong).Error)

	late := fixture.settlement
	late.ProviderSubscriptionID = "sub_paid_uncredited_wrong-link"
	_, err = CompleteSubscriptionOrderVerifiedWithOutcome(late)
	require.ErrorIs(t, err, ErrProviderEventConflict)

	var stored UserSubscription
	require.NoError(t, DB.Where("id = ?", wrong.Id).First(&stored).Error)
	assert.Empty(t, stored.ProviderSubscriptionID,
		"a provider ID must never be attached to an entitlement owned by another user")
}

func TestCompleteSubscriptionOrderPaidUncreditedRejectsConflictingReplay(t *testing.T) {
	fixture := newPaidUncreditedFixture(t)
	_, err := CompleteSubscriptionOrderVerifiedWithOutcome(fixture.settlement)
	require.NoError(t, err)

	conflict := fixture.settlement
	conflict.Amount = "11.00"
	_, err = CompleteSubscriptionOrderVerifiedWithOutcome(conflict)
	require.ErrorIs(t, err, ErrProviderSnapshotMismatch)

	stored := GetSubscriptionOrderByTradeNo(fixture.order.TradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, SubscriptionOrderStatusPaidUncredited, stored.Status)
}

func TestCompleteSubscriptionOrderPaidUncreditedRejectsReplayWhenEventWasRebound(t *testing.T) {
	fixture := newPaidUncreditedFixture(t)
	_, err := CompleteSubscriptionOrderVerifiedWithOutcome(fixture.settlement)
	require.NoError(t, err)

	// Corrupt the event binding in the same way a legacy/manual reconciliation
	// mistake could.  The provider transaction must remain bound to the original
	// order; a paid_uncredited replay must not acknowledge a cross-order event.
	require.NoError(t, DB.Model(&PaymentEvent{}).
		Where("provider = ? AND provider_trade_no = ?", fixture.settlement.Provider, fixture.settlement.ProviderTradeNo).
		Updates(map[string]interface{}{
			"order_trade_no": "different-local-order",
			"order_kind":     PaymentEventOrderTopUp,
		}).Error)

	_, err = CompleteSubscriptionOrderVerifiedWithOutcome(fixture.settlement)
	require.ErrorIs(t, err, ErrEpayProviderTradeConflict)
	stored := GetSubscriptionOrderByTradeNo(fixture.order.TradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, SubscriptionOrderStatusPaidUncredited, stored.Status)
}

func TestCompleteSubscriptionOrderPaidUncreditedMirrorDatabaseFailureRollsBack(t *testing.T) {
	fixture := newPaidUncreditedFixture(t)
	const triggerName = "paid_uncredited_mirror_insert_failure"
	require.NoError(t, DB.Exec("CREATE TRIGGER "+triggerName+" BEFORE INSERT ON top_ups BEGIN SELECT RAISE(ABORT, 'forced mirror failure'); END").Error)
	t.Cleanup(func() {
		DB.Exec("DROP TRIGGER IF EXISTS " + triggerName)
	})

	// The provider event is authenticated before the mirror write.  A genuine
	// database failure must still roll back the event, provider evidence, and
	// terminal state together so the provider can retry safely.
	_, err := CompleteSubscriptionOrderVerifiedWithOutcome(fixture.settlement)
	require.Error(t, err)

	stored := GetSubscriptionOrderByTradeNo(fixture.order.TradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, common.TopUpStatusPending, stored.Status)
	assert.Nil(t, stored.ProviderTradeNo)
	assert.Empty(t, stored.ProviderPayload)

	var eventCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).
		Where("provider = ? AND provider_trade_no = ?", fixture.settlement.Provider, fixture.settlement.ProviderTradeNo).
		Count(&eventCount).Error)
	assert.Zero(t, eventCount, "a failed mirror write must roll back the payment-event fence")

	var mirrorCount int64
	require.NoError(t, DB.Model(&TopUp{}).Where("trade_no = ?", fixture.order.TradeNo).Count(&mirrorCount).Error)
	assert.Zero(t, mirrorCount)
}

func TestRetryPaidUncreditedSubscriptionOrderGrantsAfterOperatorFreesSlot(t *testing.T) {
	fixture := newPaidUncreditedFixture(t)
	_, err := CompleteSubscriptionOrderVerifiedWithOutcome(fixture.settlement)
	require.NoError(t, err)

	// MaxPurchasePerUser is a lifetime count.  Removing the known conflicting
	// legacy entitlement models an operator's documented reconciliation action;
	// the retry itself must still use the immutable checkout snapshot.
	require.NoError(t, DB.Delete(&UserSubscription{}, fixture.existing.Id).Error)
	require.ErrorIs(t, RetryPaidUncreditedSubscriptionOrder(fixture.order.TradeNo), nil)

	stored := GetSubscriptionOrderByTradeNo(fixture.order.TradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, common.TopUpStatusSuccess, stored.Status)
	assert.Empty(t, stored.SettlementResolution)
	assert.Empty(t, stored.SettlementErrorCode)

	var granted UserSubscription
	require.NoError(t, DB.Where("subscription_order_trade_no = ?", fixture.order.TradeNo).First(&granted).Error)
	assert.Equal(t, fixture.order.UserId, granted.UserId)
	assert.Equal(t, fixture.order.PlanId, granted.PlanId)
	assert.Equal(t, "active", granted.Status)
	var mirror TopUp
	require.NoError(t, DB.Where("trade_no = ?", fixture.order.TradeNo).First(&mirror).Error)
	assert.Equal(t, common.TopUpStatusSuccess, mirror.Status)
	assert.Zero(t, mirror.CreditedQuota)
	assert.Equal(t, fixture.settlement.ProviderTradeNo, dereferenceString(mirror.ProviderTradeNo))

	// Retrying a now-successful order is a no-op, never a second entitlement.
	require.NoError(t, RetryPaidUncreditedSubscriptionOrder(fixture.order.TradeNo))
	var count int64
	require.NoError(t, DB.Model(&UserSubscription{}).
		Where("subscription_order_trade_no = ?", fixture.order.TradeNo).
		Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestRetryPaidUncreditedSubscriptionOrderKeepsStateWhenCapStillFull(t *testing.T) {
	fixture := newPaidUncreditedFixture(t)
	_, err := CompleteSubscriptionOrderVerifiedWithOutcome(fixture.settlement)
	require.NoError(t, err)

	err = RetryPaidUncreditedSubscriptionOrder(fixture.order.TradeNo)
	require.ErrorIs(t, err, ErrSubscriptionPurchaseLimitExceeded)
	stored := GetSubscriptionOrderByTradeNo(fixture.order.TradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, SubscriptionOrderStatusPaidUncredited, stored.Status)
}

func TestRetryPaidUncreditedRejectsMismatchedLinkedEntitlement(t *testing.T) {
	fixture := newPaidUncreditedFixture(t)
	_, err := CompleteSubscriptionOrderVerifiedWithOutcome(fixture.settlement)
	require.NoError(t, err)

	// Simulate a legacy/manual repair that accidentally linked a different
	// user's entitlement to this paid order.  Retry must fail closed instead of
	// marking the payment successful for the wrong account.
	otherUser := &User{
		Id:       98203,
		Username: "paid-uncredited-other-user",
		AffCode:  "paid-uncredited-other-affcode",
		Status:   common.UserStatusEnabled,
	}
	require.NoError(t, DB.Create(otherUser).Error)
	wrong := &UserSubscription{
		UserId:                   otherUser.Id,
		PlanId:                   fixture.order.PlanId,
		AmountTotal:              1000,
		StartTime:                time.Now().Unix(),
		EndTime:                  time.Now().Add(time.Hour).Unix(),
		Status:                   "active",
		Source:                   "order",
		SubscriptionOrderTradeNo: fixture.order.TradeNo,
	}
	require.NoError(t, DB.Create(wrong).Error)

	err = RetryPaidUncreditedSubscriptionOrder(fixture.order.TradeNo)
	require.ErrorIs(t, err, ErrProviderEventConflict)

	stored := GetSubscriptionOrderByTradeNo(fixture.order.TradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, SubscriptionOrderStatusPaidUncredited, stored.Status)
	assert.Equal(t, SubscriptionSettlementResolutionRefundRequired, stored.SettlementResolution)

	var mirror TopUp
	require.NoError(t, DB.Where("trade_no = ?", fixture.order.TradeNo).First(&mirror).Error)
	assert.Equal(t, common.TopUpStatusSuccess, mirror.Status,
		"the original paid mirror must remain unchanged when retry rejects a corrupt link")
}

func dereferenceString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// withConcurrentPaymentDB gives the two goroutines independent SQL
// connections while keeping all rows in one SQLite file. The package's global
// test database intentionally has one connection (to make :memory: fixtures
// deterministic), which would hide order/user lock races.
func withConcurrentPaymentDB(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB, previousLogDB := DB, LOG_DB
	previousMainType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	dsn := filepath.Join(t.TempDir(), "payments.sqlite") + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	DB, LOG_DB = db, db
	initCol()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	require.NoError(t, db.AutoMigrate(
		&User{}, &SubscriptionPlan{}, &SubscriptionOrder{}, &UserSubscription{},
		&TopUp{}, &PaymentEvent{}, &ProviderPaymentBinding{}, &Log{},
	))
	t.Cleanup(func() {
		_ = sqlDB.Close()
		DB, LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMainType, previousLogType)
		initCol()
	})
	return db
}

func TestConcurrentSubscriptionSettlementsSerializePurchaseCap(t *testing.T) {
	db := withConcurrentPaymentDB(t)
	const (
		userID = 98301
		planID = 98302
	)
	require.NoError(t, db.Create(&User{
		Id: userID, Username: "paid-uncredited-concurrent-user",
		AffCode: "paid-uncredited-concurrent-aff", Status: common.UserStatusEnabled,
	}).Error)
	plan := &SubscriptionPlan{
		Id: planID, Title: "concurrent cap plan", PriceAmount: 10, Currency: "USD",
		DurationUnit: SubscriptionDurationMonth, DurationValue: 1, Enabled: true,
		TotalAmount: 1000, MaxPurchasePerUser: 1, StripePriceId: "price-concurrent-cap",
	}
	require.NoError(t, db.Create(plan).Error)

	settlements := make([]ProviderSettlement, 2)
	for i := range settlements {
		tradeNo := fmt.Sprintf("paid-uncredited-concurrent-order-%d", i)
		order := &SubscriptionOrder{
			UserId: userID, PlanId: planID, Money: plan.PriceAmount, TradeNo: tradeNo,
			PaymentMethod: PaymentMethodStripe, PaymentProvider: PaymentProviderStripe,
			Status: common.TopUpStatusPending, CreateTime: time.Now().Unix(),
			ProviderMerchantID: "acct_concurrent", ProviderOrderName: plan.Title,
			ProviderAmount: "10.00", ProviderProductID: plan.StripePriceId,
			ProviderCurrency: "USD", ProviderCheckoutID: fmt.Sprintf("cs_concurrent_%d", i),
		}
		order.ProviderKeyFingerprint, _ = ProviderPaymentScopeFingerprint(PaymentProviderStripe, "acct_concurrent", "test")
		require.NoError(t, order.SetEntitlementSnapshot(plan))
		require.NoError(t, db.Create(order).Error)
		settlements[i] = ProviderSettlement{
			OrderTradeNo: tradeNo, Provider: PaymentProviderStripe,
			ProviderTradeNo:   fmt.Sprintf("pi_concurrent_%d", i),
			ProviderEventID:   fmt.Sprintf("evt_concurrent_%d", i),
			ProviderAccountID: "acct_concurrent", ProviderEnvironment: "test",
			ProviderKeyFingerprint: order.ProviderKeyFingerprint, MerchantID: "acct_concurrent",
			ProductID: plan.StripePriceId, CheckoutID: order.ProviderCheckoutID,
			Currency: "USD", Amount: "10.00", OrderName: plan.Title,
			Payload:        fmt.Sprintf(`{"id":"evt_concurrent_%d"}`, i),
			PaymentObjects: []ProviderPaymentObject{{ObjectType: ProviderPaymentObjectCheckoutSession, ObjectID: order.ProviderCheckoutID}},
		}
	}

	start := make(chan struct{})
	type result struct {
		index   int
		outcome EpaySettlementOutcome
		err     error
	}
	results := make(chan result, len(settlements))
	for i, settlement := range settlements {
		settlement := settlement
		go func(index int) {
			<-start
			outcome, err := CompleteSubscriptionOrderVerifiedWithOutcome(settlement)
			results <- result{index: index, outcome: outcome, err: err}
		}(i)
	}
	close(start)
	got := make([]result, len(settlements))
	for range settlements {
		result := <-results
		got[result.index] = result
	}

	// SQLite may report a transient writer-busy error even with a busy timeout
	// when both deferred transactions first read and then attempt to promote to
	// writers. Such a callback is retryable by the provider; replay it after the
	// first transaction has committed and include its durable result below.
	for i, item := range got {
		if item.err == nil {
			continue
		}
		require.True(t,
			strings.Contains(strings.ToLower(item.err.Error()), "locked") ||
				strings.Contains(strings.ToLower(item.err.Error()), "busy"),
			"only a transient SQLite writer conflict may be recovered by retry: %v", item.err)
		var retryOutcome EpaySettlementOutcome
		var retryErr error
		for attempt := 0; attempt < 3; attempt++ {
			retryOutcome, retryErr = CompleteSubscriptionOrderVerifiedWithOutcome(settlements[i])
			if retryErr == nil {
				break
			}
		}
		require.NoError(t, retryErr, "a transient concurrent writer failure must be recoverable by webhook retry")
		got[i] = result{index: i, outcome: retryOutcome}
	}

	var successCount, paidUncreditedCount int
	for _, result := range got {
		require.NoError(t, result.err)
		switch result.outcome {
		case EpaySettlementCompleted:
			successCount++
		case EpaySettlementPaidUncredited:
			paidUncreditedCount++
		default:
			t.Fatalf("unexpected settlement outcome: %v", result.outcome)
		}
	}
	assert.Equal(t, 1, successCount)
	assert.Equal(t, 1, paidUncreditedCount)

	var entitlementCount int64
	require.NoError(t, db.Model(&UserSubscription{}).Where("user_id = ? AND plan_id = ?", userID, planID).Count(&entitlementCount).Error)
	assert.EqualValues(t, 1, entitlementCount, "concurrent paid callbacks must never exceed MaxPurchasePerUser")
	var paidCount int64
	require.NoError(t, db.Model(&SubscriptionOrder{}).Where("status = ?", SubscriptionOrderStatusPaidUncredited).Count(&paidCount).Error)
	assert.EqualValues(t, 1, paidCount)
}

func TestPaidUncreditedRetryAndWebhookRaceGrantsAtMostOnce(t *testing.T) {
	db := withConcurrentPaymentDB(t)
	const (
		userID = 98311
		planID = 98312
	)
	require.NoError(t, db.Create(&User{
		Id: userID, Username: "paid-uncredited-retry-race-user",
		AffCode: "paid-uncredited-retry-race-aff", Status: common.UserStatusEnabled,
	}).Error)
	plan := &SubscriptionPlan{
		Id: planID, Title: "retry race plan", PriceAmount: 12, Currency: "USD",
		DurationUnit: SubscriptionDurationMonth, DurationValue: 1, Enabled: true,
		TotalAmount: 1200, MaxPurchasePerUser: 1, StripePriceId: "price-retry-race",
	}
	require.NoError(t, db.Create(plan).Error)
	existing := &UserSubscription{
		UserId: userID, PlanId: planID, AmountTotal: plan.TotalAmount,
		StartTime: time.Now().Add(-time.Hour).Unix(), EndTime: time.Now().Add(time.Hour).Unix(),
		Status: "active", Source: "order", SubscriptionOrderTradeNo: "retry-race-existing",
	}
	require.NoError(t, db.Create(existing).Error)
	order := &SubscriptionOrder{
		UserId: userID, PlanId: planID, Money: plan.PriceAmount, TradeNo: "paid-uncredited-retry-race",
		PaymentMethod: PaymentMethodStripe, PaymentProvider: PaymentProviderStripe,
		Status: common.TopUpStatusPending, CreateTime: time.Now().Unix(),
		ProviderMerchantID: "acct_retry_race", ProviderOrderName: plan.Title,
		ProviderAmount: "12.00", ProviderProductID: plan.StripePriceId,
		ProviderCurrency: "USD", ProviderCheckoutID: "cs_retry_race",
	}
	order.ProviderKeyFingerprint, _ = ProviderPaymentScopeFingerprint(PaymentProviderStripe, "acct_retry_race", "test")
	require.NoError(t, order.SetEntitlementSnapshot(plan))
	require.NoError(t, db.Create(order).Error)
	settlement := ProviderSettlement{
		OrderTradeNo: order.TradeNo, Provider: PaymentProviderStripe,
		ProviderTradeNo: "pi_retry_race", ProviderEventID: "evt_retry_race",
		ProviderAccountID: "acct_retry_race", ProviderEnvironment: "test",
		ProviderKeyFingerprint: order.ProviderKeyFingerprint,
		MerchantID:             "acct_retry_race", ProductID: plan.StripePriceId,
		CheckoutID: order.ProviderCheckoutID, Currency: "USD", Amount: "12.00",
		OrderName: plan.Title, Payload: `{"id":"evt_retry_race"}`,
		PaymentObjects: []ProviderPaymentObject{{ObjectType: ProviderPaymentObjectCheckoutSession, ObjectID: order.ProviderCheckoutID}},
	}
	outcome, err := CompleteSubscriptionOrderVerifiedWithOutcome(settlement)
	require.NoError(t, err)
	require.Equal(t, EpaySettlementPaidUncredited, outcome)

	// Free the cap before racing an operator retry with a duplicate provider
	// callback. Both operations must serialize on the same order/user rows.
	require.NoError(t, db.Delete(&UserSubscription{}, existing.Id).Error)
	start := make(chan struct{})
	type raceResult struct {
		kind    string
		outcome EpaySettlementOutcome
		err     error
	}
	results := make(chan raceResult, 2)
	go func() {
		<-start
		err := RetryPaidUncreditedSubscriptionOrder(order.TradeNo)
		results <- raceResult{kind: "retry", err: err}
	}()
	go func() {
		<-start
		outcome, err := CompleteSubscriptionOrderVerifiedWithOutcome(settlement)
		results <- raceResult{kind: "webhook", outcome: outcome, err: err}
	}()
	close(start)
	got := []raceResult{<-results, <-results}
	for i, item := range got {
		if item.err == nil {
			continue
		}
		// As above, SQLite's deferred writer promotion can produce SQLITE_BUSY;
		// that is a retryable transport failure, not a successful settlement.
		require.True(t, strings.Contains(strings.ToLower(item.err.Error()), "locked") || strings.Contains(strings.ToLower(item.err.Error()), "busy"), "unexpected race error: %v", item.err)
		if item.kind == "retry" {
			for attempt := 0; attempt < 3 && item.err != nil; attempt++ {
				item.err = RetryPaidUncreditedSubscriptionOrder(order.TradeNo)
			}
		} else {
			for attempt := 0; attempt < 3 && item.err != nil; attempt++ {
				item.outcome, item.err = CompleteSubscriptionOrderVerifiedWithOutcome(settlement)
			}
		}
		got[i] = item
	}
	for _, item := range got {
		require.NoError(t, item.err)
		if item.kind == "webhook" {
			assert.Contains(t, []EpaySettlementOutcome{EpaySettlementPaidUncredited, EpaySettlementAlreadyCompleted}, item.outcome)
		}
	}

	stored := GetSubscriptionOrderByTradeNo(order.TradeNo)
	require.NotNil(t, stored)
	assert.Equal(t, common.TopUpStatusSuccess, stored.Status)
	var entitlementCount int64
	require.NoError(t, db.Model(&UserSubscription{}).Where("subscription_order_trade_no = ?", order.TradeNo).Count(&entitlementCount).Error)
	assert.EqualValues(t, 1, entitlementCount)
	var eventCount int64
	require.NoError(t, db.Model(&PaymentEvent{}).Where("provider = ? AND provider_trade_no = ?", settlement.Provider, settlement.ProviderTradeNo).Count(&eventCount).Error)
	assert.EqualValues(t, 1, eventCount)
}

package model

import (
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newProviderPaymentBindingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&ProviderPaymentBinding{}))
	return db
}

func providerPaymentBindingBatch(orderTradeNo string, objects ...ProviderPaymentObject) ProviderPaymentBindingBatch {
	return ProviderPaymentBindingBatch{
		Provider:            "stripe",
		ProviderAccountID:   "acct_platform",
		ProviderEnvironment: "test",
		OrderTradeNo:        orderTradeNo,
		OrderKind:           PaymentEventOrderTopUp,
		Objects:             objects,
	}
}

func TestProviderPaymentBindingKeyIncludesAccountEnvironmentAndObjectType(t *testing.T) {
	base, ok := ProviderPaymentBindingKey(" Stripe ", "acct_platform", " TEST ", " Payment_Intent ", " pi_123 ")
	require.True(t, ok)
	canonical, ok := ProviderPaymentBindingKey("stripe", "acct_platform", "test", "payment_intent", "pi_123")
	require.True(t, ok)
	assert.Equal(t, canonical, base)

	for _, candidate := range []struct {
		account     string
		environment string
		objectType  string
	}{
		{account: "acct_other", environment: "test", objectType: "payment_intent"},
		{account: "acct_platform", environment: "live", objectType: "payment_intent"},
		{account: "acct_platform", environment: "test", objectType: "charge"},
	} {
		key, candidateOK := ProviderPaymentBindingKey("stripe", candidate.account, candidate.environment, candidate.objectType, "pi_123")
		require.True(t, candidateOK)
		assert.NotEqual(t, canonical, key)
	}

	_, ok = ProviderPaymentBindingKey("stripe", "", "test", "payment_intent", "pi_123")
	assert.False(t, ok)
}

func TestProviderPaymentScopeFingerprintIncludesAccountAndEnvironment(t *testing.T) {
	base, ok := ProviderPaymentScopeFingerprint(" Stripe ", "acct_platform", " TEST ")
	require.True(t, ok)
	canonical, ok := ProviderPaymentScopeFingerprint("stripe", "acct_platform", "test")
	require.True(t, ok)
	assert.Equal(t, canonical, base)

	otherAccount, ok := ProviderPaymentScopeFingerprint("stripe", "acct_other", "test")
	require.True(t, ok)
	otherEnvironment, ok := ProviderPaymentScopeFingerprint("stripe", "acct_platform", "live")
	require.True(t, ok)
	assert.NotEqual(t, canonical, otherAccount)
	assert.NotEqual(t, canonical, otherEnvironment)

	_, ok = ProviderPaymentScopeFingerprint("stripe", "", "test")
	assert.False(t, ok)
}

func TestBindProviderPaymentBindingsTxIsIdempotentAndScoped(t *testing.T) {
	db := newProviderPaymentBindingTestDB(t)
	objects := []ProviderPaymentObject{
		{ObjectType: ProviderPaymentObjectCheckoutSession, ObjectID: "cs_123"},
		{ObjectType: ProviderPaymentObjectPaymentIntent, ObjectID: "pi_123"},
		{ObjectType: ProviderPaymentObjectPaymentIntent, ObjectID: "pi_123"},
	}
	batch := providerPaymentBindingBatch("order-one", objects...)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return bindProviderPaymentBindingsTx(tx, batch)
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return bindProviderPaymentBindingsTx(tx, batch)
	}))

	var count int64
	require.NoError(t, db.Model(&ProviderPaymentBinding{}).Count(&count).Error)
	assert.EqualValues(t, 2, count)

	otherEnvironment := batch
	otherEnvironment.ProviderEnvironment = "live"
	otherEnvironment.OrderTradeNo = "order-live"
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return bindProviderPaymentBindingsTx(tx, otherEnvironment)
	}))

	otherAccount := batch
	otherAccount.ProviderAccountID = "acct_other"
	otherAccount.OrderTradeNo = "order-other-account"
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return bindProviderPaymentBindingsTx(tx, otherAccount)
	}))
	require.NoError(t, db.Model(&ProviderPaymentBinding{}).Count(&count).Error)
	assert.EqualValues(t, 6, count)
}

func TestBindProviderPaymentBindingsTxUsesStableKeyOrder(t *testing.T) {
	db := newProviderPaymentBindingTestDB(t)
	objects := []ProviderPaymentObject{
		{ObjectType: ProviderPaymentObjectSubscription, ObjectID: "sub_lock_order"},
		{ObjectType: ProviderPaymentObjectCheckoutSession, ObjectID: "cs_lock_order"},
		{ObjectType: ProviderPaymentObjectCharge, ObjectID: "ch_lock_order"},
	}
	batch := providerPaymentBindingBatch("order-lock-order", objects...)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return bindProviderPaymentBindingsTx(tx, batch)
	}))

	expected := make([]string, 0, len(objects))
	for _, object := range objects {
		key, ok := ProviderPaymentBindingKey(
			batch.Provider,
			batch.ProviderAccountID,
			batch.ProviderEnvironment,
			object.ObjectType,
			object.ObjectID,
		)
		require.True(t, ok)
		expected = append(expected, key)
	}
	sort.Strings(expected)
	var bindings []ProviderPaymentBinding
	require.NoError(t, db.Order("id ASC").Find(&bindings).Error)
	require.Len(t, bindings, len(expected))
	actual := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		actual = append(actual, binding.BindingKey)
	}
	assert.Equal(t, expected, actual)
}

func TestBindProviderPaymentBindingsTxRejectsCrossOrderRebinding(t *testing.T) {
	db := newProviderPaymentBindingTestDB(t)
	object := ProviderPaymentObject{ObjectType: ProviderPaymentObjectCharge, ObjectID: "ch_conflict"}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return bindProviderPaymentBindingsTx(tx, providerPaymentBindingBatch("order-one", object))
	}))

	err := db.Transaction(func(tx *gorm.DB) error {
		batch := providerPaymentBindingBatch("order-two", object)
		batch.OrderKind = PaymentEventOrderSubscription
		return bindProviderPaymentBindingsTx(tx, batch)
	})
	require.ErrorIs(t, err, ErrProviderPaymentBindingConflict)

	key, ok := ProviderPaymentBindingKey("stripe", "acct_platform", "test", ProviderPaymentObjectCharge, "ch_conflict")
	require.True(t, ok)
	var binding ProviderPaymentBinding
	require.NoError(t, db.Where("binding_key = ?", key).First(&binding).Error)
	assert.Equal(t, "order-one", binding.OrderTradeNo)
	assert.Equal(t, PaymentEventOrderTopUp, binding.OrderKind)
}

func TestProviderPaymentBindingRowsAreImmutable(t *testing.T) {
	db := newProviderPaymentBindingTestDB(t)
	object := ProviderPaymentObject{ObjectType: ProviderPaymentObjectCharge, ObjectID: "ch_immutable"}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return bindProviderPaymentBindingsTx(tx, providerPaymentBindingBatch("order-one", object))
	}))

	var binding ProviderPaymentBinding
	require.NoError(t, db.Where("object_id = ?", object.ObjectID).First(&binding).Error)
	err := db.Model(&binding).Update("order_trade_no", "order-two").Error
	require.ErrorIs(t, err, ErrProviderPaymentBindingConflict)
	require.NoError(t, db.First(&binding, binding.ID).Error)
	assert.Equal(t, "order-one", binding.OrderTradeNo)
}

func TestBindProviderPaymentBindingsTxRollsBackWholeBatchOnConflict(t *testing.T) {
	db := newProviderPaymentBindingTestDB(t)
	conflicting := ProviderPaymentObject{ObjectType: ProviderPaymentObjectInvoice, ObjectID: "in_existing"}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return bindProviderPaymentBindingsTx(tx, providerPaymentBindingBatch("order-one", conflicting))
	}))

	err := db.Transaction(func(tx *gorm.DB) error {
		return bindProviderPaymentBindingsTx(tx, providerPaymentBindingBatch(
			"order-two",
			ProviderPaymentObject{ObjectType: ProviderPaymentObjectCharge, ObjectID: "ch_should_rollback"},
			conflicting,
		))
	})
	require.ErrorIs(t, err, ErrProviderPaymentBindingConflict)

	var count int64
	require.NoError(t, db.Model(&ProviderPaymentBinding{}).Where("object_id = ?", "ch_should_rollback").Count(&count).Error)
	assert.Zero(t, count)
}

func TestFindProviderPaymentBindingRequiresExactScope(t *testing.T) {
	db := newProviderPaymentBindingTestDB(t)
	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })

	batch := providerPaymentBindingBatch(
		"order-one",
		ProviderPaymentObject{ObjectType: ProviderPaymentObjectPaymentIntent, ObjectID: "pi_lookup"},
	)
	require.NoError(t, BindProviderPaymentBindings(batch))

	binding, err := FindProviderPaymentBinding("STRIPE", "acct_platform", "TEST", "PAYMENT_INTENT", "pi_lookup")
	require.NoError(t, err)
	assert.Equal(t, "order-one", binding.OrderTradeNo)

	_, err = FindProviderPaymentBinding("stripe", "acct_platform", "live", "payment_intent", "pi_lookup")
	assert.True(t, errors.Is(err, ErrProviderPaymentBindingNotFound))
}

func TestBindProviderPaymentBindingsRejectsIncompleteScope(t *testing.T) {
	db := newProviderPaymentBindingTestDB(t)
	valid := providerPaymentBindingBatch(
		"order-one",
		ProviderPaymentObject{ObjectType: ProviderPaymentObjectPaymentIntent, ObjectID: "pi_invalid"},
	)

	for name, mutate := range map[string]func(*ProviderPaymentBindingBatch){
		"provider":    func(batch *ProviderPaymentBindingBatch) { batch.Provider = "" },
		"account":     func(batch *ProviderPaymentBindingBatch) { batch.ProviderAccountID = "" },
		"environment": func(batch *ProviderPaymentBindingBatch) { batch.ProviderEnvironment = "" },
		"order":       func(batch *ProviderPaymentBindingBatch) { batch.OrderTradeNo = "" },
		"order kind":  func(batch *ProviderPaymentBindingBatch) { batch.OrderKind = "" },
		"objects":     func(batch *ProviderPaymentBindingBatch) { batch.Objects = nil },
		"object type": func(batch *ProviderPaymentBindingBatch) { batch.Objects[0].ObjectType = "" },
		"object id":   func(batch *ProviderPaymentBindingBatch) { batch.Objects[0].ObjectID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			batch := valid
			batch.Objects = append([]ProviderPaymentObject(nil), valid.Objects...)
			mutate(&batch)
			err := db.Transaction(func(tx *gorm.DB) error {
				return bindProviderPaymentBindingsTx(tx, batch)
			})
			require.ErrorIs(t, err, ErrProviderPaymentBindingInvalid)
		})
	}
}

func createProviderPaymentLifecycleFixture(t *testing.T, userID, planID int, tradeNo, providerSubscriptionID string) (*UserSubscription, *SubscriptionLifecycleEvidence) {
	t.Helper()
	require.NoError(t, DB.Create(&User{Id: userID, Username: tradeNo + "-user"}).Error)
	plan := &SubscriptionPlan{
		Id: planID, Title: tradeNo + " plan", PriceAmount: 10,
		Currency: "USD", DurationUnit: SubscriptionDurationMonth,
		DurationValue: 1, Enabled: true, TotalAmount: 1000,
		StripePriceId: "price_" + tradeNo,
	}
	require.NoError(t, DB.Create(plan).Error)
	providerTradeNo := "cs_" + tradeNo
	order := &SubscriptionOrder{
		UserId: userID, PlanId: planID, Money: 10, TradeNo: tradeNo,
		PaymentMethod: PaymentMethodStripe, PaymentProvider: PaymentProviderStripe,
		Status: common.TopUpStatusSuccess, ProviderTradeNo: &providerTradeNo,
		ProviderSubscriptionID: providerSubscriptionID, ProviderAmount: "10.00",
		ProviderCurrency: "USD", ProviderProductID: plan.StripePriceId,
		ProviderMerchantID: "stripe", ProviderOrderName: plan.Title,
	}
	require.NoError(t, order.SetEntitlementSnapshot(plan))
	require.NoError(t, DB.Create(order).Error)
	subscription := &UserSubscription{
		UserId: userID, PlanId: planID, AmountTotal: 1000, AmountUsed: 25,
		Status: "active", EndTime: 100,
		ProviderSubscriptionID:       providerSubscriptionID,
		ProviderSubscriptionProvider: PaymentProviderStripe,
		SubscriptionOrderTradeNo:     tradeNo,
	}
	require.NoError(t, DB.Create(subscription).Error)
	require.NoError(t, BindProviderPaymentBindings(ProviderPaymentBindingBatch{
		Provider:            PaymentProviderStripe,
		ProviderAccountID:   "acct_platform",
		ProviderEnvironment: "test",
		OrderTradeNo:        tradeNo,
		OrderKind:           PaymentEventOrderSubscription,
		Objects: []ProviderPaymentObject{{
			ObjectType: ProviderPaymentObjectSubscription,
			ObjectID:   providerSubscriptionID,
		}},
	}))
	return subscription, &SubscriptionLifecycleEvidence{
		Amount: "10.00", Currency: "USD", ProductID: plan.StripePriceId,
	}
}

func TestProviderPaymentBindingCommitsWithSubscriptionRenewal(t *testing.T) {
	truncateTables(t)
	subscription, evidence := createProviderPaymentLifecycleFixture(t, 401, 401, "binding-renewal-order", "sub_binding_renewal")
	objects := []ProviderPaymentObject{
		{ObjectType: ProviderPaymentObjectInvoice, ObjectID: "in_binding_renewal"},
		{ObjectType: ProviderPaymentObjectPaymentIntent, ObjectID: "pi_binding_renewal"},
		{ObjectType: ProviderPaymentObjectCharge, ObjectID: "ch_binding_renewal"},
		{ObjectType: ProviderPaymentObjectSubscription, ObjectID: subscription.ProviderSubscriptionID},
	}

	require.NoError(t, ApplySubscriptionLifecycleEventAtWithEvidenceAndOptions(
		PaymentProviderStripe,
		"evt_binding_renewal",
		subscription.ProviderSubscriptionID,
		"active",
		200,
		300,
		evidence,
		"{}",
		SubscriptionLifecycleOptions{
			RenewalOnly: true,
			ProviderScope: &ProviderLifecycleScope{
				ProviderAccountID:   "acct_platform",
				ProviderEnvironment: "test",
			},
			PaymentBinding: &ProviderPaymentBindingScope{
				ProviderAccountID:   "acct_platform",
				ProviderEnvironment: "test",
				Objects:             objects,
			},
		},
	))

	var bindings []ProviderPaymentBinding
	require.NoError(t, DB.Order("object_type ASC").Find(&bindings).Error)
	require.Len(t, bindings, len(objects))
	for _, binding := range bindings {
		assert.Equal(t, "binding-renewal-order", binding.OrderTradeNo)
		assert.Equal(t, PaymentEventOrderSubscription, binding.OrderKind)
		assert.Equal(t, "acct_platform", binding.ProviderAccountID)
		assert.Equal(t, "test", binding.ProviderEnvironment)
	}
}

func TestProviderPaymentBindingConflictRollsBackSubscriptionRenewal(t *testing.T) {
	truncateTables(t)
	subscription, evidence := createProviderPaymentLifecycleFixture(t, 402, 402, "binding-conflict-order", "sub_binding_conflict")
	conflictingCharge := ProviderPaymentObject{ObjectType: ProviderPaymentObjectCharge, ObjectID: "ch_binding_conflict"}
	require.NoError(t, BindProviderPaymentBindings(ProviderPaymentBindingBatch{
		Provider:            PaymentProviderStripe,
		ProviderAccountID:   "acct_platform",
		ProviderEnvironment: "test",
		OrderTradeNo:        "different-order",
		OrderKind:           PaymentEventOrderTopUp,
		Objects:             []ProviderPaymentObject{conflictingCharge},
	}))

	err := ApplySubscriptionLifecycleEventAtWithEvidenceAndOptions(
		PaymentProviderStripe,
		"evt_binding_conflict",
		subscription.ProviderSubscriptionID,
		"active",
		200,
		300,
		evidence,
		"{}",
		SubscriptionLifecycleOptions{
			RenewalOnly: true,
			ProviderScope: &ProviderLifecycleScope{
				ProviderAccountID:   "acct_platform",
				ProviderEnvironment: "test",
			},
			PaymentBinding: &ProviderPaymentBindingScope{
				ProviderAccountID:   "acct_platform",
				ProviderEnvironment: "test",
				Objects: []ProviderPaymentObject{
					{ObjectType: ProviderPaymentObjectInvoice, ObjectID: "in_should_rollback"},
					conflictingCharge,
				},
			},
		},
	)
	require.ErrorIs(t, err, ErrProviderPaymentBindingConflict)

	var eventCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).Where("provider_trade_no = ?", "evt_binding_conflict").Count(&eventCount).Error)
	assert.Zero(t, eventCount)
	var invoiceCount int64
	require.NoError(t, DB.Model(&ProviderPaymentBinding{}).Where("object_id = ?", "in_should_rollback").Count(&invoiceCount).Error)
	assert.Zero(t, invoiceCount)
	var got UserSubscription
	require.NoError(t, DB.First(&got, subscription.Id).Error)
	assert.EqualValues(t, 100, got.EndTime)
	assert.EqualValues(t, 25, got.AmountUsed)
}

func TestProviderPaymentBindingMissingCheckoutScopeLeavesRenewalManual(t *testing.T) {
	truncateTables(t)
	subscription, evidence := createProviderPaymentLifecycleFixture(t, 403, 403, "binding-manual-order", "sub_binding_manual")
	require.NoError(t, DB.Where("object_type = ? AND object_id = ?", ProviderPaymentObjectSubscription, subscription.ProviderSubscriptionID).
		Delete(&ProviderPaymentBinding{}).Error)

	err := ApplySubscriptionLifecycleEventAtWithEvidenceAndOptions(
		PaymentProviderStripe,
		"evt_binding_manual",
		subscription.ProviderSubscriptionID,
		"active",
		200,
		300,
		evidence,
		"{}",
		SubscriptionLifecycleOptions{
			RenewalOnly: true,
			ProviderScope: &ProviderLifecycleScope{
				ProviderAccountID:   "acct_platform",
				ProviderEnvironment: "test",
			},
			PaymentBinding: &ProviderPaymentBindingScope{
				ProviderAccountID:   "acct_platform",
				ProviderEnvironment: "test",
				Objects: []ProviderPaymentObject{
					{ObjectType: ProviderPaymentObjectInvoice, ObjectID: "in_binding_manual"},
					{ObjectType: ProviderPaymentObjectSubscription, ObjectID: subscription.ProviderSubscriptionID},
				},
			},
		},
	)
	require.ErrorIs(t, err, ErrProviderPaymentBindingNotFound)

	var got UserSubscription
	require.NoError(t, DB.First(&got, subscription.Id).Error)
	assert.EqualValues(t, 100, got.EndTime)
	assert.EqualValues(t, 25, got.AmountUsed)
	var eventCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).Where("provider_trade_no = ?", "evt_binding_manual").Count(&eventCount).Error)
	assert.Zero(t, eventCount)
	var invoiceCount int64
	require.NoError(t, DB.Model(&ProviderPaymentBinding{}).Where("object_id = ?", "in_binding_manual").Count(&invoiceCount).Error)
	assert.Zero(t, invoiceCount)
}

func TestProviderLifecycleScopeRejectsCrossAccountAndEnvironmentEvents(t *testing.T) {
	tests := []struct {
		name        string
		status      string
		accountID   string
		environment string
		options     SubscriptionLifecycleOptions
	}{
		{
			name:        "failed payment from another account",
			status:      "past_due",
			accountID:   "acct_other",
			environment: "test",
		},
		{
			name:        "deletion from another environment",
			status:      "cancelled",
			accountID:   "acct_platform",
			environment: "live",
			options: SubscriptionLifecycleOptions{
				ImmediateRevoke: true,
			},
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			truncateTables(t)
			tradeNo := fmt.Sprintf("binding-lifecycle-scope-%d", index)
			subscriptionID := fmt.Sprintf("sub_binding_lifecycle_scope_%d", index)
			subscription, evidence := createProviderPaymentLifecycleFixture(t, 410+index, 410+index, tradeNo, subscriptionID)
			test.options.ProviderScope = &ProviderLifecycleScope{
				ProviderAccountID:   test.accountID,
				ProviderEnvironment: test.environment,
			}

			eventID := fmt.Sprintf("evt_binding_lifecycle_scope_%d", index)
			err := ApplySubscriptionLifecycleEventAtWithEvidenceAndOptions(
				PaymentProviderStripe,
				eventID,
				subscription.ProviderSubscriptionID,
				test.status,
				200,
				0,
				evidence,
				"{}",
				test.options,
			)
			require.ErrorIs(t, err, ErrProviderPaymentBindingNotFound)

			var got UserSubscription
			require.NoError(t, DB.First(&got, subscription.Id).Error)
			assert.Equal(t, "active", got.Status)
			assert.EqualValues(t, 100, got.EndTime)
			assert.EqualValues(t, 25, got.AmountUsed)
			assert.Zero(t, got.ProviderLifecycleEventTime)

			var eventCount int64
			require.NoError(t, DB.Model(&PaymentEvent{}).Where("provider_trade_no = ?", eventID).Count(&eventCount).Error)
			assert.Zero(t, eventCount)
		})
	}
}

func TestProviderLifecycleScopeAllowsOwnedEvent(t *testing.T) {
	truncateTables(t)
	subscription, evidence := createProviderPaymentLifecycleFixture(t, 412, 412, "binding-lifecycle-owned", "sub_binding_lifecycle_owned")

	require.NoError(t, ApplySubscriptionLifecycleEventAtWithEvidenceAndOptions(
		PaymentProviderStripe,
		"evt_binding_lifecycle_owned",
		subscription.ProviderSubscriptionID,
		"past_due",
		200,
		0,
		evidence,
		"{}",
		SubscriptionLifecycleOptions{
			ProviderScope: &ProviderLifecycleScope{
				ProviderAccountID:   "acct_platform",
				ProviderEnvironment: "test",
			},
		},
	))

	var got UserSubscription
	require.NoError(t, DB.First(&got, subscription.Id).Error)
	assert.Equal(t, "past_due", got.Status)
	assert.EqualValues(t, 200, got.ProviderLifecycleEventTime)

	var eventCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).Where("provider_trade_no = ?", "evt_binding_lifecycle_owned").Count(&eventCount).Error)
	assert.EqualValues(t, 1, eventCount)
}

func TestProviderLifecycleScopeIsRequiredForStripe(t *testing.T) {
	truncateTables(t)
	subscription, evidence := createProviderPaymentLifecycleFixture(t, 413, 413, "binding-lifecycle-required", "sub_binding_lifecycle_required")
	var initialEventCount, initialBindingCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).Count(&initialEventCount).Error)
	require.NoError(t, DB.Model(&ProviderPaymentBinding{}).Count(&initialBindingCount).Error)

	err := ApplySubscriptionLifecycleEventAtWithEvidenceAndOptions(
		PaymentProviderStripe,
		"evt_binding_lifecycle_required",
		subscription.ProviderSubscriptionID,
		"past_due",
		200,
		0,
		evidence,
		"{}",
		SubscriptionLifecycleOptions{
			RenewalOnly: true,
			PaymentBinding: &ProviderPaymentBindingScope{
				ProviderAccountID:   "acct_platform",
				ProviderEnvironment: "test",
				Objects: []ProviderPaymentObject{{
					ObjectType: ProviderPaymentObjectInvoice,
					ObjectID:   "in_binding_lifecycle_required",
				}},
			},
		},
	)
	require.ErrorIs(t, err, ErrProviderPaymentBindingInvalid)

	var got UserSubscription
	require.NoError(t, DB.First(&got, subscription.Id).Error)
	assert.Equal(t, "active", got.Status)
	assert.EqualValues(t, 100, got.EndTime)
	assert.EqualValues(t, 25, got.AmountUsed)
	assert.Zero(t, got.ProviderLifecycleEventTime)

	var eventCount, bindingCount int64
	require.NoError(t, DB.Model(&PaymentEvent{}).Count(&eventCount).Error)
	require.NoError(t, DB.Model(&ProviderPaymentBinding{}).Count(&bindingCount).Error)
	assert.Equal(t, initialEventCount, eventCount)
	assert.Equal(t, initialBindingCount, bindingCount)
}

func TestProviderLifecycleScopeMustMatchPositivePaymentScope(t *testing.T) {
	tests := []struct {
		name               string
		paymentAccountID   string
		paymentEnvironment string
	}{
		{name: "cross account", paymentAccountID: "acct_other", paymentEnvironment: "test"},
		{name: "cross environment", paymentAccountID: "acct_platform", paymentEnvironment: "live"},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			truncateTables(t)
			tradeNo := fmt.Sprintf("binding-lifecycle-scope-conflict-%d", index)
			subscriptionID := fmt.Sprintf("sub_binding_lifecycle_scope_conflict_%d", index)
			subscription, evidence := createProviderPaymentLifecycleFixture(t, 414+index, 414+index, tradeNo, subscriptionID)
			var initialEventCount, initialBindingCount int64
			require.NoError(t, DB.Model(&PaymentEvent{}).Count(&initialEventCount).Error)
			require.NoError(t, DB.Model(&ProviderPaymentBinding{}).Count(&initialBindingCount).Error)

			eventID := fmt.Sprintf("evt_binding_lifecycle_scope_conflict_%d", index)
			err := ApplySubscriptionLifecycleEventAtWithEvidenceAndOptions(
				PaymentProviderStripe,
				eventID,
				subscription.ProviderSubscriptionID,
				"active",
				200,
				300,
				evidence,
				"{}",
				SubscriptionLifecycleOptions{
					RenewalOnly: true,
					ProviderScope: &ProviderLifecycleScope{
						ProviderAccountID:   "acct_platform",
						ProviderEnvironment: "test",
					},
					PaymentBinding: &ProviderPaymentBindingScope{
						ProviderAccountID:   test.paymentAccountID,
						ProviderEnvironment: test.paymentEnvironment,
						Objects: []ProviderPaymentObject{{
							ObjectType: ProviderPaymentObjectInvoice,
							ObjectID:   "in_" + eventID,
						}},
					},
				},
			)
			require.ErrorIs(t, err, ErrProviderPaymentBindingConflict)

			var got UserSubscription
			require.NoError(t, DB.First(&got, subscription.Id).Error)
			assert.Equal(t, "active", got.Status)
			assert.EqualValues(t, 100, got.EndTime)
			assert.EqualValues(t, 25, got.AmountUsed)
			assert.Zero(t, got.ProviderLifecycleEventTime)

			var eventCount, bindingCount int64
			require.NoError(t, DB.Model(&PaymentEvent{}).Count(&eventCount).Error)
			require.NoError(t, DB.Model(&ProviderPaymentBinding{}).Count(&bindingCount).Error)
			assert.Equal(t, initialEventCount, eventCount)
			assert.Equal(t, initialBindingCount, bindingCount)
		})
	}
}

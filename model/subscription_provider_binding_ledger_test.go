package model

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newBoundSubscriptionOrderFixture(t *testing.T, tradeNo string, userID, planID int, provider string) (*SubscriptionOrder, *UserSubscription) {
	t.Helper()
	user := &User{Id: userID, Username: fmt.Sprintf("binding-user-%d", userID), Status: common.UserStatusEnabled, AffCode: fmt.Sprintf("binding-aff-%d", userID)}
	require.NoError(t, DB.Create(user).Error)
	plan := &SubscriptionPlan{
		Id: planID, Title: "binding plan", PriceAmount: 10, Currency: "USD",
		DurationUnit: SubscriptionDurationMonth, DurationValue: 1, Enabled: true,
		TotalAmount: 1000, StripePriceId: "price-binding",
	}
	if provider == PaymentProviderCreem {
		plan.CreemProductId = "product-binding"
	}
	require.NoError(t, DB.Create(plan).Error)
	order := &SubscriptionOrder{
		UserId: user.Id, PlanId: plan.Id, Money: plan.PriceAmount, TradeNo: tradeNo,
		PaymentMethod: provider, PaymentProvider: provider, Status: common.TopUpStatusSuccess,
		CreateTime: time.Now().Unix(), ProviderMerchantID: "merchant-binding",
		ProviderOrderName: plan.Title, ProviderAmount: "10.00", ProviderCurrency: "USD",
		ProviderProductID: func() string {
			if provider == PaymentProviderCreem {
				return plan.CreemProductId
			}
			return plan.StripePriceId
		}(),
	}
	require.NoError(t, order.SetEntitlementSnapshot(plan))
	require.NoError(t, DB.Create(order).Error)
	sub := &UserSubscription{
		UserId: user.Id, PlanId: plan.Id, AmountTotal: plan.TotalAmount,
		StartTime: time.Now().Unix(), EndTime: time.Now().Add(time.Hour).Unix(),
		Status: "active", Source: "order", SubscriptionOrderTradeNo: tradeNo,
	}
	require.NoError(t, DB.Create(sub).Error)
	return order, sub
}

func TestBindSubscriptionProviderIDToOrderUsesDurableUniqueFence(t *testing.T) {
	truncateTables(t)
	orderA, subA := newBoundSubscriptionOrderFixture(t, "binding-order-a", 99001, 99002, PaymentProviderStripe)
	require.NoError(t, BindSubscriptionProviderIDToOrder(SubscriptionProviderBinding{
		OrderTradeNo: orderA.TradeNo, Provider: PaymentProviderStripe,
		ProviderSubscriptionID: "sub_binding_same",
		ProductID:              orderA.ProviderProductID, Currency: "USD", Amount: "10",
	}))
	// Replaying the exact callback is idempotent.
	require.NoError(t, BindSubscriptionProviderIDToOrder(SubscriptionProviderBinding{
		OrderTradeNo: orderA.TradeNo, Provider: PaymentProviderStripe,
		ProviderSubscriptionID: "sub_binding_same",
	}))
	var row SubscriptionProviderBindingRecord
	require.NoError(t, DB.Where("provider_subscription_key = ?", mustBindingKey(t, PaymentProviderStripe, "sub_binding_same")).First(&row).Error)
	assert.Equal(t, subA.Id, row.UserSubscriptionID)
	require.NotNil(t, row.OrderTradeNo)
	assert.Equal(t, orderA.TradeNo, *row.OrderTradeNo)

	orderB, subB := newBoundSubscriptionOrderFixture(t, "binding-order-b", 99003, 99004, PaymentProviderStripe)
	err := BindSubscriptionProviderIDToOrder(SubscriptionProviderBinding{
		OrderTradeNo: orderB.TradeNo, Provider: PaymentProviderStripe,
		ProviderSubscriptionID: "sub_binding_same",
	})
	require.ErrorIs(t, err, ErrProviderEventConflict)
	var untouched UserSubscription
	require.NoError(t, DB.First(&untouched, subB.Id).Error)
	assert.Empty(t, untouched.ProviderSubscriptionID)
	var count int64
	require.NoError(t, DB.Model(&SubscriptionProviderBindingRecord{}).
		Where("provider_subscription_key = ?", mustBindingKey(t, PaymentProviderStripe, "sub_binding_same")).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestSubscriptionProviderBindingAllowsSameOpaqueIDAcrossProviders(t *testing.T) {
	truncateTables(t)
	orderA, _ := newBoundSubscriptionOrderFixture(t, "binding-stripe", 99005, 99006, PaymentProviderStripe)
	orderB, _ := newBoundSubscriptionOrderFixture(t, "binding-creem", 99007, 99008, PaymentProviderCreem)
	require.NoError(t, BindSubscriptionProviderIDToOrder(SubscriptionProviderBinding{OrderTradeNo: orderA.TradeNo, Provider: PaymentProviderStripe, ProviderSubscriptionID: "same-opaque-id"}))
	require.NoError(t, BindSubscriptionProviderIDToOrder(SubscriptionProviderBinding{OrderTradeNo: orderB.TradeNo, Provider: PaymentProviderCreem, ProviderSubscriptionID: "same-opaque-id"}))
	var count int64
	require.NoError(t, DB.Model(&SubscriptionProviderBindingRecord{}).Where("provider_subscription_id = ?", "same-opaque-id").Count(&count).Error)
	assert.EqualValues(t, 2, count)
}

func TestSubscriptionLifecycleProviderIsolationUsesBindingNamespace(t *testing.T) {
	truncateTables(t)
	stripe := &UserSubscription{
		UserId: 99011, PlanId: 99012, AmountTotal: 1000, Status: "active", EndTime: 100,
		ProviderSubscriptionID: "shared-lifecycle-id", ProviderSubscriptionProvider: PaymentProviderStripe,
	}
	creem := &UserSubscription{
		UserId: 99013, PlanId: 99014, AmountTotal: 1000, Status: "active", EndTime: 100,
		ProviderSubscriptionID: "shared-lifecycle-id", ProviderSubscriptionProvider: PaymentProviderCreem,
	}
	require.NoError(t, DB.Create(stripe).Error)
	require.NoError(t, DB.Create(creem).Error)
	// Seed both rows through the ledger's legacy backfill path. The same raw ID
	// is valid because the provider namespace is part of the canonical key.
	require.NoError(t, DB.Create(&SubscriptionProviderBindingRecord{
		UserSubscriptionID: stripe.Id, Provider: PaymentProviderStripe,
		ProviderSubscriptionID: stripe.ProviderSubscriptionID,
	}).Error)
	require.NoError(t, DB.Create(&SubscriptionProviderBindingRecord{
		UserSubscriptionID: creem.Id, Provider: PaymentProviderCreem,
		ProviderSubscriptionID: creem.ProviderSubscriptionID,
	}).Error)
	require.NoError(t, ApplySubscriptionLifecycleEvent(PaymentProviderCreem, "evt-provider-isolation", creem.ProviderSubscriptionID, "past_due", 0, "{}"))
	var gotStripe, gotCreem UserSubscription
	require.NoError(t, DB.First(&gotStripe, stripe.Id).Error)
	require.NoError(t, DB.First(&gotCreem, creem.Id).Error)
	assert.Equal(t, "active", gotStripe.Status)
	assert.Equal(t, "past_due", gotCreem.Status)
}

func TestSubscriptionLifecycleAdoptsSingleUnnamespacedLegacyRow(t *testing.T) {
	truncateTables(t)
	const providerSubscriptionID = "single-legacy-unnamespaced-id"
	sub := &UserSubscription{
		UserId: 99019, PlanId: 99020, AmountTotal: 1000, Status: "active", EndTime: 100,
		ProviderSubscriptionID: providerSubscriptionID,
		// ProviderSubscriptionProvider is intentionally empty: this is the
		// compatibility path for a pre-ledger row.  Adoption is allowed only when
		// this is the sole raw candidate for the provider/id pair.
	}
	require.NoError(t, DB.Create(sub).Error)

	require.NoError(t, ApplySubscriptionLifecycleEvent(PaymentProviderCreem, "evt-single-legacy-unnamespaced", providerSubscriptionID, "past_due", 0, "{}"))
	var got UserSubscription
	require.NoError(t, DB.First(&got, sub.Id).Error)
	assert.Equal(t, "past_due", got.Status)
	assert.Equal(t, PaymentProviderCreem, got.ProviderSubscriptionProvider)
	var binding SubscriptionProviderBindingRecord
	require.NoError(t, DB.Where("user_subscription_id = ?", sub.Id).First(&binding).Error)
	assert.Equal(t, PaymentProviderCreem, binding.Provider)
}

func TestSubscriptionLifecycleRejectsUnnamespacedLegacyCollision(t *testing.T) {
	truncateTables(t)
	const providerSubscriptionID = "collision-legacy-unnamespaced-id"
	for _, userID := range []int{99023, 99024} {
		require.NoError(t, DB.Create(&UserSubscription{
			UserId: userID, PlanId: userID, AmountTotal: 1000, Status: "active", EndTime: 100,
			ProviderSubscriptionID: providerSubscriptionID,
		}).Error)
	}

	err := ApplySubscriptionLifecycleEvent(PaymentProviderCreem, "evt-collision-legacy-unnamespaced", providerSubscriptionID, "past_due", 0, "{}")
	require.ErrorIs(t, err, ErrProviderEventConflict)
	var count int64
	require.NoError(t, DB.Model(&SubscriptionProviderBindingRecord{}).
		Where("provider_subscription_id = ?", providerSubscriptionID).Count(&count).Error)
	assert.Zero(t, count, "ambiguous legacy rows must not be adopted into the binding ledger")
}

func TestSubscriptionLifecycleRejectsUnnamespacedRowWhenOtherProviderUsesSameID(t *testing.T) {
	truncateTables(t)
	const providerSubscriptionID = "cross-provider-legacy-ambiguity"
	legacy := &UserSubscription{
		UserId: 99025, PlanId: 99026, AmountTotal: 1000, Status: "active", EndTime: 100,
		ProviderSubscriptionID: providerSubscriptionID,
	}
	namespaced := &UserSubscription{
		UserId: 99027, PlanId: 99028, AmountTotal: 1000, Status: "active", EndTime: 100,
		ProviderSubscriptionID: providerSubscriptionID, ProviderSubscriptionProvider: PaymentProviderStripe,
	}
	require.NoError(t, DB.Create(legacy).Error)
	require.NoError(t, DB.Create(namespaced).Error)

	err := ApplySubscriptionLifecycleEvent(PaymentProviderCreem, "evt-cross-provider-legacy-ambiguity", providerSubscriptionID, "past_due", 0, "{}")
	require.ErrorIs(t, err, ErrProviderEventConflict)
	var got UserSubscription
	require.NoError(t, DB.First(&got, legacy.Id).Error)
	assert.Empty(t, got.ProviderSubscriptionProvider)
	assert.Equal(t, "active", got.Status)
	var count int64
	require.NoError(t, DB.Model(&SubscriptionProviderBindingRecord{}).
		Where("provider_subscription_id = ?", providerSubscriptionID).Count(&count).Error)
	assert.Zero(t, count, "a cross-provider collision must not adopt the unnamespaced legacy row")
}

func TestSubscriptionLifecycleRejectsDanglingProviderBindingOrder(t *testing.T) {
	truncateTables(t)
	const providerSubscriptionID = "dangling-order-provider-id"
	sub := &UserSubscription{
		UserId: 99015, PlanId: 99016, AmountTotal: 1000, Status: "active", EndTime: 100,
		ProviderSubscriptionID: providerSubscriptionID, ProviderSubscriptionProvider: PaymentProviderCreem,
		SubscriptionOrderTradeNo: "missing-order-for-binding",
	}
	require.NoError(t, DB.Create(sub).Error)
	missingTradeNo := "missing-order-for-binding"
	require.NoError(t, DB.Create(&SubscriptionProviderBindingRecord{
		UserSubscriptionID: sub.Id, OrderTradeNo: &missingTradeNo,
		Provider: PaymentProviderCreem, ProviderSubscriptionID: providerSubscriptionID,
	}).Error)

	err := ApplySubscriptionLifecycleEvent(PaymentProviderCreem, "evt-dangling-binding-order", providerSubscriptionID, "past_due", 0, "{}")
	require.ErrorIs(t, err, ErrProviderEventConflict)
	var unchanged UserSubscription
	require.NoError(t, DB.First(&unchanged, sub.Id).Error)
	assert.Equal(t, "active", unchanged.Status, "a ledger row with a missing order must not mutate the entitlement")
}

func TestSubscriptionLifecycleRejectsProviderBindingWithEmptyDenormalizedProvider(t *testing.T) {
	truncateTables(t)
	const providerSubscriptionID = "empty-denormalized-provider-id"
	sub := &UserSubscription{
		UserId: 99017, PlanId: 99018, AmountTotal: 1000, Status: "active", EndTime: 100,
		ProviderSubscriptionID: providerSubscriptionID,
		// A ledger row claiming a provider identity must agree with this column;
		// an empty value here indicates a partial/manual repair.
	}
	require.NoError(t, DB.Create(sub).Error)
	require.NoError(t, DB.Create(&SubscriptionProviderBindingRecord{
		UserSubscriptionID: sub.Id, Provider: PaymentProviderCreem,
		ProviderSubscriptionID: providerSubscriptionID,
	}).Error)

	err := ApplySubscriptionLifecycleEvent(PaymentProviderCreem, "evt-empty-denormalized-provider", providerSubscriptionID, "past_due", 0, "{}")
	require.ErrorIs(t, err, ErrProviderEventConflict)
	var unchanged UserSubscription
	require.NoError(t, DB.First(&unchanged, sub.Id).Error)
	assert.Empty(t, unchanged.ProviderSubscriptionProvider)
	assert.Equal(t, "active", unchanged.Status)
}

func TestSubscriptionProviderBindingDatabaseUniqueIndexesRejectDuplicates(t *testing.T) {
	truncateTables(t)
	first := &SubscriptionProviderBindingRecord{
		UserSubscriptionID: 99021, Provider: PaymentProviderStripe,
		ProviderSubscriptionID: "database-fenced-id",
	}
	require.NoError(t, DB.Create(first).Error)
	duplicateIdentity := &SubscriptionProviderBindingRecord{
		UserSubscriptionID: 99022, Provider: PaymentProviderStripe,
		ProviderSubscriptionID: "database-fenced-id",
	}
	assert.Error(t, DB.Create(duplicateIdentity).Error, "provider identity must be fenced by a database unique index")
	duplicateEntitlement := &SubscriptionProviderBindingRecord{
		UserSubscriptionID: first.UserSubscriptionID, Provider: PaymentProviderCreem,
		ProviderSubscriptionID: "different-entitlement-id",
	}
	assert.Error(t, DB.Create(duplicateEntitlement).Error, "one entitlement cannot have two provider identities")
}

func TestSubscriptionProviderBindingRejectsSecondIDForOneEntitlement(t *testing.T) {
	truncateTables(t)
	order, _ := newBoundSubscriptionOrderFixture(t, "binding-one-entitlement", 99009, 99010, PaymentProviderStripe)
	require.NoError(t, BindSubscriptionProviderIDToOrder(SubscriptionProviderBinding{OrderTradeNo: order.TradeNo, Provider: PaymentProviderStripe, ProviderSubscriptionID: "sub_first"}))
	err := BindSubscriptionProviderIDToOrder(SubscriptionProviderBinding{OrderTradeNo: order.TradeNo, Provider: PaymentProviderStripe, ProviderSubscriptionID: "sub_second"})
	require.ErrorIs(t, err, ErrProviderEventConflict)
}

func TestMigrateSubscriptionProviderBindingBackfillsUnambiguousRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&UserSubscription{}))
	require.NoError(t, db.Create(&UserSubscription{
		Id: 99101, UserId: 99102, PlanId: 99103, ProviderSubscriptionID: "legacy-sub",
		ProviderSubscriptionProvider: PaymentProviderStripe, SubscriptionOrderTradeNo: "legacy-order",
	}).Error)
	require.NoError(t, MigrateSubscriptionProviderBindingSchemaExpand(db, common.DatabaseTypeSQLite))
	var binding SubscriptionProviderBindingRecord
	require.NoError(t, db.Where("user_subscription_id = ?", 99101).First(&binding).Error)
	assert.Equal(t, PaymentProviderStripe, binding.Provider)
	assert.Equal(t, "legacy-sub", binding.ProviderSubscriptionID)
	require.NotNil(t, binding.OrderTradeNo)
	assert.Equal(t, "legacy-order", *binding.OrderTradeNo)
	assert.True(t, db.Migrator().HasIndex(&SubscriptionProviderBindingRecord{}, subscriptionProviderBindingIdentityIndexName))
	assert.True(t, db.Migrator().HasIndex(&SubscriptionProviderBindingRecord{}, subscriptionProviderBindingEntitlementIndexName))
}

func TestMigrateSubscriptionProviderBindingRejectsLegacyDuplicateIdentity(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&UserSubscription{}))
	for id := 99111; id <= 99112; id++ {
		require.NoError(t, db.Create(&UserSubscription{
			Id: id, UserId: id, PlanId: id, ProviderSubscriptionID: "legacy-duplicate",
			ProviderSubscriptionProvider: PaymentProviderStripe,
		}).Error)
	}
	err = MigrateSubscriptionProviderBindingSchemaExpand(db, common.DatabaseTypeSQLite)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
}

func TestMigrateSubscriptionProviderBindingRepairsPartialTableConcurrently(t *testing.T) {
	// A rolling deployment can have two application masters execute the expand
	// migration at once.  Start from a table that only has its primary key so
	// both callers race on the same ADD COLUMN statements; the loser of each
	// DDL race must observe the winning column and continue.
	dsn := filepath.Join(t.TempDir(), "subscription-bindings.sqlite") + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	open := func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
		return db
	}
	first := open()
	require.NoError(t, first.Exec("CREATE TABLE subscription_provider_bindings (id integer primary key)").Error)
	second := open()

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, db := range []*gorm.DB{first, second} {
		wg.Add(1)
		go func(db *gorm.DB) {
			defer wg.Done()
			<-start
			errs <- MigrateSubscriptionProviderBindingSchemaExpand(db, common.DatabaseTypeSQLite)
		}(db)
	}
	close(start)
	wg.Wait()
	for i := 0; i < 2; i++ {
		require.NoError(t, <-errs)
	}

	for _, column := range subscriptionProviderBindingColumns() {
		assert.True(t, first.Migrator().HasColumn(&SubscriptionProviderBindingRecord{}, column.Name), column.DBName)
	}
	for _, index := range []string{
		subscriptionProviderBindingIdentityIndexName,
		subscriptionProviderBindingEntitlementIndexName,
		subscriptionProviderBindingOrderIndexName,
		"idx_spb_provider_id",
	} {
		assert.True(t, first.Migrator().HasIndex(&SubscriptionProviderBindingRecord{}, index), index)
	}
}

func TestMigrateSubscriptionProviderBindingFreshTableIsIdempotent(t *testing.T) {
	db := newPaymentMigrationSQLite(t)
	// The expand pass is intentionally allowed to create the complete ledger on
	// a fresh database because callback handlers must have the uniqueness fence
	// before the broad startup AutoMigrate runs.
	require.NoError(t, MigrateSubscriptionProviderBindingSchemaExpand(db, common.DatabaseTypeSQLite))
	require.NoError(t, MigrateSubscriptionProviderBindingSchemaExpand(db, common.DatabaseTypeSQLite))
	for _, column := range subscriptionProviderBindingColumns() {
		assert.True(t, db.Migrator().HasColumn(&SubscriptionProviderBindingRecord{}, column.Name), column.DBName)
	}
}

func mustBindingKey(t *testing.T, provider, id string) string {
	t.Helper()
	key, ok := SubscriptionProviderBindingKey(provider, id)
	require.True(t, ok)
	return key
}

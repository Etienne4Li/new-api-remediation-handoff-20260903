package model

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// These legacy structs intentionally contain only the columns that existed
// before the immutable checkout snapshot was introduced. The test therefore
// exercises the real expand path instead of letting AutoMigrate create the
// target schema in one step.
type legacyPaymentTopUp struct {
	Id      int    `gorm:"primaryKey"`
	TradeNo string `gorm:"type:varchar(255);uniqueIndex"`
	Amount  int64
}

func (legacyPaymentTopUp) TableName() string { return "top_ups" }

type legacyPaymentSubscriptionOrder struct {
	Id      int    `gorm:"primaryKey"`
	TradeNo string `gorm:"type:varchar(255);uniqueIndex"`
	PlanId  int
}

func (legacyPaymentSubscriptionOrder) TableName() string { return "subscription_orders" }

type legacyPaymentUserSubscription struct {
	Id                     int    `gorm:"primaryKey"`
	ProviderSubscriptionID string `gorm:"type:varchar(255)"`
}

func (legacyPaymentUserSubscription) TableName() string { return "user_subscriptions" }

type legacyProviderRefundEvent struct {
	ID         int64  `gorm:"primaryKey"`
	EventKey   string `gorm:"type:char(64);not null;uniqueIndex:idx_provider_refund_event_key"`
	Provider   string `gorm:"type:varchar(50);not null"`
	DeliveryID string `gorm:"type:varchar(255);not null"`
	EventType  string `gorm:"type:varchar(64);not null"`
	Status     string `gorm:"type:varchar(32);not null"`
}

func (legacyProviderRefundEvent) TableName() string { return "provider_refund_events" }

func newPaymentMigrationSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	// An in-memory SQLite database is scoped to one underlying connection.
	// Keep the migration's metadata queries and DDL on that same connection so
	// the test cannot intermittently observe a fresh empty database.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	return db
}

func TestPaymentEventKeyIsStableAndSeparatesIdentities(t *testing.T) {
	key, ok := PaymentEventKey("stripe", "pi_123")
	require.True(t, ok)
	assert.Equal(t, "a838808b0a16b19607da60c958c2c7a5be2b7de50d62985cc2be9f830071d880", key)

	trimmed, ok := PaymentEventKey("  stripe  ", "  pi_123  ")
	require.True(t, ok)
	assert.Equal(t, key, trimmed)

	otherProvider, ok := PaymentEventKey("stripe-connect", "pi_123")
	require.True(t, ok)
	otherTrade, ok := PaymentEventKey("stripe", "pi_124")
	require.True(t, ok)
	assert.NotEqual(t, key, otherProvider)
	assert.NotEqual(t, key, otherTrade)

	_, ok = PaymentEventKey("", "pi_123")
	assert.False(t, ok)
	_, ok = PaymentEventKey("stripe", "")
	assert.False(t, ok)
}

func TestPaymentSchemaExpandSQLiteAddsSnapshotColumnsAndIdempotencyFence(t *testing.T) {
	db := newPaymentMigrationSQLite(t)
	require.NoError(t, db.AutoMigrate(&legacyPaymentTopUp{}, &legacyPaymentSubscriptionOrder{}))
	require.NoError(t, db.Create(&legacyPaymentTopUp{Id: 11, TradeNo: "legacy-topup", Amount: 2}).Error)
	require.NoError(t, db.Create(&legacyPaymentSubscriptionOrder{Id: 12, TradeNo: "legacy-subscription", PlanId: 3}).Error)
	require.NoError(t, db.AutoMigrate(&legacyPaymentUserSubscription{}))
	require.NoError(t, db.Create(&legacyPaymentUserSubscription{Id: 13, ProviderSubscriptionID: "legacy-provider-subscription"}).Error)
	require.NoError(t, db.AutoMigrate(&legacyProviderRefundEvent{}))
	require.NoError(t, db.Create(&legacyProviderRefundEvent{
		ID: 14, EventKey: ProviderRefundEventKey("waffo_pancake", "legacy-refund-delivery"),
		Provider: "waffo_pancake", DeliveryID: "legacy-refund-delivery",
		EventType: "refund.succeeded", Status: string(ProviderRefundEventManualReconciliation),
	}).Error)

	require.NoError(t, MigratePaymentSchemaExpand(db, common.DatabaseTypeSQLite))

	for _, column := range []string{
		"credited_quota",
		"provider_trade_no",
		"provider_merchant_id",
		"provider_order_name",
		"provider_amount",
		"provider_product_id",
		"provider_store_id",
		"provider_checkout_id",
		"provider_subscription_id",
		"provider_currency",
		"provider_event_id",
		"provider_key_fingerprint",
		"provider_payload",
	} {
		assert.True(t, db.Migrator().HasColumn(&TopUp{}, column), "top_ups.%s", column)
	}
	for _, column := range []string{
		"settlement_resolution",
		"settlement_error_code",
		"provider_trade_no",
		"provider_merchant_id",
		"provider_order_name",
		"provider_amount",
		"provider_product_id",
		"provider_store_id",
		"provider_checkout_id",
		"provider_subscription_id",
		"provider_currency",
		"provider_event_id",
		"provider_key_fingerprint",
		"provider_payload",
		"entitlement_snapshot",
	} {
		assert.True(t, db.Migrator().HasColumn(&SubscriptionOrder{}, column), "subscription_orders.%s", column)
	}
	for _, column := range []string{"quota_reset_period", "quota_reset_custom_seconds", "provider_subscription_provider", "provider_lifecycle_event_time"} {
		assert.True(t, db.Migrator().HasColumn(&UserSubscription{}, column), "user_subscriptions.%s", column)
	}
	for _, column := range []string{
		"provider_account_id",
		"provider_environment",
		"effect_key",
		"effect_id",
		"related_effect_id",
		"provider_object_type",
		"provider_trade_no",
		"amount",
		"currency",
		"wallet_delta",
		"decision_reason",
	} {
		assert.True(t, db.Migrator().HasColumn(&ProviderRefundEvent{}, column), "provider_refund_events.%s", column)
	}

	assert.True(t, db.Migrator().HasTable(&PaymentEvent{}))
	assert.True(t, db.Migrator().HasIndex(&PaymentEvent{}, paymentEventKeyIndexName))
	assert.True(t, db.Migrator().HasTable(&ProviderReversalEffect{}))
	assert.True(t, db.Migrator().HasIndex(&ProviderReversalEffect{}, providerReversalEffectKeyIndexName))
	assert.True(t, db.Migrator().HasIndex(&ProviderReversalEffect{}, providerReversalRelatedEffectIndexName))
	assert.True(t, db.Migrator().HasIndex(&ProviderReversalEffect{}, providerReversalOrderIndexName))

	var topUp legacyPaymentTopUp
	require.NoError(t, db.Where("trade_no = ?", "legacy-topup").First(&topUp).Error)
	assert.Equal(t, int64(2), topUp.Amount, "expand migration must preserve legacy data")
	var order legacyPaymentSubscriptionOrder
	require.NoError(t, db.Where("trade_no = ?", "legacy-subscription").First(&order).Error)
	assert.Equal(t, 3, order.PlanId, "expand migration must preserve legacy data")
	var legacySub legacyPaymentUserSubscription
	require.NoError(t, db.Where("id = ?", 13).First(&legacySub).Error)
	assert.Equal(t, "legacy-provider-subscription", legacySub.ProviderSubscriptionID, "expand migration must preserve legacy subscription identity")
	var legacyRefund legacyProviderRefundEvent
	require.NoError(t, db.Where("id = ?", 14).First(&legacyRefund).Error)
	assert.Equal(t, "legacy-refund-delivery", legacyRefund.DeliveryID, "expand migration must preserve refund delivery audit rows")

	relatedKey := ProviderReversalEffectKey("stripe", "dispute.funds_withdrawn", "dp_once")
	require.NoError(t, db.Create(&ProviderReversalEffect{
		EffectKey: ProviderReversalEffectKey("stripe", "dispute.funds_reinstated", "dp_once"),
		Provider:  "stripe", ProviderAccountID: "acct_test", ProviderEnvironment: "test",
		EffectID: "dp_once", RelatedEffectID: "dp_once", RelatedEffectKey: &relatedKey,
		ProviderTradeNo: "pi_once", OrderTradeNo: "legacy-topup", OrderKind: PaymentEventOrderTopUp,
		EventType: "dispute.funds_reinstated", Amount: "1", Currency: "USD",
		Status: ProviderReversalEffectApplied, Outcome: ProviderRefundEventApplied,
	}).Error)
	assert.Error(t, db.Create(&ProviderReversalEffect{
		EffectKey: ProviderReversalEffectKey("stripe", "dispute.funds_reinstated", "dp_twice"),
		Provider:  "stripe", ProviderAccountID: "acct_test", ProviderEnvironment: "test",
		EffectID: "dp_twice", RelatedEffectID: "dp_once", RelatedEffectKey: &relatedKey,
		ProviderTradeNo: "pi_once", OrderTradeNo: "legacy-topup", OrderKind: PaymentEventOrderTopUp,
		EventType: "dispute.funds_reinstated", Amount: "1", Currency: "USD",
		Status: ProviderReversalEffectApplied, Outcome: ProviderRefundEventApplied,
	}).Error, "one withdrawn effect may be reinstated only once")

	// The event ledger's uniqueness is a database invariant, not merely an
	// application convention.
	require.NoError(t, db.Create(&PaymentEvent{
		Provider:        "epay",
		ProviderTradeNo: "provider-once",
		OrderTradeNo:    "legacy-topup",
		OrderKind:       PaymentEventOrderTopUp,
	}).Error)
	assert.Error(t, db.Create(&PaymentEvent{
		Provider:        "epay",
		ProviderTradeNo: "provider-once",
		OrderTradeNo:    "legacy-subscription",
		OrderKind:       PaymentEventOrderSubscription,
	}).Error)

	// A second startup must be a no-op and must not rewrite the tables.
	require.NoError(t, MigratePaymentSchemaExpand(db, common.DatabaseTypeSQLite))
}

func TestPaymentSchemaExpandSQLiteBackfillsLegacyPaymentEventKeys(t *testing.T) {
	db := newPaymentMigrationSQLite(t)
	require.NoError(t, db.Exec(`CREATE TABLE payment_events (
		id integer primary key,
		provider varchar(50) NOT NULL,
		provider_trade_no varchar(255) NOT NULL,
		order_trade_no varchar(255) NOT NULL,
		order_kind varchar(32) NOT NULL,
		payload text,
		create_time bigint
	)`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX idx_payment_event_provider_trade
		ON payment_events(provider, provider_trade_no)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO payment_events
		(id, provider, provider_trade_no, order_trade_no, order_kind, payload, create_time)
		VALUES (41, 'stripe', 'pi_legacy', 'order-legacy', ?, 'legacy-payload', 123)`,
		PaymentEventOrderTopUp).Error)

	require.NoError(t, MigratePaymentSchemaExpand(db, common.DatabaseTypeSQLite))

	var event PaymentEvent
	require.NoError(t, db.Where("id = ?", 41).First(&event).Error)
	expected, ok := PaymentEventKey("stripe", "pi_legacy")
	require.True(t, ok)
	assert.Equal(t, expected, event.EventKey)
	assert.Equal(t, "legacy-payload", event.Payload)
	definition, err := paymentIndexDefinition(db, common.DatabaseTypeSQLite, "payment_events", paymentEventKeyIndexName)
	require.NoError(t, err)
	assert.True(t, paymentIndexDefinitionMatches(
		definition, "payment_events", paymentEventKeyIndexName, []string{"event_key"}, true,
	))

	assert.Error(t, db.Exec(`INSERT INTO payment_events
		(event_key, provider, provider_trade_no, order_trade_no, order_kind)
		VALUES (?, 'stripe', 'pi_duplicate_key', 'order-other', ?)`,
		expected, PaymentEventOrderTopUp).Error)
}

func TestPaymentSchemaExpandSQLiteRejectsIncorrectExistingPaymentEventKey(t *testing.T) {
	db := newPaymentMigrationSQLite(t)
	require.NoError(t, db.AutoMigrate(&PaymentEvent{}))
	require.NoError(t, db.Migrator().DropIndex(&PaymentEvent{}, paymentEventKeyIndexName))
	require.NoError(t, db.Exec(`INSERT INTO payment_events
		(event_key, provider, provider_trade_no, order_trade_no, order_kind)
		VALUES (?, 'stripe', 'pi_wrong_key', 'order-a', ?)`,
		strings.Repeat("f", 64), PaymentEventOrderTopUp).Error)

	err := MigratePaymentSchemaExpand(db, common.DatabaseTypeSQLite)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid event key")
	assert.False(t, db.Migrator().HasIndex(&PaymentEvent{}, paymentEventKeyIndexName))
}

func TestPaymentSchemaExpandSQLiteRejectsAmbiguousExistingEvents(t *testing.T) {
	db := newPaymentMigrationSQLite(t)
	require.NoError(t, db.AutoMigrate(&PaymentEvent{}))
	require.NoError(t, db.Migrator().DropIndex(&PaymentEvent{}, paymentEventKeyIndexName))
	require.NoError(t, db.Exec(`INSERT INTO payment_events
		(event_key, provider, provider_trade_no, order_trade_no, order_kind)
		VALUES ('', 'epay', 'provider-duplicate', 'order-a', ?),
		       ('', 'epay', 'provider-duplicate', 'order-b', ?)`,
		PaymentEventOrderTopUp, PaymentEventOrderSubscription).Error)

	err := MigratePaymentSchemaExpand(db, common.DatabaseTypeSQLite)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
	assert.False(t, db.Migrator().HasIndex(&PaymentEvent{}, paymentEventKeyIndexName), "ambiguous rows must not be silently deduplicated")
}

func TestPaymentSchemaExpandSQLiteRejectsPartialEventTableWithRows(t *testing.T) {
	db := newPaymentMigrationSQLite(t)
	// Simulate a manually-created/partially-deployed event table. There is no
	// safe value the migration can invent for provider identity columns.
	require.NoError(t, db.Exec("CREATE TABLE payment_events (id integer primary key, payload text)").Error)
	require.NoError(t, db.Exec("INSERT INTO payment_events (payload) VALUES (?)", "legacy").Error)

	err := MigratePaymentSchemaExpand(db, common.DatabaseTypeSQLite)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required columns")
}

func TestPaymentSchemaExpandSQLiteRejectsNullableEventIdentityColumns(t *testing.T) {
	db := newPaymentMigrationSQLite(t)
	// Every column is present, but provider identity is nullable. A unique
	// index would still allow multiple NULL identities, so the migration must
	// stop instead of claiming the ledger is idempotent.
	require.NoError(t, db.Exec(`CREATE TABLE payment_events (
		id integer primary key,
		provider varchar(50),
		provider_trade_no varchar(255),
		order_trade_no varchar(255),
		order_kind varchar(32),
		payload text,
		create_time bigint
	)`).Error)

	err := MigratePaymentSchemaExpand(db, common.DatabaseTypeSQLite)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nullable")
	assert.False(t, db.Migrator().HasIndex(&PaymentEvent{}, paymentEventKeyIndexName))
}

func TestPaymentSchemaExpandSQLiteRejectsWhitespaceEventIdentity(t *testing.T) {
	db := newPaymentMigrationSQLite(t)
	require.NoError(t, db.AutoMigrate(&PaymentEvent{}))
	require.NoError(t, db.Migrator().DropIndex(&PaymentEvent{}, paymentEventKeyIndexName))
	require.NoError(t, db.Exec(`INSERT INTO payment_events
		(event_key, provider, provider_trade_no, order_trade_no, order_kind)
		VALUES ('', '   ', 'provider-whitespace', 'order-a', ?)`, PaymentEventOrderTopUp).Error)
	var invalidCount int64
	require.NoError(t, db.Table("payment_events").Where("TRIM(provider) = ''").Count(&invalidCount).Error)
	assert.Equal(t, int64(1), invalidCount)

	err := MigratePaymentSchemaExpand(db, common.DatabaseTypeSQLite)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty provider identity")
	assert.False(t, db.Migrator().HasIndex(&PaymentEvent{}, paymentEventKeyIndexName))
}

func TestPaymentSchemaExpandSQLiteRejectsWhitespaceEventIdentityWithExistingIndex(t *testing.T) {
	db := newPaymentMigrationSQLite(t)
	require.NoError(t, db.AutoMigrate(&PaymentEvent{}))
	require.True(t, db.Migrator().HasIndex(&PaymentEvent{}, paymentEventKeyIndexName))
	require.NoError(t, db.Exec(`INSERT INTO payment_events
		(event_key, provider, provider_trade_no, order_trade_no, order_kind)
		VALUES (?, '   ', 'provider-whitespace-indexed', 'order-a', ?)`,
		strings.Repeat("a", 64), PaymentEventOrderTopUp).Error)

	err := MigratePaymentSchemaExpand(db, common.DatabaseTypeSQLite)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty provider identity")
}

func TestPaymentSchemaExpandSQLiteRejectsPartialPaymentEventIndex(t *testing.T) {
	db := newPaymentMigrationSQLite(t)
	require.NoError(t, db.AutoMigrate(&PaymentEvent{}))
	require.NoError(t, db.Migrator().DropIndex(&PaymentEvent{}, paymentEventKeyIndexName))
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX idx_payment_event_key
		ON payment_events(event_key) WHERE event_key <> ''`).Error)

	err := MigratePaymentSchemaExpand(db, common.DatabaseTypeSQLite)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsafe definition")
}

func TestPaymentSchemaExpandSQLiteRejectsIndexOwnedByAnotherTable(t *testing.T) {
	db := newPaymentMigrationSQLite(t)
	require.NoError(t, db.Exec(`CREATE TABLE payment_events (
		id integer primary key,
		event_key char(64) NOT NULL,
		provider varchar(50) NOT NULL,
		provider_trade_no varchar(255) NOT NULL,
		order_trade_no varchar(255) NOT NULL,
		order_kind varchar(32) NOT NULL,
		payload text,
		create_time bigint
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE unrelated_payment_rows (event_key char(64) NOT NULL)`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX idx_payment_event_key
		ON unrelated_payment_rows(event_key)`).Error)

	err := MigratePaymentSchemaExpand(db, common.DatabaseTypeSQLite)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsafe definition")
}

func TestPaymentSchemaExpandSQLiteRejectsUnsafeProviderReversalIndexes(t *testing.T) {
	tests := []struct {
		name       string
		indexName  string
		definition string
	}{
		{
			name:      "partial order index",
			indexName: providerReversalOrderIndexName,
			definition: `CREATE INDEX idx_provider_reversal_order
				ON provider_reversal_effects(provider, provider_account_id, provider_environment, order_trade_no, order_kind)
				WHERE status <> 'processing'`,
		},
		{
			name:      "reordered order index",
			indexName: providerReversalOrderIndexName,
			definition: `CREATE INDEX idx_provider_reversal_order
				ON provider_reversal_effects(provider, provider_account_id, provider_environment, order_kind, order_trade_no)`,
		},
		{
			name:      "expression effect index",
			indexName: providerReversalEffectKeyIndexName,
			definition: `CREATE UNIQUE INDEX idx_provider_reversal_effect_key
				ON provider_reversal_effects(lower(effect_key))`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := newPaymentMigrationSQLite(t)
			require.NoError(t, db.AutoMigrate(&ProviderReversalEffect{}))
			require.NoError(t, db.Migrator().DropIndex(&ProviderReversalEffect{}, test.indexName))
			require.NoError(t, db.Exec(test.definition).Error)

			err := ensureProviderReversalEffectTable(db, common.DatabaseTypeSQLite)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unsafe definition")
		})
	}
}

func createLegacyProviderRefundTable(t *testing.T, db *gorm.DB, eventKeyDDL string) {
	t.Helper()
	require.NoError(t, db.Exec(`CREATE TABLE provider_refund_events (
		id integer primary key,
		event_key `+eventKeyDDL+`,
		provider varchar(50) NOT NULL,
		delivery_id varchar(255) NOT NULL,
		event_type varchar(64) NOT NULL,
		status varchar(32) NOT NULL
	)`).Error)
}

func TestPaymentSchemaExpandSQLiteRejectsUnsafeLegacyProviderRefundIdentity(t *testing.T) {
	t.Run("nullable event key", func(t *testing.T) {
		db := newPaymentMigrationSQLite(t)
		createLegacyProviderRefundTable(t, db, "char(64)")
		require.NoError(t, db.Exec(`CREATE UNIQUE INDEX idx_provider_refund_event_key
			ON provider_refund_events(event_key)`).Error)

		err := MigratePaymentSchemaExpand(db, common.DatabaseTypeSQLite)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "provider_refund_events.event_key is nullable")
	})

	t.Run("duplicate event key", func(t *testing.T) {
		db := newPaymentMigrationSQLite(t)
		createLegacyProviderRefundTable(t, db, "char(64) NOT NULL")
		key := ProviderRefundEventKey("stripe", "duplicate-delivery")
		require.NoError(t, db.Exec(`INSERT INTO provider_refund_events
			(event_key, provider, delivery_id, event_type, status) VALUES
			(?, 'stripe', 'delivery-a', 'refund.succeeded', 'processing'),
			(?, 'stripe', 'delivery-b', 'refund.succeeded', 'processing')`, key, key).Error)

		err := MigratePaymentSchemaExpand(db, common.DatabaseTypeSQLite)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate event key")
	})

	t.Run("same-name non-unique index", func(t *testing.T) {
		db := newPaymentMigrationSQLite(t)
		createLegacyProviderRefundTable(t, db, "char(64) NOT NULL")
		require.NoError(t, db.Exec(`CREATE INDEX idx_provider_refund_event_key
			ON provider_refund_events(event_key)`).Error)

		err := MigratePaymentSchemaExpand(db, common.DatabaseTypeSQLite)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "idx_provider_refund_event_key has unsafe definition")
	})

	t.Run("partial unique index", func(t *testing.T) {
		db := newPaymentMigrationSQLite(t)
		createLegacyProviderRefundTable(t, db, "char(64) NOT NULL")
		require.NoError(t, db.Exec(`CREATE UNIQUE INDEX idx_provider_refund_event_key
			ON provider_refund_events(event_key) WHERE status <> 'processing'`).Error)

		err := MigratePaymentSchemaExpand(db, common.DatabaseTypeSQLite)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "idx_provider_refund_event_key has unsafe definition")
	})
}

func TestPaymentSchemaExpandSQLiteCreatesMissingLegacyProviderRefundFence(t *testing.T) {
	db := newPaymentMigrationSQLite(t)
	createLegacyProviderRefundTable(t, db, "char(64) NOT NULL")

	require.NoError(t, MigratePaymentSchemaExpand(db, common.DatabaseTypeSQLite))
	definition, err := paymentIndexDefinition(db, common.DatabaseTypeSQLite, "provider_refund_events", providerRefundEventKeyIndexName)
	require.NoError(t, err)
	assert.True(t, paymentIndexDefinitionMatches(
		definition, "provider_refund_events", providerRefundEventKeyIndexName, []string{"event_key"}, true,
	))

	key := ProviderRefundEventKey("stripe", "delivery-once")
	require.NoError(t, db.Exec(`INSERT INTO provider_refund_events
		(event_key, provider, delivery_id, event_type, status) VALUES
		(?, 'stripe', 'delivery-a', 'refund.succeeded', 'processing')`, key).Error)
	assert.Error(t, db.Exec(`INSERT INTO provider_refund_events
		(event_key, provider, delivery_id, event_type, status) VALUES
		(?, 'stripe', 'delivery-b', 'refund.succeeded', 'processing')`, key).Error)
}

func TestPaymentIndexDefinitionMatchesRejectsUnsafePhysicalMetadata(t *testing.T) {
	expectedColumns := []string{"provider", "provider_account_id", "provider_environment", "order_trade_no", "order_kind"}
	safe := paymentPhysicalIndex{
		Exists: true, Table: "provider_reversal_effects", Name: providerReversalOrderIndexName,
		Columns: expectedColumns, Valid: true, Ready: true, Live: true,
	}
	assert.True(t, paymentIndexDefinitionMatches(
		safe, "provider_reversal_effects", providerReversalOrderIndexName, expectedColumns, false,
	))
	tests := []struct {
		name  string
		index paymentPhysicalIndex
	}{
		{name: "SQLite partial index", index: func() paymentPhysicalIndex { value := safe; value.Partial = true; return value }()},
		{name: "MySQL prefix index", index: func() paymentPhysicalIndex { value := safe; value.Prefix = true; return value }()},
		{name: "PostgreSQL expression index", index: func() paymentPhysicalIndex { value := safe; value.Expression = true; return value }()},
		{name: "PostgreSQL invalid index", index: func() paymentPhysicalIndex { value := safe; value.Valid = false; return value }()},
		{name: "PostgreSQL unready index", index: func() paymentPhysicalIndex { value := safe; value.Ready = false; return value }()},
		{name: "PostgreSQL dead index", index: func() paymentPhysicalIndex { value := safe; value.Live = false; return value }()},
		{name: "PostgreSQL NULLS NOT DISTINCT", index: func() paymentPhysicalIndex { value := safe; value.NullsNotDistinct = true; return value }()},
		{name: "unsupported access method", index: func() paymentPhysicalIndex { value := safe; value.UnsupportedMethod = true; return value }()},
		{name: "wrong owner table", index: func() paymentPhysicalIndex { value := safe; value.Table = "other_table"; return value }()},
		{name: "reordered columns", index: func() paymentPhysicalIndex {
			value := safe
			value.Columns = []string{"provider", "provider_account_id", "provider_environment", "order_kind", "order_trade_no"}
			return value
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.False(t, paymentIndexDefinitionMatches(
				test.index, "provider_reversal_effects", providerReversalOrderIndexName, expectedColumns, false,
			))
		})
	}
}

func TestPaymentIndexDefinitionConfiguredDatabases(t *testing.T) {
	t.Run("mysql prefix index", func(t *testing.T) {
		dsn := strings.TrimSpace(os.Getenv("TEST_MYSQL_DSN"))
		if dsn == "" {
			t.Skip("TEST_MYSQL_DSN is not configured")
		}
		db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = sqlDB.Close() })

		suffix := fmt.Sprintf("%x", time.Now().UnixNano())
		tableName := "payment_index_probe_" + suffix
		indexName := "idx_payment_probe_" + suffix
		quotedTable := quotePaymentIdentifier(tableName, common.DatabaseTypeMySQL)
		quotedIndex := quotePaymentIdentifier(indexName, common.DatabaseTypeMySQL)
		require.NoError(t, db.Exec(fmt.Sprintf("CREATE TABLE %s (effect_key CHAR(64) NOT NULL)", quotedTable)).Error)
		t.Cleanup(func() { _ = db.Exec("DROP TABLE IF EXISTS " + quotedTable).Error })
		require.NoError(t, db.Exec(fmt.Sprintf("CREATE UNIQUE INDEX %s ON %s (`effect_key`(8))", quotedIndex, quotedTable)).Error)

		definition, err := paymentIndexDefinition(db, common.DatabaseTypeMySQL, tableName, indexName)
		require.NoError(t, err)
		assert.True(t, definition.Exists)
		assert.True(t, definition.Unique)
		assert.True(t, definition.Prefix)
		assert.False(t, paymentIndexDefinitionMatches(definition, tableName, indexName, []string{"effect_key"}, true))
	})

	t.Run("postgres partial and reordered indexes", func(t *testing.T) {
		dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
		if dsn == "" {
			t.Skip("TEST_POSTGRES_DSN is not configured")
		}
		db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { _ = sqlDB.Close() })

		suffix := fmt.Sprintf("%x", time.Now().UnixNano())
		tableName := "payment_index_probe_" + suffix
		indexName := "idx_payment_probe_" + suffix
		quotedTable := quotePaymentIdentifier(tableName, common.DatabaseTypePostgreSQL)
		quotedIndex := quotePaymentIdentifier(indexName, common.DatabaseTypePostgreSQL)
		require.NoError(t, db.Exec(fmt.Sprintf(`CREATE TABLE %s (
			effect_key CHAR(64) NOT NULL, provider VARCHAR(50) NOT NULL,
			provider_account_id VARCHAR(255) NOT NULL, provider_environment VARCHAR(32) NOT NULL,
			order_trade_no VARCHAR(255) NOT NULL, order_kind VARCHAR(32) NOT NULL,
			status VARCHAR(32) NOT NULL)`, quotedTable)).Error)
		t.Cleanup(func() { _ = db.Exec("DROP TABLE IF EXISTS " + quotedTable).Error })
		require.NoError(t, db.Exec(fmt.Sprintf(
			"CREATE UNIQUE INDEX %s ON %s (effect_key) WHERE status <> 'processing'", quotedIndex, quotedTable,
		)).Error)

		definition, err := paymentIndexDefinition(db, common.DatabaseTypePostgreSQL, tableName, indexName)
		require.NoError(t, err)
		assert.True(t, definition.Exists)
		assert.True(t, definition.Unique)
		assert.True(t, definition.Partial)
		assert.False(t, paymentIndexDefinitionMatches(definition, tableName, indexName, []string{"effect_key"}, true))

		require.NoError(t, db.Exec("DROP INDEX "+quotedIndex).Error)
		require.NoError(t, db.Exec(fmt.Sprintf(`CREATE INDEX %s ON %s
			(provider, provider_account_id, provider_environment, order_kind, order_trade_no)`, quotedIndex, quotedTable)).Error)
		definition, err = paymentIndexDefinition(db, common.DatabaseTypePostgreSQL, tableName, indexName)
		require.NoError(t, err)
		assert.Equal(t,
			[]string{"provider", "provider_account_id", "provider_environment", "order_kind", "order_trade_no"},
			definition.Columns,
		)
		assert.False(t, paymentIndexDefinitionMatches(definition, tableName, indexName,
			[]string{"provider", "provider_account_id", "provider_environment", "order_trade_no", "order_kind"}, false))
	})
}

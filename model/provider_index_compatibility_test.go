package model

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func newProviderIndexCompatibilityDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	return db
}

func TestProviderIndexesFitLegacyMySQLUtf8mb4KeyLimit(t *testing.T) {
	db := newProviderIndexCompatibilityDB(t)
	models := []struct {
		name  string
		model any
	}{
		{name: "payment event", model: &PaymentEvent{}},
		{name: "payment binding", model: &ProviderPaymentBinding{}},
		{name: "refund delivery", model: &ProviderRefundEvent{}},
		{name: "reversal effect", model: &ProviderReversalEffect{}},
	}

	for _, test := range models {
		t.Run(test.name, func(t *testing.T) {
			statement := &gorm.Statement{DB: db}
			require.NoError(t, statement.Parse(test.model))
			for _, index := range statement.Schema.ParseIndexes() {
				totalCharacters := 0
				for _, field := range index.Fields {
					if field.Field.DataType != schema.String {
						continue
					}
					characters := field.Length
					if characters == 0 {
						characters = field.Field.Size
					}
					require.Positive(t, characters, "%s.%s must have a bounded MySQL index width", index.Name, field.DBName)
					totalCharacters += characters
				}
				if totalCharacters > 0 {
					assert.LessOrEqual(t, totalCharacters*4, 767,
						"%s exceeds the COMPACT/DYNAMIC-disabled InnoDB utf8mb4 key limit", index.Name)
				}
			}
		})
	}

	queryIndexes := []struct {
		model any
		name  string
	}{
		{model: &TopUp{}, name: "idx_top_ups_provider_trade_no"},
		{model: &SubscriptionOrder{}, name: "idx_subscription_orders_provider_trade_no"},
		{model: &UserSubscription{}, name: "idx_user_subscriptions_provider_subscription_id"},
		{model: &UserSubscription{}, name: "idx_user_subscriptions_subscription_order_trade_no"},
	}
	for _, test := range queryIndexes {
		statement := &gorm.Statement{DB: db}
		require.NoError(t, statement.Parse(test.model))
		index := statement.Schema.LookIndex(test.name)
		require.NotNil(t, index, test.name)
		require.Len(t, index.Fields, 1, test.name)
		assert.Equal(t, 191, index.Fields[0].Length, test.name)
	}

	statement := &gorm.Statement{DB: db}
	require.NoError(t, statement.Parse(&ProviderReversalEffect{}))
	orderIndex := statement.Schema.LookIndex(providerReversalOrderIndexName)
	require.NotNil(t, orderIndex)
	require.Len(t, orderIndex.Fields, len(providerReversalOrderMySQLPrefixLengths))
	actualPrefixes := make([]int64, 0, len(orderIndex.Fields))
	for _, field := range orderIndex.Fields {
		actualPrefixes = append(actualPrefixes, int64(field.Length))
	}
	assert.Equal(t, providerReversalOrderMySQLPrefixLengths, actualPrefixes)
}

func TestProviderReversalMySQLPrefixDefinitionMustMatchExactly(t *testing.T) {
	expectedColumns := []string{"provider", "provider_account_id", "provider_environment", "order_trade_no", "order_kind"}
	index := paymentPhysicalIndex{
		Exists: true, Table: "provider_reversal_effects", Name: providerReversalOrderIndexName,
		Columns: expectedColumns, Prefix: true,
		PrefixLengths: append([]int64(nil), providerReversalOrderMySQLPrefixLengths...),
		Valid:         true, Ready: true, Live: true,
	}
	assert.True(t, paymentIndexDefinitionMatchesWithPrefixes(
		index, "provider_reversal_effects", providerReversalOrderIndexName, expectedColumns, false,
		providerReversalOrderMySQLPrefixLengths,
	))

	index.PrefixLengths[3]--
	assert.False(t, paymentIndexDefinitionMatchesWithPrefixes(
		index, "provider_reversal_effects", providerReversalOrderIndexName, expectedColumns, false,
		providerReversalOrderMySQLPrefixLengths,
	))
}

func TestProviderMySQLLengthHintsPreserveSQLiteFullColumnIndexes(t *testing.T) {
	db := newProviderIndexCompatibilityDB(t)
	require.NoError(t, db.AutoMigrate(
		&PaymentEvent{},
		&TopUp{},
		&SubscriptionOrder{},
		&UserSubscription{},
		&ProviderPaymentBinding{},
		&ProviderRefundEvent{},
		&ProviderReversalEffect{},
	))

	tests := []struct {
		table   string
		name    string
		columns []string
		unique  bool
	}{
		{table: "provider_payment_bindings", name: "idx_provider_payment_binding_order", columns: []string{"order_trade_no", "order_kind"}},
		{table: "payment_events", name: "idx_payment_events_order_trade_no", columns: []string{"order_trade_no"}},
		{table: "top_ups", name: "idx_top_ups_provider_trade_no", columns: []string{"provider_trade_no"}},
		{table: "subscription_orders", name: "idx_subscription_orders_provider_trade_no", columns: []string{"provider_trade_no"}},
		{table: "user_subscriptions", name: "idx_user_subscriptions_provider_subscription_id", columns: []string{"provider_subscription_id"}},
		{table: "user_subscriptions", name: "idx_user_subscriptions_subscription_order_trade_no", columns: []string{"subscription_order_trade_no"}},
		{table: "provider_refund_events", name: "idx_provider_refund_events_provider_account_id", columns: []string{"provider_account_id"}},
		{table: "provider_refund_events", name: "idx_provider_refund_events_delivery_id", columns: []string{"delivery_id"}},
		{table: "provider_refund_events", name: "idx_provider_refund_events_effect_id", columns: []string{"effect_id"}},
		{table: "provider_refund_events", name: "idx_provider_refund_events_provider_trade_no", columns: []string{"provider_trade_no"}},
		{table: "provider_refund_events", name: "idx_provider_refund_events_order_trade_no", columns: []string{"order_trade_no"}},
		{table: "provider_reversal_effects", name: providerReversalOrderIndexName, columns: []string{"provider", "provider_account_id", "provider_environment", "order_trade_no", "order_kind"}},
	}
	for _, test := range tests {
		definition, err := paymentIndexDefinition(db, common.DatabaseTypeSQLite, test.table, test.name)
		require.NoError(t, err)
		assert.True(t, paymentIndexDefinitionMatches(definition, test.table, test.name, test.columns, test.unique), test.name)
	}
}

func TestPaymentSchemaExpandSQLiteConcurrentStartup(t *testing.T) {
	const instances = 8
	dsn := fmt.Sprintf("file:%s?cache=shared&_pragma=busy_timeout(5000)",
		filepath.Join(t.TempDir(), "payment-migration.db"))
	databases := make([]*gorm.DB, 0, instances)
	for i := 0; i < instances; i++ {
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
		databases = append(databases, db)
	}

	start := make(chan struct{})
	results := make(chan error, instances)
	for _, db := range databases {
		go func(db *gorm.DB) {
			<-start
			results <- MigratePaymentSchemaExpand(db, common.DatabaseTypeSQLite)
		}(db)
	}
	close(start)
	errs := make([]error, 0, instances)
	for i := 0; i < instances; i++ {
		errs = append(errs, <-results)
	}
	for _, err := range errs {
		require.NoError(t, err)
	}

	require.True(t, databases[0].Migrator().HasTable(&ProviderReversalEffect{}))
	for _, indexName := range []string{
		providerReversalEffectKeyIndexName,
		providerReversalRelatedEffectIndexName,
		providerReversalOrderIndexName,
	} {
		assert.True(t, databases[0].Migrator().HasIndex(&ProviderReversalEffect{}, indexName), indexName)
	}
}

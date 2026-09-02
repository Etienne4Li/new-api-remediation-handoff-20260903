package model

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const paymentEventKeyIndexName = "idx_payment_event_key"

const (
	providerRefundEventKeyIndexName        = "idx_provider_refund_event_key"
	providerReversalEffectKeyIndexName     = "idx_provider_reversal_effect_key"
	providerReversalRelatedEffectIndexName = "idx_provider_reversal_related_effect"
	providerReversalOrderIndexName         = "idx_provider_reversal_order"
)

var providerReversalOrderMySQLPrefixLengths = []int64{12, 48, 12, 96, 16}

const (
	paymentSchemaMigrationMySQLLockName = "newapi:payment-schema-expand:v1"
	paymentSchemaMigrationPostgresLock  = int64(0x4e41504950313034)
)

type paymentSchemaColumn struct {
	// Name is the Go field name used by GORM's schema lookup. Keeping it as a
	// field name (rather than interpolating a caller supplied SQL identifier)
	// lets the migration validate that every definition is present in the
	// corresponding model.
	Name string
	// DBName is the physical column name used in the explicit ALTER TABLE DDL.
	DBName string
	// DDL is intentionally dialect-specific. Add-column syntax is common
	// across our supported databases, but the type spelling/default handling
	// differs enough that relying on a future AutoMigrate rewrite is unsafe for
	// an existing production table.
	DDL      map[common.DatabaseType]string
	Required bool
}

type paymentSchemaTable struct {
	Name    string
	Model   any
	Columns []paymentSchemaColumn
	Indexes []string
}

var paymentSchemaTables = []paymentSchemaTable{
	{
		Name:  "top_ups",
		Model: &TopUp{},
		Indexes: []string{
			"idx_top_ups_provider_trade_no",
			"idx_top_ups_settlement_resolution",
		},
		Columns: []paymentSchemaColumn{
			{Name: "SettlementResolution", DBName: "settlement_resolution", DDL: paymentDDL("VARCHAR(32) NOT NULL DEFAULT ''")},
			{Name: "SettlementErrorCode", DBName: "settlement_error_code", DDL: paymentDDL("VARCHAR(64) NOT NULL DEFAULT ''")},
			{Name: "CreditedQuota", DBName: "credited_quota", DDL: paymentDDL("BIGINT DEFAULT 0")},
			{Name: "ProviderTradeNo", DBName: "provider_trade_no", DDL: paymentDDL("VARCHAR(255)")},
			{Name: "ProviderMerchantID", DBName: "provider_merchant_id", DDL: paymentDDL("VARCHAR(255) NOT NULL DEFAULT ''")},
			{Name: "ProviderOrderName", DBName: "provider_order_name", DDL: paymentDDL("VARCHAR(255) NOT NULL DEFAULT ''")},
			{Name: "ProviderAmount", DBName: "provider_amount", DDL: paymentDDL("VARCHAR(64) NOT NULL DEFAULT ''")},
			{Name: "ProviderProductID", DBName: "provider_product_id", DDL: paymentDDL("VARCHAR(255) NOT NULL DEFAULT ''")},
			{Name: "ProviderStoreID", DBName: "provider_store_id", DDL: paymentDDL("VARCHAR(255) NOT NULL DEFAULT ''")},
			{Name: "ProviderCheckoutID", DBName: "provider_checkout_id", DDL: paymentDDL("VARCHAR(255) NOT NULL DEFAULT ''")},
			{Name: "ProviderSubscriptionID", DBName: "provider_subscription_id", DDL: paymentDDL("VARCHAR(255) NOT NULL DEFAULT ''")},
			{Name: "ProviderCurrency", DBName: "provider_currency", DDL: paymentDDL("VARCHAR(16) NOT NULL DEFAULT ''")},
			{Name: "ProviderEventID", DBName: "provider_event_id", DDL: paymentDDL("VARCHAR(255) NOT NULL DEFAULT ''")},
			{Name: "ProviderKeyFingerprint", DBName: "provider_key_fingerprint", DDL: paymentDDL("VARCHAR(64) NOT NULL DEFAULT ''")},
			{Name: "ProviderPayload", DBName: "provider_payload", DDL: paymentDDL("TEXT")},
		},
	},
	{
		Name:  "subscription_orders",
		Model: &SubscriptionOrder{},
		Indexes: []string{
			"idx_subscription_orders_provider_trade_no",
			"idx_subscription_orders_settlement_resolution",
		},
		Columns: []paymentSchemaColumn{
			{Name: "SettlementResolution", DBName: "settlement_resolution", DDL: paymentDDL("VARCHAR(32) NOT NULL DEFAULT ''")},
			{Name: "SettlementErrorCode", DBName: "settlement_error_code", DDL: paymentDDL("VARCHAR(64) NOT NULL DEFAULT ''")},
			{Name: "ProviderTradeNo", DBName: "provider_trade_no", DDL: paymentDDL("VARCHAR(255)")},
			{Name: "ProviderMerchantID", DBName: "provider_merchant_id", DDL: paymentDDL("VARCHAR(255) NOT NULL DEFAULT ''")},
			{Name: "ProviderOrderName", DBName: "provider_order_name", DDL: paymentDDL("VARCHAR(255) NOT NULL DEFAULT ''")},
			{Name: "ProviderAmount", DBName: "provider_amount", DDL: paymentDDL("VARCHAR(64) NOT NULL DEFAULT ''")},
			{Name: "ProviderProductID", DBName: "provider_product_id", DDL: paymentDDL("VARCHAR(255) NOT NULL DEFAULT ''")},
			{Name: "ProviderStoreID", DBName: "provider_store_id", DDL: paymentDDL("VARCHAR(255) NOT NULL DEFAULT ''")},
			{Name: "ProviderCheckoutID", DBName: "provider_checkout_id", DDL: paymentDDL("VARCHAR(255) NOT NULL DEFAULT ''")},
			{Name: "ProviderSubscriptionID", DBName: "provider_subscription_id", DDL: paymentDDL("VARCHAR(255) NOT NULL DEFAULT ''")},
			{Name: "ProviderCurrency", DBName: "provider_currency", DDL: paymentDDL("VARCHAR(16) NOT NULL DEFAULT ''")},
			{Name: "ProviderEventID", DBName: "provider_event_id", DDL: paymentDDL("VARCHAR(255) NOT NULL DEFAULT ''")},
			{Name: "ProviderKeyFingerprint", DBName: "provider_key_fingerprint", DDL: paymentDDL("VARCHAR(64) NOT NULL DEFAULT ''")},
			{Name: "ProviderPayload", DBName: "provider_payload", DDL: paymentDDL("TEXT")},
			{Name: "EntitlementSnapshot", DBName: "entitlement_snapshot", DDL: paymentDDL("TEXT")},
		},
	},
	{
		// Recurring lifecycle identity is needed by webhook handlers before the
		// broad startup AutoMigrate finishes. Keep these additive columns in the
		// early payment-schema pass for existing installations.
		Name:  "user_subscriptions",
		Model: &UserSubscription{},
		Columns: []paymentSchemaColumn{
			{Name: "SubscriptionOrderTradeNo", DBName: "subscription_order_trade_no", DDL: paymentDDL("VARCHAR(255) NOT NULL DEFAULT ''")},
			{Name: "QuotaResetPeriod", DBName: "quota_reset_period", DDL: paymentDDL("VARCHAR(16) NOT NULL DEFAULT ''")},
			{Name: "QuotaResetCustomSeconds", DBName: "quota_reset_custom_seconds", DDL: paymentDDL("BIGINT NOT NULL DEFAULT 0")},
			{Name: "ProviderSubscriptionProvider", DBName: "provider_subscription_provider", DDL: paymentDDL("VARCHAR(50) NOT NULL DEFAULT ''")},
			{Name: "ProviderLifecycleEventTime", DBName: "provider_lifecycle_event_time", DDL: paymentDDL("BIGINT NOT NULL DEFAULT 0")},
		},
	},
	{
		Name:  "provider_refund_events",
		Model: &ProviderRefundEvent{},
		Columns: []paymentSchemaColumn{
			{Name: "ProviderAccountID", DBName: "provider_account_id", DDL: paymentDDL("VARCHAR(255) NOT NULL DEFAULT ''")},
			{Name: "ProviderEnvironment", DBName: "provider_environment", DDL: paymentDDL("VARCHAR(32) NOT NULL DEFAULT ''")},
			{Name: "EffectKey", DBName: "effect_key", DDL: paymentDDL("CHAR(64) NOT NULL DEFAULT ''")},
			{Name: "EffectID", DBName: "effect_id", DDL: paymentDDL("VARCHAR(255) NOT NULL DEFAULT ''")},
			{Name: "RelatedEffectID", DBName: "related_effect_id", DDL: paymentDDL("VARCHAR(255) NOT NULL DEFAULT ''")},
			{Name: "ProviderObjectType", DBName: "provider_object_type", DDL: paymentDDL("VARCHAR(32) NOT NULL DEFAULT ''")},
			{Name: "ProviderTradeNo", DBName: "provider_trade_no", DDL: paymentDDL("VARCHAR(255) NOT NULL DEFAULT ''")},
			{Name: "Amount", DBName: "amount", DDL: paymentDDL("VARCHAR(64) NOT NULL DEFAULT ''")},
			{Name: "Currency", DBName: "currency", DDL: paymentDDL("VARCHAR(16) NOT NULL DEFAULT ''")},
			{Name: "WalletDelta", DBName: "wallet_delta", DDL: paymentDDL("BIGINT NOT NULL DEFAULT 0")},
			{Name: "DecisionReason", DBName: "decision_reason", DDL: paymentDDL("VARCHAR(64) NOT NULL DEFAULT ''")},
		},
	},
}

// paymentDDL returns the same conservative SQL type for all three supported
// engines. SQLite treats these declarations as affinities, while MySQL and
// PostgreSQL preserve the intended width/text semantics. Keeping the map
// explicit makes unsupported database types fail before any DDL is attempted.
func paymentDDL(ddl string) map[common.DatabaseType]string {
	return map[common.DatabaseType]string{
		common.DatabaseTypeSQLite:     ddl,
		common.DatabaseTypeMySQL:      ddl,
		common.DatabaseTypePostgreSQL: ddl,
	}
}

// migratePaymentSchemaExpand applies only additive payment-schema changes.
// It is intentionally run before the broad model AutoMigrate: callback code
// may execute as soon as the process starts, and old installations can have
// large top_ups/subscription_orders tables where an implicit table rewrite is
// an unacceptable startup surprise. The operation is idempotent and can be
// safely retried after a partially completed deployment.
func migratePaymentSchemaExpand() error {
	if DB == nil {
		return errors.New("database is not initialized")
	}
	return migratePaymentSchemaExpandDB(DB, paymentMigrationDatabaseType(DB, common.MainDatabaseType()))
}

// MigratePaymentSchemaExpand is an explicit migration entry point for
// operators and migration tests. It performs no backfill or settlement: rows
// whose required payment-event identity is ambiguous are rejected and must be
// reconciled before the unique idempotency fence is installed.
func MigratePaymentSchemaExpand(db *gorm.DB, dbType common.DatabaseType) error {
	return migratePaymentSchemaExpandDB(db, paymentMigrationDatabaseType(db, dbType))
}

func migratePaymentSchemaExpandDB(db *gorm.DB, dbType common.DatabaseType) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	if dbType != common.DatabaseTypeSQLite && dbType != common.DatabaseTypeMySQL && dbType != common.DatabaseTypePostgreSQL {
		return fmt.Errorf("payment schema migration does not support database type %q", dbType)
	}
	return withPaymentSchemaMigrationLock(db, dbType, func(lockedDB *gorm.DB) error {
		return migratePaymentSchemaExpandLocked(lockedDB, dbType)
	})
}

func migratePaymentSchemaExpandLocked(db *gorm.DB, dbType common.DatabaseType) error {
	for _, table := range paymentSchemaTables {
		if !db.Migrator().HasTable(table.Model) {
			// These are baseline tables on a fresh installation. The regular
			// AutoMigrate below creates them; this expand pass must not create a
			// partial table without its historical columns/indexes.
			continue
		}
		if err := addPaymentSchemaColumns(db, dbType, table); err != nil {
			return err
		}
		if err := ensurePaymentOrderIndexes(db, table); err != nil {
			return err
		}
	}
	if db.Migrator().HasTable(&ProviderRefundEvent{}) {
		if err := ensureProviderRefundEventIdentity(db, dbType); err != nil {
			return err
		}
	}

	if err := ensurePaymentEventTable(db); err != nil {
		return err
	}
	if err := addPaymentEventColumns(db, dbType); err != nil {
		return err
	}
	if err := validatePaymentEventRequiredColumns(db, dbType); err != nil {
		return err
	}
	if err := ensurePaymentEventKeyIdentity(db, dbType); err != nil {
		return err
	}
	return ensureProviderReversalEffectTable(db, dbType)
}

func withPaymentSchemaMigrationLock(db *gorm.DB, dbType common.DatabaseType, migrate func(*gorm.DB) error) error {
	return db.Connection(func(connection *gorm.DB) (err error) {
		switch dbType {
		case common.DatabaseTypeSQLite:
			if err = connection.Exec("BEGIN IMMEDIATE").Error; err != nil {
				return fmt.Errorf("acquire SQLite payment schema migration lock: %w", err)
			}
			committed := false
			defer func() {
				if !committed {
					_ = connection.Exec("ROLLBACK").Error
				}
			}()
			// BEGIN IMMEDIATE already owns the transaction on this dedicated
			// connection. Disable GORM's per-write default transaction so a
			// backfill UPDATE cannot try to nest BEGIN inside it.
			if err = migrate(connection.Session(&gorm.Session{NewDB: true, SkipDefaultTransaction: true})); err != nil {
				return err
			}
			if err = connection.Exec("COMMIT").Error; err != nil {
				return fmt.Errorf("commit SQLite payment schema migration: %w", err)
			}
			committed = true
			return nil

		case common.DatabaseTypeMySQL:
			var acquired sql.NullInt64
			if err = connection.Raw("SELECT GET_LOCK(?, 60) AS acquired", paymentSchemaMigrationMySQLLockName).Scan(&acquired).Error; err != nil {
				return fmt.Errorf("acquire MySQL payment schema migration lock: %w", err)
			}
			if !acquired.Valid || acquired.Int64 != 1 {
				return errors.New("timed out acquiring MySQL payment schema migration lock")
			}
			defer func() {
				var released sql.NullInt64
				releaseErr := connection.Raw("SELECT RELEASE_LOCK(?) AS released", paymentSchemaMigrationMySQLLockName).Scan(&released).Error
				if releaseErr != nil {
					err = errors.Join(err, fmt.Errorf("release MySQL payment schema migration lock: %w", releaseErr))
				} else if !released.Valid || released.Int64 != 1 {
					err = errors.Join(err, errors.New("MySQL payment schema migration lock was not released"))
				}
			}()
			return migrate(connection.Session(&gorm.Session{NewDB: true}))

		case common.DatabaseTypePostgreSQL:
			if err = connection.Exec("SELECT pg_advisory_lock(?)", paymentSchemaMigrationPostgresLock).Error; err != nil {
				return fmt.Errorf("acquire PostgreSQL payment schema migration lock: %w", err)
			}
			defer func() {
				var released bool
				releaseErr := connection.Raw("SELECT pg_advisory_unlock(?) AS released", paymentSchemaMigrationPostgresLock).Scan(&released).Error
				if releaseErr != nil {
					err = errors.Join(err, fmt.Errorf("release PostgreSQL payment schema migration lock: %w", releaseErr))
				} else if !released {
					err = errors.Join(err, errors.New("PostgreSQL payment schema migration lock was not released"))
				}
			}()
			return migrate(connection.Session(&gorm.Session{NewDB: true}))
		default:
			return fmt.Errorf("payment schema migration does not support database type %q", dbType)
		}
	})
}

func ensureProviderReversalEffectTable(db *gorm.DB, dbType common.DatabaseType) error {
	expected := []struct {
		name          string
		cols          []string
		unique        bool
		prefixLengths []int64
	}{
		{name: providerReversalEffectKeyIndexName, cols: []string{"effect_key"}, unique: true},
		{name: providerReversalRelatedEffectIndexName, cols: []string{"related_effect_key"}, unique: true},
		{name: providerReversalOrderIndexName, cols: []string{"provider", "provider_account_id", "provider_environment", "order_trade_no", "order_kind"}, unique: false},
	}
	if dbType == common.DatabaseTypeMySQL {
		expected[2].prefixLengths = providerReversalOrderMySQLPrefixLengths
	}

	// Inspect same-name indexes before AutoMigrate. GORM checks only the index
	// name before deciding that an index already exists, and some dialect
	// migrators then fail with an opaque DDL error while comparing a partial or
	// expression index. Reject the physical mismatch explicitly first.
	if db.Migrator().HasTable(&ProviderReversalEffect{}) {
		for _, want := range expected {
			definition, err := paymentIndexDefinition(db, dbType, "provider_reversal_effects", want.name)
			if err != nil {
				return fmt.Errorf("inspect provider reversal index %s: %w", want.name, err)
			}
			if definition.Exists && !paymentIndexDefinitionMatchesWithPrefixes(definition, "provider_reversal_effects", want.name, want.cols, want.unique, want.prefixLengths) {
				return fmt.Errorf("provider_reversal_effects index %s has unsafe definition", want.name)
			}
		}
	}

	if err := db.AutoMigrate(&ProviderReversalEffect{}); err != nil {
		return fmt.Errorf("migrate provider_reversal_effects: %w", err)
	}
	for _, want := range expected {
		definition, err := paymentIndexDefinition(db, dbType, "provider_reversal_effects", want.name)
		if err != nil {
			return fmt.Errorf("inspect provider reversal index %s: %w", want.name, err)
		}
		if definition.Exists {
			if !paymentIndexDefinitionMatchesWithPrefixes(definition, "provider_reversal_effects", want.name, want.cols, want.unique, want.prefixLengths) {
				return fmt.Errorf("provider_reversal_effects index %s has unsafe definition", want.name)
			}
			continue
		}
		if err := db.Migrator().CreateIndex(&ProviderReversalEffect{}, want.name); err != nil {
			definitionAfterError, inspectErr := paymentIndexDefinition(db, dbType, "provider_reversal_effects", want.name)
			if inspectErr == nil && paymentIndexDefinitionMatchesWithPrefixes(definitionAfterError, "provider_reversal_effects", want.name, want.cols, want.unique, want.prefixLengths) {
				continue
			}
			return fmt.Errorf("create provider_reversal_effects index %s: %w", want.name, err)
		}
		definition, err = paymentIndexDefinition(db, dbType, "provider_reversal_effects", want.name)
		if err != nil || !paymentIndexDefinitionMatchesWithPrefixes(definition, "provider_reversal_effects", want.name, want.cols, want.unique, want.prefixLengths) {
			return fmt.Errorf("provider_reversal_effects index %s was created with unsafe definition", want.name)
		}
	}
	return nil
}

type paymentPhysicalIndex struct {
	Exists            bool
	Table             string
	Name              string
	Columns           []string
	Unique            bool
	Partial           bool
	Expression        bool
	Prefix            bool
	PrefixLengths     []int64
	Valid             bool
	Ready             bool
	Live              bool
	NullsNotDistinct  bool
	UnsupportedMethod bool
}

func paymentIndexDefinitionMatches(index paymentPhysicalIndex, table, name string, columns []string, unique bool) bool {
	return paymentIndexDefinitionMatchesWithPrefixes(index, table, name, columns, unique, nil)
}

func paymentIndexDefinitionMatchesWithPrefixes(index paymentPhysicalIndex, table, name string, columns []string, unique bool, prefixLengths []int64) bool {
	return index.Exists && strings.EqualFold(strings.TrimSpace(index.Table), table) &&
		strings.EqualFold(strings.TrimSpace(index.Name), name) && index.Unique == unique &&
		!index.Partial && !index.Expression && paymentIndexPrefixLengthsMatch(index, prefixLengths) && index.Valid && index.Ready && index.Live &&
		!index.NullsNotDistinct && !index.UnsupportedMethod && equalPaymentIndexColumns(index.Columns, columns)
}

func paymentIndexPrefixLengthsMatch(index paymentPhysicalIndex, expected []int64) bool {
	if len(expected) == 0 {
		return !index.Prefix
	}
	if len(index.PrefixLengths) != len(expected) {
		return false
	}
	for position := range expected {
		if expected[position] <= 0 || index.PrefixLengths[position] != expected[position] {
			return false
		}
	}
	return index.Prefix
}

func paymentIndexDefinition(db *gorm.DB, dbType common.DatabaseType, tableName, indexName string) (paymentPhysicalIndex, error) {
	switch dbType {
	case common.DatabaseTypeSQLite:
		return sqlitePaymentIndexDefinition(db, tableName, indexName)
	case common.DatabaseTypeMySQL:
		return mysqlPaymentIndexDefinition(db, tableName, indexName)
	case common.DatabaseTypePostgreSQL:
		return postgresPaymentIndexDefinition(db, tableName, indexName)
	default:
		return paymentPhysicalIndex{}, fmt.Errorf("unsupported payment index database type %q", dbType)
	}
}

func sqlitePaymentIndexDefinition(db *gorm.DB, tableName, indexName string) (paymentPhysicalIndex, error) {
	index := paymentPhysicalIndex{Name: indexName, Valid: true, Ready: true, Live: true}
	var definition struct {
		TableName string `gorm:"column:tbl_name"`
		SQL       string `gorm:"column:sql"`
	}
	result := db.Raw("SELECT tbl_name, sql FROM sqlite_master WHERE type = 'index' AND name = ?", indexName).Scan(&definition)
	if result.Error != nil {
		return paymentPhysicalIndex{}, result.Error
	}
	if result.RowsAffected == 0 {
		return index, nil
	}
	index.Exists = true
	index.Table = definition.TableName
	if !strings.EqualFold(strings.TrimSpace(definition.TableName), tableName) {
		return index, nil
	}
	if strings.TrimSpace(definition.SQL) == "" {
		index.Expression = true
	}

	var listRow struct {
		Unique  int `gorm:"column:is_unique"`
		Partial int `gorm:"column:partial"`
	}
	listResult := db.Raw(`SELECT "unique" AS is_unique, partial
		FROM pragma_index_list(?) WHERE name = ?`, tableName, indexName).Scan(&listRow)
	if listResult.Error != nil {
		return paymentPhysicalIndex{}, listResult.Error
	}
	if listResult.RowsAffected == 0 {
		return index, nil
	}
	index.Unique = listRow.Unique != 0
	index.Partial = listRow.Partial != 0

	var columns []struct {
		SeqNo int            `gorm:"column:seqno"`
		CID   int            `gorm:"column:cid"`
		Name  sql.NullString `gorm:"column:name"`
	}
	if err := db.Raw("SELECT seqno, cid, name FROM pragma_index_info(?) ORDER BY seqno", indexName).Scan(&columns).Error; err != nil {
		return paymentPhysicalIndex{}, err
	}
	for position, column := range columns {
		if column.SeqNo != position || column.CID < 0 || !column.Name.Valid {
			index.Expression = true
			index.Columns = append(index.Columns, "")
			continue
		}
		index.Columns = append(index.Columns, strings.ToLower(strings.TrimSpace(column.Name.String)))
	}
	return index, nil
}

func mysqlPaymentIndexDefinition(db *gorm.DB, tableName, indexName string) (paymentPhysicalIndex, error) {
	index := paymentPhysicalIndex{Table: tableName, Name: indexName, Valid: true, Ready: true, Live: true}
	var rows []struct {
		NonUnique int            `gorm:"column:non_unique"`
		Column    sql.NullString `gorm:"column:column_name"`
		Seq       int            `gorm:"column:seq_in_index"`
		SubPart   sql.NullInt64  `gorm:"column:sub_part"`
		IndexType string         `gorm:"column:index_type"`
	}
	result := db.Raw(`SELECT NON_UNIQUE AS non_unique, COLUMN_NAME AS column_name,
		SEQ_IN_INDEX AS seq_in_index, SUB_PART AS sub_part, INDEX_TYPE AS index_type
		FROM information_schema.statistics
		WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?
		ORDER BY SEQ_IN_INDEX`, tableName, indexName).Scan(&rows)
	if result.Error != nil {
		return paymentPhysicalIndex{}, result.Error
	}
	if len(rows) == 0 {
		return index, nil
	}
	index.Exists = true
	index.Unique = rows[0].NonUnique == 0
	for position, row := range rows {
		if row.NonUnique != rows[0].NonUnique || row.Seq != position+1 {
			index.Expression = true
		}
		if row.SubPart.Valid {
			index.Prefix = true
			index.PrefixLengths = append(index.PrefixLengths, row.SubPart.Int64)
		} else {
			index.PrefixLengths = append(index.PrefixLengths, 0)
		}
		if !row.Column.Valid {
			index.Expression = true
			index.Columns = append(index.Columns, "")
		} else {
			index.Columns = append(index.Columns, strings.ToLower(strings.TrimSpace(row.Column.String)))
		}
		if !strings.EqualFold(strings.TrimSpace(row.IndexType), "BTREE") {
			index.UnsupportedMethod = true
		}
	}
	return index, nil
}

func postgresPaymentIndexDefinition(db *gorm.DB, tableName, indexName string) (paymentPhysicalIndex, error) {
	index := paymentPhysicalIndex{Name: indexName}
	var rows []struct {
		TableName       string         `gorm:"column:table_name"`
		IndexName       string         `gorm:"column:index_name"`
		Unique          bool           `gorm:"column:is_unique"`
		Partial         bool           `gorm:"column:is_partial"`
		Expression      bool           `gorm:"column:has_expression"`
		Valid           bool           `gorm:"column:is_valid"`
		Ready           bool           `gorm:"column:is_ready"`
		Live            bool           `gorm:"column:is_live"`
		Ordinal         int            `gorm:"column:ordinality"`
		AttNum          int            `gorm:"column:attnum"`
		Column          sql.NullString `gorm:"column:column_name"`
		AccessMethod    string         `gorm:"column:access_method"`
		IndexDefinition string         `gorm:"column:index_definition"`
	}
	result := db.Raw(`SELECT tbl.relname AS table_name, idx.relname AS index_name,
		i.indisunique AS is_unique, (i.indpred IS NOT NULL) AS is_partial,
		(i.indexprs IS NOT NULL) AS has_expression, i.indisvalid AS is_valid,
		i.indisready AS is_ready, i.indislive AS is_live, key.ord AS ordinality,
		key.attnum, attr.attname AS column_name, am.amname AS access_method,
		pg_get_indexdef(i.indexrelid) AS index_definition
		FROM pg_class idx
		JOIN pg_namespace index_ns ON index_ns.oid = idx.relnamespace
		JOIN pg_index i ON i.indexrelid = idx.oid
		JOIN pg_class tbl ON tbl.oid = i.indrelid
		JOIN pg_am am ON am.oid = idx.relam
		JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS key(attnum, ord) ON TRUE
		LEFT JOIN pg_attribute attr ON attr.attrelid = tbl.oid AND attr.attnum = key.attnum
		WHERE idx.relname = ? AND index_ns.nspname = current_schema()
		ORDER BY key.ord`, indexName).Scan(&rows)
	if result.Error != nil {
		return paymentPhysicalIndex{}, result.Error
	}
	if len(rows) == 0 {
		return index, nil
	}
	first := rows[0]
	index.Exists = true
	index.Table = first.TableName
	index.Name = first.IndexName
	index.Unique = first.Unique
	index.Partial = first.Partial
	index.Expression = first.Expression
	index.Valid = first.Valid
	index.Ready = first.Ready
	index.Live = first.Live
	index.NullsNotDistinct = strings.Contains(strings.ToUpper(first.IndexDefinition), "NULLS NOT DISTINCT")
	index.UnsupportedMethod = !strings.EqualFold(strings.TrimSpace(first.AccessMethod), "btree")
	for position, row := range rows {
		if row.TableName != first.TableName || row.IndexName != first.IndexName || row.Unique != first.Unique ||
			row.Partial != first.Partial || row.Expression != first.Expression || row.Valid != first.Valid ||
			row.Ready != first.Ready || row.Live != first.Live || row.Ordinal != position+1 {
			index.Expression = true
		}
		if row.AttNum <= 0 || !row.Column.Valid {
			index.Expression = true
			index.Columns = append(index.Columns, "")
		} else {
			index.Columns = append(index.Columns, strings.ToLower(strings.TrimSpace(row.Column.String)))
		}
	}
	return index, nil
}

func ensureProviderRefundEventIdentity(db *gorm.DB, dbType common.DatabaseType) error {
	if err := validateProviderRefundEventKeyColumn(db, dbType); err != nil {
		return err
	}
	if err := validateProviderRefundEventIdentityRows(db); err != nil {
		return err
	}

	expectedColumns := []string{"event_key"}
	definition, err := paymentIndexDefinition(db, dbType, "provider_refund_events", providerRefundEventKeyIndexName)
	if err != nil {
		return fmt.Errorf("inspect provider_refund_events identity index: %w", err)
	}
	if definition.Exists {
		if !paymentIndexDefinitionMatches(definition, "provider_refund_events", providerRefundEventKeyIndexName, expectedColumns, true) {
			return fmt.Errorf("provider_refund_events index %s has unsafe definition", providerRefundEventKeyIndexName)
		}
		return nil
	}

	if err := db.Migrator().CreateIndex(&ProviderRefundEvent{}, providerRefundEventKeyIndexName); err != nil {
		definitionAfterError, inspectErr := paymentIndexDefinition(db, dbType, "provider_refund_events", providerRefundEventKeyIndexName)
		if inspectErr == nil && paymentIndexDefinitionMatches(definitionAfterError, "provider_refund_events", providerRefundEventKeyIndexName, expectedColumns, true) {
			return nil
		}
		return fmt.Errorf("create provider_refund_events identity index: %w", err)
	}
	definition, err = paymentIndexDefinition(db, dbType, "provider_refund_events", providerRefundEventKeyIndexName)
	if err != nil {
		return fmt.Errorf("inspect created provider_refund_events identity index: %w", err)
	}
	if !paymentIndexDefinitionMatches(definition, "provider_refund_events", providerRefundEventKeyIndexName, expectedColumns, true) {
		return fmt.Errorf("provider_refund_events index %s was created with unsafe definition", providerRefundEventKeyIndexName)
	}
	return nil
}

func validateProviderRefundEventKeyColumn(db *gorm.DB, dbType common.DatabaseType) error {
	if dbType == common.DatabaseTypeSQLite {
		var rows []struct {
			Name    string `gorm:"column:name"`
			NotNull int    `gorm:"column:is_not_null"`
		}
		if err := db.Raw(`SELECT name, "notnull" AS is_not_null
			FROM pragma_table_info(?)`, "provider_refund_events").Scan(&rows).Error; err != nil {
			return fmt.Errorf("inspect SQLite provider_refund_events.event_key: %w", err)
		}
		for _, row := range rows {
			if !strings.EqualFold(strings.TrimSpace(row.Name), "event_key") {
				continue
			}
			if row.NotNull == 0 {
				return errors.New("provider_refund_events.event_key is nullable; alter it to NOT NULL before startup")
			}
			return nil
		}
		return errors.New("provider_refund_events.event_key is missing after migration")
	}

	columnTypes, err := db.Migrator().ColumnTypes(&ProviderRefundEvent{})
	if err != nil {
		return fmt.Errorf("inspect provider_refund_events.event_key: %w", err)
	}
	for _, columnType := range columnTypes {
		if !strings.EqualFold(strings.TrimSpace(columnType.Name()), "event_key") {
			continue
		}
		nullable, known := columnType.Nullable()
		if !known {
			return errors.New("provider_refund_events.event_key nullability is unavailable; verify it is NOT NULL before startup")
		}
		if nullable {
			return errors.New("provider_refund_events.event_key is nullable; alter it to NOT NULL before startup")
		}
		return nil
	}
	return errors.New("provider_refund_events.event_key is missing after migration")
}

func validateProviderRefundEventIdentityRows(db *gorm.DB) error {
	var invalidCount int64
	if err := db.Table("provider_refund_events").
		Where("event_key IS NULL OR TRIM(event_key) = ''").
		Count(&invalidCount).Error; err != nil {
		return fmt.Errorf("validate provider_refund_events event keys: %w", err)
	}
	if invalidCount > 0 {
		return fmt.Errorf("provider_refund_events contains %d rows with an empty event key; reconcile before startup", invalidCount)
	}

	var duplicate struct {
		EventKey       string `gorm:"column:event_key"`
		DuplicateCount int64  `gorm:"column:duplicate_count"`
	}
	result := db.Table("provider_refund_events").
		Select("event_key, COUNT(*) AS duplicate_count").
		Group("event_key").
		Having("COUNT(*) > ?", 1).
		Limit(1).
		Scan(&duplicate)
	if result.Error != nil {
		return fmt.Errorf("validate duplicate provider_refund_events event keys: %w", result.Error)
	}
	if result.RowsAffected > 0 {
		return fmt.Errorf("provider_refund_events has duplicate event key %q (%d rows); reconcile before startup", duplicate.EventKey, duplicate.DuplicateCount)
	}
	return nil
}

func paymentMigrationDatabaseType(db *gorm.DB, configured common.DatabaseType) common.DatabaseType {
	if db != nil {
		switch strings.ToLower(db.Dialector.Name()) {
		case string(common.DatabaseTypeSQLite):
			return common.DatabaseTypeSQLite
		case string(common.DatabaseTypeMySQL):
			return common.DatabaseTypeMySQL
		case string(common.DatabaseTypePostgreSQL), "postgresql":
			return common.DatabaseTypePostgreSQL
		}
	}
	return configured
}

func addPaymentSchemaColumns(db *gorm.DB, dbType common.DatabaseType, table paymentSchemaTable) error {
	for _, column := range table.Columns {
		if db.Migrator().HasColumn(table.Model, column.Name) {
			continue
		}
		ddl, ok := column.DDL[dbType]
		if !ok || strings.TrimSpace(ddl) == "" {
			return fmt.Errorf("no payment schema DDL for %s.%s on %s", table.Name, column.DBName, dbType)
		}
		query := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", quotePaymentIdentifier(table.Name, dbType), quotePaymentIdentifier(column.DBName, dbType), ddl)
		if err := db.Exec(query).Error; err != nil {
			// Multiple application masters may observe the same missing column
			// during a rolling deployment. If another master won the DDL race,
			// its duplicate-column error is benign; verify the physical schema
			// before returning the error instead of making startup order-dependent.
			if db.Migrator().HasColumn(table.Model, column.Name) {
				continue
			}
			return fmt.Errorf("add payment schema column %s.%s: %w", table.Name, column.DBName, err)
		}
	}
	return nil
}

func ensurePaymentOrderIndexes(db *gorm.DB, table paymentSchemaTable) error {
	for _, indexName := range table.Indexes {
		if db.Migrator().HasIndex(table.Model, indexName) {
			continue
		}
		if err := db.Migrator().CreateIndex(table.Model, indexName); err != nil {
			// A concurrent migrator may have created the exact index between the
			// HasIndex check and CreateIndex. Re-read metadata and accept only that
			// case; malformed or unrelated indexes still fail closed below.
			if db.Migrator().HasIndex(table.Model, indexName) {
				continue
			}
			return fmt.Errorf("create payment order index %s on %s: %w", indexName, table.Name, err)
		}
	}
	return nil
}

func ensurePaymentEventTable(db *gorm.DB) error {
	if db.Migrator().HasTable(&PaymentEvent{}) {
		return nil
	}
	// Let GORM create the complete baseline table and its primary key. This
	// avoids hand-written AUTO_INCREMENT/SERIAL syntax and keeps fresh
	// installations aligned with the model's per-dialect primary-key rules.
	if err := db.AutoMigrate(&PaymentEvent{}); err != nil && !db.Migrator().HasTable(&PaymentEvent{}) {
		return fmt.Errorf("create payment_events table: %w", err)
	}
	// If another startup process created the table while this process was
	// running AutoMigrate, the table now exists and the additive/validation pass
	// below can finish any columns or indexes it left behind.
	return nil
}

func addPaymentEventColumns(db *gorm.DB, dbType common.DatabaseType) error {
	model := &PaymentEvent{}
	if !db.Migrator().HasTable(model) {
		return errors.New("payment_events table was not created")
	}

	// Adding a NOT NULL identity column to a non-empty partially-created event
	// table cannot be made correct by inventing a default. Fail before any such
	// DDL and require an operator to reconcile the existing events explicitly.
	missingRequired := make([]string, 0, 4)
	for _, column := range paymentEventColumns() {
		if column.Required && column.Name != "EventKey" && !db.Migrator().HasColumn(model, column.Name) {
			missingRequired = append(missingRequired, column.DBName)
		}
	}
	if len(missingRequired) > 0 {
		var rowCount int64
		if err := db.Table("payment_events").Count(&rowCount).Error; err != nil {
			return fmt.Errorf("inspect payment_events before adding identity columns: %w", err)
		}
		if rowCount > 0 {
			return fmt.Errorf("payment_events has %d rows but is missing required columns %s; reconcile rows before migration", rowCount, strings.Join(missingRequired, ", "))
		}
	}

	for _, column := range paymentEventColumns() {
		if db.Migrator().HasColumn(model, column.Name) {
			continue
		}
		ddl, ok := column.DDL[dbType]
		if !ok || strings.TrimSpace(ddl) == "" {
			return fmt.Errorf("no payment event DDL for %s on %s", column.DBName, dbType)
		}
		query := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", quotePaymentIdentifier("payment_events", dbType), quotePaymentIdentifier(column.DBName, dbType), ddl)
		if err := db.Exec(query).Error; err != nil {
			if db.Migrator().HasColumn(model, column.Name) {
				continue
			}
			return fmt.Errorf("add payment_events.%s: %w", column.DBName, err)
		}
	}
	return nil
}

func paymentEventColumns() []paymentSchemaColumn {
	return []paymentSchemaColumn{
		{Name: "EventKey", DBName: "event_key", Required: true, DDL: paymentDDL("CHAR(64) NOT NULL DEFAULT ''")},
		{Name: "Provider", DBName: "provider", Required: true, DDL: paymentDDL("VARCHAR(50) NOT NULL")},
		{Name: "ProviderTradeNo", DBName: "provider_trade_no", Required: true, DDL: paymentDDL("VARCHAR(255) NOT NULL")},
		{Name: "OrderTradeNo", DBName: "order_trade_no", Required: true, DDL: paymentDDL("VARCHAR(255) NOT NULL")},
		{Name: "OrderKind", DBName: "order_kind", Required: true, DDL: paymentDDL("VARCHAR(32) NOT NULL")},
		{Name: "Payload", DBName: "payload", DDL: paymentDDL("TEXT")},
		{Name: "CreateTime", DBName: "create_time", DDL: paymentDDL("BIGINT")},
	}
}

func ensurePaymentEventKeyIdentity(db *gorm.DB, dbType common.DatabaseType) error {
	if err := validatePaymentEventIdentityRows(db); err != nil {
		return err
	}
	if err := backfillPaymentEventKeys(db); err != nil {
		return err
	}
	if err := validatePaymentEventKeyRows(db); err != nil {
		return err
	}

	expectedColumns := []string{"event_key"}
	definition, err := paymentIndexDefinition(db, dbType, "payment_events", paymentEventKeyIndexName)
	if err != nil {
		return fmt.Errorf("inspect payment_events identity index: %w", err)
	}
	if definition.Exists {
		if !paymentIndexDefinitionMatches(definition, "payment_events", paymentEventKeyIndexName, expectedColumns, true) {
			return fmt.Errorf("payment_events index %s has unsafe definition", paymentEventKeyIndexName)
		}
		return nil
	}

	if err := db.Migrator().CreateIndex(&PaymentEvent{}, paymentEventKeyIndexName); err != nil {
		definitionAfterError, inspectErr := paymentIndexDefinition(db, dbType, "payment_events", paymentEventKeyIndexName)
		if inspectErr == nil && paymentIndexDefinitionMatches(definitionAfterError, "payment_events", paymentEventKeyIndexName, expectedColumns, true) {
			return nil
		}
		return fmt.Errorf("create payment_events identity index: %w", err)
	}
	definition, err = paymentIndexDefinition(db, dbType, "payment_events", paymentEventKeyIndexName)
	if err != nil {
		return fmt.Errorf("inspect created payment_events identity index: %w", err)
	}
	if !paymentIndexDefinitionMatches(definition, "payment_events", paymentEventKeyIndexName, expectedColumns, true) {
		return fmt.Errorf("payment_events index %s was created with unsafe definition", paymentEventKeyIndexName)
	}
	return nil
}

func backfillPaymentEventKeys(db *gorm.DB) error {
	const batchSize = 500
	lastID := 0
	for {
		var events []PaymentEvent
		if err := db.Select("id", "event_key", "provider", "provider_trade_no", "order_trade_no", "order_kind").
			Where("id > ?", lastID).Order("id ASC").Limit(batchSize).Find(&events).Error; err != nil {
			return fmt.Errorf("load payment_events identity batch: %w", err)
		}
		if len(events) == 0 {
			return nil
		}
		for i := range events {
			event := &events[i]
			if event.Id <= lastID || event.Provider != strings.TrimSpace(event.Provider) ||
				event.ProviderTradeNo != strings.TrimSpace(event.ProviderTradeNo) {
				return fmt.Errorf("payment_events row %d has a non-canonical provider identity; reconcile before startup", event.Id)
			}
			expected, ok := PaymentEventKey(event.Provider, event.ProviderTradeNo)
			if !ok {
				return fmt.Errorf("payment_events row %d has an invalid provider identity; reconcile before startup", event.Id)
			}
			stored := strings.TrimSpace(event.EventKey)
			if stored != "" {
				if stored != expected {
					return fmt.Errorf("payment_events row %d has an invalid event key; reconcile before startup", event.Id)
				}
				lastID = event.Id
				continue
			}
			result := db.Model(&PaymentEvent{}).
				Where("id = ? AND (event_key IS NULL OR TRIM(event_key) = '')", event.Id).
				UpdateColumn("event_key", expected)
			if result.Error != nil {
				return fmt.Errorf("backfill payment_events row %d event key: %w", event.Id, result.Error)
			}
			if result.RowsAffected == 0 {
				var winner PaymentEvent
				if err := db.Select("event_key").Where("id = ?", event.Id).First(&winner).Error; err != nil || winner.EventKey != expected {
					return fmt.Errorf("payment_events row %d event key changed concurrently", event.Id)
				}
			}
			lastID = event.Id
		}
	}
}

func validatePaymentEventKeyRows(db *gorm.DB) error {
	var invalidCount int64
	if err := db.Table("payment_events").
		Where("event_key IS NULL OR TRIM(event_key) = ''").Count(&invalidCount).Error; err != nil {
		return fmt.Errorf("validate payment_events event keys: %w", err)
	}
	if invalidCount > 0 {
		return fmt.Errorf("payment_events contains %d rows with an empty event key; reconcile before startup", invalidCount)
	}
	var duplicate struct {
		EventKey       string `gorm:"column:event_key"`
		DuplicateCount int64  `gorm:"column:duplicate_count"`
	}
	result := db.Table("payment_events").Select("event_key, COUNT(*) AS duplicate_count").
		Group("event_key").Having("COUNT(*) > ?", 1).Limit(1).Scan(&duplicate)
	if result.Error != nil {
		return fmt.Errorf("validate duplicate payment_events event keys: %w", result.Error)
	}
	if result.RowsAffected > 0 {
		return fmt.Errorf("payment_events has duplicate event key %q (%d rows); reconcile before startup", duplicate.EventKey, duplicate.DuplicateCount)
	}
	return nil
}

// validatePaymentEventRequiredColumns makes the model's NOT NULL contract
// explicit for partially-created/hand-managed payment_events tables. A unique
// index alone is not enough: SQL permits multiple NULL values in a unique
// index, which would leave a hole in the provider-transaction idempotency
// fence. We fail closed instead of silently rebuilding a live table (SQLite in
// particular cannot alter nullability without a table rewrite).

func validatePaymentEventRequiredColumns(db *gorm.DB, dbType common.DatabaseType) error {
	if dbType == common.DatabaseTypeSQLite {
		// The SQLite GORM migrator reports every column as non-nullable due to a
		// parser limitation, so read the authoritative PRAGMA table metadata.
		var rows []struct {
			Name    string `gorm:"column:name"`
			NotNull int    `gorm:"column:is_not_null"`
		}
		result := db.Raw(`SELECT name, "notnull" AS is_not_null
			FROM pragma_table_info(?)`, "payment_events").Scan(&rows)
		if result.Error != nil {
			return fmt.Errorf("inspect SQLite payment_events required columns: %w", result.Error)
		}
		required := make(map[string]struct{})
		for _, column := range paymentEventColumns() {
			if column.Required {
				required[strings.ToLower(column.DBName)] = struct{}{}
			}
		}
		found := make(map[string]bool, len(required))
		for _, row := range rows {
			name := strings.ToLower(strings.TrimSpace(row.Name))
			if _, ok := required[name]; !ok {
				continue
			}
			found[name] = true
			if row.NotNull == 0 {
				return fmt.Errorf("payment_events.%s is nullable; alter it to NOT NULL before startup", name)
			}
		}
		for name := range required {
			if !found[name] {
				return fmt.Errorf("payment_events.%s is missing after migration", name)
			}
		}
		return nil
	}
	columnTypes, err := db.Migrator().ColumnTypes(&PaymentEvent{})
	if err != nil {
		return fmt.Errorf("inspect payment_events required columns: %w", err)
	}
	required := make(map[string]struct{})
	for _, column := range paymentEventColumns() {
		if column.Required {
			required[strings.ToLower(column.DBName)] = struct{}{}
		}
	}
	found := make(map[string]bool, len(required))
	for _, columnType := range columnTypes {
		name := strings.ToLower(strings.TrimSpace(columnType.Name()))
		if _, ok := required[name]; !ok {
			continue
		}
		found[name] = true
		nullable, known := columnType.Nullable()
		if !known {
			return fmt.Errorf("payment_events.%s nullability is unavailable; verify it is NOT NULL before startup", name)
		}
		if nullable {
			return fmt.Errorf("payment_events.%s is nullable; alter it to NOT NULL before startup", name)
		}
	}
	for name := range required {
		if !found[name] {
			return fmt.Errorf("payment_events.%s is missing after migration", name)
		}
	}
	return nil
}

func validatePaymentEventIdentityRows(db *gorm.DB) error {
	var invalidCount int64
	if err := db.Table("payment_events").
		Where("provider IS NULL OR TRIM(provider) = '' OR provider_trade_no IS NULL OR TRIM(provider_trade_no) = ''").
		Count(&invalidCount).Error; err != nil {
		return fmt.Errorf("validate payment_events identity columns: %w", err)
	}
	if invalidCount > 0 {
		return fmt.Errorf("payment_events contains %d rows with empty provider identity; reconcile before creating unique index", invalidCount)
	}

	var duplicate struct {
		Provider        string
		ProviderTradeNo string
		DuplicateCount  int64 `gorm:"column:duplicate_count"`
	}
	result := db.Table("payment_events").
		Select("provider, provider_trade_no, COUNT(*) AS duplicate_count").
		Group("provider, provider_trade_no").
		Having("COUNT(*) > ?", 1).
		Limit(1).
		Scan(&duplicate)
	if result.Error != nil {
		return fmt.Errorf("validate duplicate payment_events: %w", result.Error)
	}
	if result.RowsAffected > 0 {
		return fmt.Errorf("payment_events has duplicate provider transaction %q/%q (%d rows); reconcile before creating unique index", duplicate.Provider, duplicate.ProviderTradeNo, duplicate.DuplicateCount)
	}
	return nil
}

func equalPaymentIndexColumns(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for index := range expected {
		if !strings.EqualFold(strings.TrimSpace(actual[index]), expected[index]) {
			return false
		}
	}
	return true
}

func quotePaymentIdentifier(identifier string, dbType common.DatabaseType) string {
	if dbType == common.DatabaseTypePostgreSQL {
		return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
	}
	return "`" + strings.ReplaceAll(identifier, "`", "``") + "`"
}

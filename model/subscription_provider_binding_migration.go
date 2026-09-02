package model

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const subscriptionProviderBindingBackfillBatchSize = 500

type subscriptionProviderBindingColumn struct {
	Name     string
	DBName   string
	DDL      map[common.DatabaseType]string
	Required bool
}

func subscriptionProviderBindingColumns() []subscriptionProviderBindingColumn {
	return []subscriptionProviderBindingColumn{
		{Name: "UserSubscriptionID", DBName: "user_subscription_id", DDL: paymentDDL("BIGINT NOT NULL DEFAULT 0"), Required: true},
		{Name: "OrderTradeNo", DBName: "order_trade_no", DDL: paymentDDL("VARCHAR(255)"), Required: false},
		{Name: "Provider", DBName: "provider", DDL: paymentDDL("VARCHAR(50) NOT NULL DEFAULT ''"), Required: true},
		{Name: "ProviderSubscriptionID", DBName: "provider_subscription_id", DDL: paymentDDL("VARCHAR(255) NOT NULL DEFAULT ''"), Required: true},
		{Name: "ProviderSubscriptionKey", DBName: "provider_subscription_key", DDL: paymentDDL("CHAR(64) NOT NULL DEFAULT ''"), Required: true},
		{Name: "CreatedAt", DBName: "created_at", DDL: paymentDDL("BIGINT NOT NULL DEFAULT 0"), Required: false},
		{Name: "UpdatedAt", DBName: "updated_at", DDL: paymentDDL("BIGINT NOT NULL DEFAULT 0"), Required: false},
	}
}

// migrateSubscriptionProviderBindingSchemaExpand installs the recurring
// identity ledger before webhook handlers can run.  The ledger is additive and
// idempotent; no existing entitlement is deleted or reassigned.
func migrateSubscriptionProviderBindingSchemaExpand() error {
	if DB == nil {
		return errors.New("database is not initialized")
	}
	return MigrateSubscriptionProviderBindingSchemaExpand(DB, paymentMigrationDatabaseType(DB, common.MainDatabaseType()))
}

// MigrateSubscriptionProviderBindingSchemaExpand is exported for operator
// tooling and migration tests.  Legacy user_subscription rows are copied only
// when provider + subscription ID identifies one entitlement unambiguously.
// Rows with no provider namespace remain untouched and therefore fail closed
// in provider-scoped lifecycle lookups.
func MigrateSubscriptionProviderBindingSchemaExpand(db *gorm.DB, dbType common.DatabaseType) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	dbType = paymentMigrationDatabaseType(db, dbType)
	if dbType != common.DatabaseTypeSQLite && dbType != common.DatabaseTypeMySQL && dbType != common.DatabaseTypePostgreSQL {
		return fmt.Errorf("subscription provider binding migration does not support database type %q", dbType)
	}

	model := &SubscriptionProviderBindingRecord{}
	if !db.Migrator().HasTable(model) {
		if err := db.AutoMigrate(model); err != nil && !db.Migrator().HasTable(model) {
			return fmt.Errorf("create subscription_provider_bindings table: %w", err)
		}
	}
	// Run the additive column pass even when AutoMigrate just observed a table
	// created by another startup process.  A concurrent/previously interrupted
	// AutoMigrate can leave a table present but missing one of the ledger
	// columns; treating HasTable=true as proof that the schema is complete would
	// make the next startup fail closed forever.
	if err := addSubscriptionProviderBindingColumns(db, dbType); err != nil {
		return err
	}
	if !db.Migrator().HasTable(model) {
		return errors.New("subscription_provider_bindings table was not created")
	}
	if err := validateSubscriptionProviderBindingColumns(db, dbType); err != nil {
		return err
	}
	if err := validateSubscriptionProviderBindingRows(db); err != nil {
		return err
	}
	// Install the fences before copying legacy rows.  This matters when two
	// application masters are upgraded at the same time: both may observe the
	// same legacy row, but only one can win each INSERT once the indexes exist.
	if err := ensureSubscriptionProviderBindingIndexes(db); err != nil {
		return err
	}
	if err := backfillSubscriptionProviderBindings(db); err != nil {
		return err
	}
	return validateSubscriptionProviderBindingRows(db)
}

func addSubscriptionProviderBindingColumns(db *gorm.DB, dbType common.DatabaseType) error {
	model := &SubscriptionProviderBindingRecord{}
	var rowCount int64
	if err := db.Table(model.TableName()).Count(&rowCount).Error; err != nil {
		return fmt.Errorf("inspect subscription_provider_bindings before migration: %w", err)
	}
	for _, column := range subscriptionProviderBindingColumns() {
		if db.Migrator().HasColumn(model, column.Name) {
			continue
		}
		if column.Required && rowCount > 0 {
			return fmt.Errorf("subscription_provider_bindings has %d rows but is missing required column %s; reconcile rows before migration", rowCount, column.DBName)
		}
		ddl, ok := column.DDL[dbType]
		if !ok || strings.TrimSpace(ddl) == "" {
			return fmt.Errorf("no subscription provider binding DDL for %s on %s", column.DBName, dbType)
		}
		query := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s",
			quotePaymentIdentifier(model.TableName(), dbType),
			quotePaymentIdentifier(column.DBName, dbType), ddl)
		if err := db.Exec(query).Error; err != nil {
			// Two application masters can both observe the column as absent and
			// race to add it.  The loser receives a dialect-specific "already
			// exists/duplicate column" error; re-read metadata before surfacing the
			// error so that only genuine DDL failures abort startup.  This check is
			// intentionally after the failed statement because some drivers cache
			// schema metadata until the first DDL attempt.
			if db.Migrator().HasColumn(model, column.Name) {
				continue
			}
			return fmt.Errorf("add subscription_provider_bindings.%s: %w", column.DBName, err)
		}
	}
	return nil
}

func validateSubscriptionProviderBindingColumns(db *gorm.DB, dbType common.DatabaseType) error {
	model := &SubscriptionProviderBindingRecord{}
	required := make(map[string]struct{})
	for _, column := range subscriptionProviderBindingColumns() {
		if column.Required {
			required[strings.ToLower(column.DBName)] = struct{}{}
		}
	}
	if dbType == common.DatabaseTypeSQLite {
		var rows []struct {
			Name    string `gorm:"column:name"`
			NotNull int    `gorm:"column:is_not_null"`
		}
		if err := db.Raw(`SELECT name, "notnull" AS is_not_null FROM pragma_table_info(?)`, model.TableName()).Scan(&rows).Error; err != nil {
			return fmt.Errorf("inspect SQLite subscription provider binding columns: %w", err)
		}
		found := make(map[string]bool, len(required))
		for _, row := range rows {
			name := strings.ToLower(strings.TrimSpace(row.Name))
			if _, ok := required[name]; !ok {
				continue
			}
			found[name] = true
			if row.NotNull == 0 {
				return fmt.Errorf("subscription_provider_bindings.%s is nullable; repair it before startup", name)
			}
		}
		for name := range required {
			if !found[name] {
				return fmt.Errorf("subscription_provider_bindings.%s is missing after migration", name)
			}
		}
		return nil
	}
	columnTypes, err := db.Migrator().ColumnTypes(model)
	if err != nil {
		return fmt.Errorf("inspect subscription provider binding columns: %w", err)
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
			return fmt.Errorf("subscription_provider_bindings.%s nullability is unavailable; verify it is NOT NULL before startup", name)
		}
		if nullable {
			return fmt.Errorf("subscription_provider_bindings.%s is nullable; repair it before startup", name)
		}
	}
	for name := range required {
		if !found[name] {
			return fmt.Errorf("subscription_provider_bindings.%s is missing after migration", name)
		}
	}
	return nil
}

type subscriptionProviderBindingBackfillRow struct {
	ID                     int    `gorm:"column:id"`
	Provider               string `gorm:"column:provider_subscription_provider"`
	ProviderSubscriptionID string `gorm:"column:provider_subscription_id"`
	OrderTradeNo           string `gorm:"column:subscription_order_trade_no"`
}

func backfillSubscriptionProviderBindings(db *gorm.DB) error {
	if !db.Migrator().HasTable(&UserSubscription{}) || !db.Migrator().HasTable(&SubscriptionProviderBindingRecord{}) {
		return nil
	}
	// Provider namespace is mandatory for a safe backfill.  If an old row has an
	// ID but no namespace, leave it in the legacy columns; lifecycle handlers
	// query the ledger by provider+ID and will not guess its owner.
	if !db.Migrator().HasColumn(&UserSubscription{}, "ProviderSubscriptionProvider") ||
		!db.Migrator().HasColumn(&UserSubscription{}, "ProviderSubscriptionID") {
		return nil
	}
	var duplicate struct {
		Provider               string `gorm:"column:provider"`
		ProviderSubscriptionID string `gorm:"column:provider_subscription_id"`
		DuplicateCount         int64  `gorm:"column:duplicate_count"`
	}
	dupResult := db.Table("user_subscriptions").
		Select("LOWER(TRIM(provider_subscription_provider)) AS provider, provider_subscription_id, COUNT(*) AS duplicate_count").
		Where("provider_subscription_id IS NOT NULL AND TRIM(provider_subscription_id) <> '' AND provider_subscription_provider IS NOT NULL AND TRIM(provider_subscription_provider) <> ''").
		Group("LOWER(TRIM(provider_subscription_provider)), provider_subscription_id").
		Having("COUNT(*) > ?", 1).Limit(1).Scan(&duplicate)
	if dupResult.Error != nil {
		return fmt.Errorf("validate legacy subscription provider identities: %w", dupResult.Error)
	}
	if dupResult.RowsAffected > 0 {
		return fmt.Errorf("user_subscriptions contain duplicate provider identity %q/%q (%d rows); reconcile before enabling the binding ledger", duplicate.Provider, duplicate.ProviderSubscriptionID, duplicate.DuplicateCount)
	}

	lastID := 0
	for {
		var rows []subscriptionProviderBindingBackfillRow
		query := db.Table("user_subscriptions").
			Select("id, provider_subscription_provider, provider_subscription_id, subscription_order_trade_no").
			Where("id > ? AND provider_subscription_id IS NOT NULL AND TRIM(provider_subscription_id) <> '' AND provider_subscription_provider IS NOT NULL AND TRIM(provider_subscription_provider) <> ''", lastID).
			Order("id").Limit(subscriptionProviderBindingBackfillBatchSize)
		if !db.Migrator().HasColumn(&UserSubscription{}, "SubscriptionOrderTradeNo") {
			query = db.Table("user_subscriptions").
				Select("id, provider_subscription_provider, provider_subscription_id").
				Where("id > ? AND provider_subscription_id IS NOT NULL AND TRIM(provider_subscription_id) <> '' AND provider_subscription_provider IS NOT NULL AND TRIM(provider_subscription_provider) <> ''", lastID).
				Order("id").Limit(subscriptionProviderBindingBackfillBatchSize)
		}
		if err := query.Scan(&rows).Error; err != nil {
			return fmt.Errorf("read legacy subscription provider identities: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if row.ID > lastID {
				lastID = row.ID
			}
			provider := normalizeSubscriptionBindingProvider(row.Provider)
			providerSubscriptionID := strings.TrimSpace(row.ProviderSubscriptionID)
			key, ok := SubscriptionProviderBindingKey(provider, providerSubscriptionID)
			if !ok {
				return fmt.Errorf("legacy user_subscription id=%d has invalid provider identity", row.ID)
			}
			var orderTradeNo *string
			if tradeNo := strings.TrimSpace(row.OrderTradeNo); tradeNo != "" {
				orderTradeNo = &tradeNo
			}
			candidate := &SubscriptionProviderBindingRecord{
				UserSubscriptionID:      row.ID,
				OrderTradeNo:            orderTradeNo,
				Provider:                provider,
				ProviderSubscriptionID:  providerSubscriptionID,
				ProviderSubscriptionKey: key,
			}
			if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(candidate).Error; err != nil {
				return fmt.Errorf("backfill subscription provider identity id=%d: %w", row.ID, err)
			}
			var bound SubscriptionProviderBindingRecord
			if err := db.Where("provider_subscription_key = ?", key).First(&bound).Error; err != nil {
				return fmt.Errorf("verify backfilled subscription provider identity id=%d: %w", row.ID, err)
			}
			if bound.UserSubscriptionID != row.ID || strings.TrimSpace(bound.ProviderSubscriptionID) != providerSubscriptionID || normalizeSubscriptionBindingProvider(bound.Provider) != provider {
				return fmt.Errorf("backfilled provider identity %q/%q points to subscription %d, expected %d", provider, providerSubscriptionID, bound.UserSubscriptionID, row.ID)
			}
		}
	}
}

func validateSubscriptionProviderBindingRows(db *gorm.DB) error {
	var invalid int64
	if err := db.Table("subscription_provider_bindings").
		Where("user_subscription_id <= 0 OR TRIM(provider) = '' OR TRIM(provider_subscription_id) = '' OR TRIM(provider_subscription_key) = ''").
		Count(&invalid).Error; err != nil {
		return fmt.Errorf("validate subscription provider binding rows: %w", err)
	}
	if invalid > 0 {
		return fmt.Errorf("subscription_provider_bindings contains %d rows with empty identity fields; reconcile before creating unique indexes", invalid)
	}
	// Validate the digest against the raw pair. This catches hand-edited rows
	// and makes a digest collision fail closed rather than silently aliasing an
	// unrelated provider object.
	var rows []SubscriptionProviderBindingRecord
	if err := db.Find(&rows).Error; err != nil {
		return fmt.Errorf("read subscription provider binding rows: %w", err)
	}
	for _, row := range rows {
		key, ok := SubscriptionProviderBindingKey(row.Provider, row.ProviderSubscriptionID)
		if !ok || key != strings.TrimSpace(row.ProviderSubscriptionKey) {
			return fmt.Errorf("subscription_provider_bindings row %d has an invalid identity key", row.Id)
		}
	}
	for _, spec := range []struct {
		column string
		label  string
	}{
		{column: "provider_subscription_key", label: "provider identity"},
		{column: "user_subscription_id", label: "entitlement"},
		{column: "order_trade_no", label: "order"},
	} {
		var duplicate struct {
			Value          string `gorm:"column:value"`
			DuplicateCount int64  `gorm:"column:duplicate_count"`
		}
		query := db.Table("subscription_provider_bindings").Select(spec.column + " AS value, COUNT(*) AS duplicate_count")
		if spec.column == "order_trade_no" {
			query = query.Where("order_trade_no IS NOT NULL AND TRIM(order_trade_no) <> ''")
		}
		result := query.Group(spec.column).Having("COUNT(*) > ?", 1).Limit(1).Scan(&duplicate)
		if result.Error != nil {
			return fmt.Errorf("validate duplicate subscription provider %s bindings: %w", spec.label, result.Error)
		}
		if result.RowsAffected > 0 {
			return fmt.Errorf("subscription_provider_bindings has duplicate %s %q (%d rows); reconcile before creating unique indexes", spec.label, duplicate.Value, duplicate.DuplicateCount)
		}
	}
	return nil
}

func ensureSubscriptionProviderBindingIndexes(db *gorm.DB) error {
	expected := []struct {
		name   string
		cols   []string
		unique bool
	}{
		{name: subscriptionProviderBindingIdentityIndexName, cols: []string{"provider_subscription_key"}, unique: true},
		{name: subscriptionProviderBindingEntitlementIndexName, cols: []string{"user_subscription_id"}, unique: true},
		{name: subscriptionProviderBindingOrderIndexName, cols: []string{"order_trade_no"}, unique: true},
		{name: "idx_spb_provider_id", cols: []string{"provider", "provider_subscription_id"}, unique: false},
	}
	indexes, err := subscriptionProviderBindingIndexes(db)
	if err != nil {
		return fmt.Errorf("inspect subscription provider binding indexes: %w", err)
	}
	for _, want := range expected {
		var found gorm.Index
		for _, index := range indexes {
			if strings.EqualFold(strings.TrimSpace(index.Name()), want.name) {
				found = index
				break
			}
		}
		if found != nil {
			unique, known := found.Unique()
			if !known || unique != want.unique || !equalPaymentIndexColumns(found.Columns(), want.cols) {
				return fmt.Errorf("subscription_provider_bindings index %s has unsafe definition", want.name)
			}
			continue
		}
		if err := db.Migrator().CreateIndex(&SubscriptionProviderBindingRecord{}, want.name); err != nil {
			indexesAfterError, inspectErr := subscriptionProviderBindingIndexes(db)
			if inspectErr == nil && subscriptionProviderBindingIndexMatches(indexesAfterError, want.name, want.cols, want.unique) {
				continue
			}
			return fmt.Errorf("create subscription_provider_bindings index %s: %w", want.name, err)
		}
	}
	return nil
}

// The glebarez SQLite migrator intentionally does not implement GetIndexes
// (it returns "not support").  Inspect SQLite's pragma metadata directly;
// MySQL/PostgreSQL use GORM's dialect-aware index introspection.
func subscriptionProviderBindingIndexes(db *gorm.DB) ([]gorm.Index, error) {
	dbType := paymentMigrationDatabaseType(db, common.MainDatabaseType())
	if dbType != common.DatabaseTypeSQLite {
		return db.Migrator().GetIndexes(&SubscriptionProviderBindingRecord{})
	}
	var rows []struct {
		Name    string `gorm:"column:name"`
		Unique  int    `gorm:"column:is_unique"`
		Origin  string `gorm:"column:origin"`
		Partial int    `gorm:"column:partial"`
	}
	if err := db.Raw(`SELECT name, "unique" AS is_unique, origin, partial
		FROM pragma_index_list(?)`, "subscription_provider_bindings").Scan(&rows).Error; err != nil {
		return nil, err
	}
	indexes := make([]gorm.Index, 0, len(rows))
	for _, row := range rows {
		var columns []struct {
			SeqNo int            `gorm:"column:seqno"`
			CID   int            `gorm:"column:cid"`
			Name  sql.NullString `gorm:"column:name"`
		}
		if err := db.Raw(`SELECT seqno, cid, name FROM pragma_index_info(?) ORDER BY seqno`, row.Name).Scan(&columns).Error; err != nil {
			return nil, err
		}
		indexColumns := make([]string, 0, len(columns))
		for _, column := range columns {
			if column.CID < 0 || !column.Name.Valid {
				indexColumns = append(indexColumns, "")
			} else {
				indexColumns = append(indexColumns, strings.ToLower(strings.TrimSpace(column.Name.String)))
			}
		}
		indexes = append(indexes, &subscriptionProviderBindingIndex{
			name: row.Name, columns: indexColumns, unique: row.Unique != 0 && row.Partial == 0,
		})
	}
	return indexes, nil
}

// subscriptionProviderBindingIndex is a small gorm.Index implementation used
// by the SQLite metadata adapter above.
type subscriptionProviderBindingIndex struct {
	name    string
	columns []string
	unique  bool
}

func (i *subscriptionProviderBindingIndex) Table() string            { return "subscription_provider_bindings" }
func (i *subscriptionProviderBindingIndex) Name() string             { return i.name }
func (i *subscriptionProviderBindingIndex) Columns() []string        { return i.columns }
func (i *subscriptionProviderBindingIndex) PrimaryKey() (bool, bool) { return false, true }
func (i *subscriptionProviderBindingIndex) Unique() (bool, bool)     { return i.unique, true }
func (i *subscriptionProviderBindingIndex) Option() string           { return "" }

func subscriptionProviderBindingIndexMatches(indexes []gorm.Index, name string, columns []string, unique bool) bool {
	for _, index := range indexes {
		if !strings.EqualFold(strings.TrimSpace(index.Name()), name) {
			continue
		}
		isUnique, known := index.Unique()
		return known && isUnique == unique && equalPaymentIndexColumns(index.Columns(), columns)
	}
	return false
}

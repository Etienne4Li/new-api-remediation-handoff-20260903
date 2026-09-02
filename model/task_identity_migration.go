package model

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const taskUpstreamIdentityColumnName = "upstream_task_identity"

// taskIdentityBackfillBatchSize intentionally stays small enough that a
// restart can resume quickly on a large production tasks table.  The
// migration is additive and idempotent: rows whose identity was already
// written are skipped, while rows after the last committed batch are picked
// up on the next startup.
const taskIdentityBackfillBatchSize = 500

// migrateTaskIdentitySchemaExpand installs the nullable identity column and
// its composite unique fence before the regular Task AutoMigrate. Keeping the
// column nullable is important for old rows: they may not have a provider ID,
// and a non-null sentinel would make unrelated legacy rows collide.
func migrateTaskIdentitySchemaExpand() error {
	if DB == nil {
		return errors.New("database is not initialized")
	}
	return migrateTaskIdentitySchemaExpandDB(DB, taskMigrationDatabaseType(DB, common.MainDatabaseType()))
}

// MigrateTaskIdentitySchemaExpand is exported for migration tests and
// operator tooling. It does not backfill opaque historical JSON; those rows
// remain nullable and are handled by the existing duplicate-ID reconciliation
// path until an operator explicitly backfills them.
func MigrateTaskIdentitySchemaExpand(db *gorm.DB, dbType common.DatabaseType) error {
	return migrateTaskIdentitySchemaExpandDB(db, taskMigrationDatabaseType(db, dbType))
}

func taskMigrationDatabaseType(db *gorm.DB, configured common.DatabaseType) common.DatabaseType {
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

func migrateTaskIdentitySchemaExpandDB(db *gorm.DB, dbType common.DatabaseType) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	if dbType != common.DatabaseTypeSQLite && dbType != common.DatabaseTypeMySQL && dbType != common.DatabaseTypePostgreSQL {
		return fmt.Errorf("task identity migration does not support database type %q", dbType)
	}
	if !db.Migrator().HasTable(&Task{}) {
		// Fresh databases are created by the regular AutoMigrate using the model
		// tags. Do not create a partial tasks table in this expand pass.
		return nil
	}
	if !db.Migrator().HasColumn(&Task{}, "UpstreamTaskIdentity") {
		query := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s CHAR(64)",
			quoteTaskIdentityIdentifier("tasks", dbType),
			quoteTaskIdentityIdentifier(taskUpstreamIdentityColumnName, dbType))
		if err := db.Exec(query).Error; err != nil {
			// A rolling deployment may have two masters observe the column as
			// missing and race on ALTER TABLE. Re-read the schema after an error;
			// only a column that is now physically present makes the losing DDL
			// harmless. Genuine DDL failures remain fatal.
			if db.Migrator().HasColumn(&Task{}, "UpstreamTaskIdentity") {
				// Another process completed the additive step; continue with the
				// idempotent backfill and index validation below.
			} else {
				return fmt.Errorf("add tasks.%s: %w", taskUpstreamIdentityColumnName, err)
			}
		}
	}
	// The unique index only protects rows after the new column exists.  Existing
	// rows commonly keep the provider ID inside private_data, so backfill that
	// denormalized identity before creating the fence.  A duplicate is reported
	// explicitly and leaves the index absent; silently choosing one task would
	// make a later provider response settle an arbitrary account.
	if err := backfillTaskUpstreamIdentities(db); err != nil {
		return err
	}
	return ensureTaskUpstreamIdentityIndex(db, dbType)
}

type taskIdentityBackfillRow struct {
	ID                   int64           `gorm:"column:id"`
	ChannelID            int             `gorm:"column:channel_id"`
	Platform             string          `gorm:"column:platform"`
	TaskID               string          `gorm:"column:task_id"`
	PrivateData          TaskPrivateData `gorm:"column:private_data"`
	UpstreamTaskIdentity sql.NullString  `gorm:"column:upstream_task_identity"`
}

// backfillTaskUpstreamIdentities copies the provider identity from the legacy
// JSON column into the bounded digest column.  It deliberately uses a narrow
// projection instead of loading Task: old installations may not yet have all
// of the newer task billing columns, and a broad SELECT would make an
// otherwise safe additive migration fail before it can repair the schema.
func backfillTaskUpstreamIdentities(db *gorm.DB) error {
	if db == nil {
		return errors.New("database is not initialized")
	}
	if !db.Migrator().HasTable("tasks") || !db.Migrator().HasColumn(&Task{}, taskUpstreamIdentityColumnName) {
		return nil
	}

	// private_data was part of the original task schema, but a few hand-created
	// legacy databases omitted it.  In that case we can still safely use the
	// historical TaskID fallback and avoid failing startup solely for a missing
	// optional metadata column.
	hasPrivateData := db.Migrator().HasColumn(&Task{}, "PrivateData")
	lastID := int64(0)
	for {
		query := db.Table("tasks").Select("id, channel_id, platform, task_id, upstream_task_identity")
		if hasPrivateData {
			query = query.Select("id, channel_id, platform, task_id, private_data, upstream_task_identity")
		}
		var rows []taskIdentityBackfillRow
		if err := query.Where("id > ?", lastID).Order("id").Limit(taskIdentityBackfillBatchSize).Find(&rows).Error; err != nil {
			return fmt.Errorf("read legacy task identities: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if row.ID > lastID {
				lastID = row.ID
			}
			if row.UpstreamTaskIdentity.Valid && strings.TrimSpace(row.UpstreamTaskIdentity.String) != "" {
				continue
			}
			source := strings.TrimSpace(row.PrivateData.UpstreamTaskID)
			if source == "" {
				source = strings.TrimSpace(row.TaskID)
			}
			digest, ok := TaskUpstreamIdentityDigest(source)
			if !ok {
				continue
			}
			// The NULL/empty predicate makes the write idempotent and avoids
			// overwriting an identity published by another startup process.
			result := db.Table("tasks").Where("id = ? AND (upstream_task_identity IS NULL OR TRIM(upstream_task_identity) = '')", row.ID).
				Update(taskUpstreamIdentityColumnName, digest)
			if result.Error != nil {
				return fmt.Errorf("backfill task identity id=%d: %w", row.ID, result.Error)
			}
		}
	}
}

func ensureTaskUpstreamIdentityIndex(db *gorm.DB, dbType common.DatabaseType) error {
	exists, unique, columnsMatch, err := taskUpstreamIdentityIndexState(db, dbType)
	if err != nil {
		return err
	}
	if exists {
		if !unique {
			return fmt.Errorf("tasks index %s exists but is not unique; repair it before startup", taskUpstreamIdentityIndexName)
		}
		if !columnsMatch {
			return fmt.Errorf("tasks index %s does not cover (channel_id, platform, %s); repair it before startup", taskUpstreamIdentityIndexName, taskUpstreamIdentityColumnName)
		}
		return nil
	}
	if err := validateTaskUpstreamIdentityRows(db); err != nil {
		return err
	}
	if err := db.Migrator().CreateIndex(&Task{}, taskUpstreamIdentityIndexName); err != nil {
		existsAfterError, uniqueAfterError, columnsMatchAfterError, inspectErr := taskUpstreamIdentityIndexState(db, dbType)
		if inspectErr == nil && existsAfterError && uniqueAfterError && columnsMatchAfterError {
			return nil
		}
		return fmt.Errorf("create unique tasks index %s: %w", taskUpstreamIdentityIndexName, err)
	}
	existsAfterCreate, uniqueAfterCreate, columnsMatchAfterCreate, err := taskUpstreamIdentityIndexState(db, dbType)
	if err != nil {
		return err
	}
	if !existsAfterCreate || !uniqueAfterCreate || !columnsMatchAfterCreate {
		return fmt.Errorf("tasks index %s was created with unsafe definition; repair it before startup", taskUpstreamIdentityIndexName)
	}
	return nil
}

func validateTaskUpstreamIdentityRows(db *gorm.DB) error {
	var duplicate struct {
		ChannelID      int
		Platform       string
		Identity       string
		DuplicateCount int64 `gorm:"column:duplicate_count"`
	}
	result := db.Table("tasks").
		Select("channel_id, platform, upstream_task_identity, COUNT(*) AS duplicate_count").
		Where("upstream_task_identity IS NOT NULL AND TRIM(upstream_task_identity) <> ''").
		Group("channel_id, platform, upstream_task_identity").
		Having("COUNT(*) > ?", 1).
		Limit(1).
		Scan(&duplicate)
	if result.Error != nil {
		return fmt.Errorf("validate duplicate task upstream identities: %w", result.Error)
	}
	if result.RowsAffected > 0 {
		return fmt.Errorf("tasks contain duplicate upstream identity channel=%d platform=%q (%d rows); reconcile before creating unique index", duplicate.ChannelID, duplicate.Platform, duplicate.DuplicateCount)
	}
	return nil
}

func taskUpstreamIdentityIndexState(db *gorm.DB, dbType common.DatabaseType) (exists bool, unique bool, columnsMatch bool, err error) {
	expected := []string{"channel_id", "platform", taskUpstreamIdentityColumnName}
	switch dbType {
	case common.DatabaseTypeSQLite:
		var listRow struct {
			Unique  int `gorm:"column:is_unique"`
			Partial int `gorm:"column:partial"`
		}
		result := db.Raw(`SELECT "unique" AS is_unique, partial
			FROM pragma_index_list(?) WHERE name = ?`, "tasks", taskUpstreamIdentityIndexName).Scan(&listRow)
		if result.Error != nil {
			return false, false, false, fmt.Errorf("inspect SQLite tasks identity index: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return false, false, false, nil
		}
		if listRow.Partial != 0 {
			return true, listRow.Unique != 0, false, nil
		}
		var rows []struct {
			SeqNo int            `gorm:"column:seqno"`
			CID   int            `gorm:"column:cid"`
			Name  sql.NullString `gorm:"column:name"`
		}
		if queryErr := db.Raw("SELECT seqno, cid, name FROM pragma_index_info(?) ORDER BY seqno", taskUpstreamIdentityIndexName).Scan(&rows).Error; queryErr != nil {
			return false, false, false, fmt.Errorf("inspect SQLite tasks identity index columns: %w", queryErr)
		}
		columns := make([]string, 0, len(rows))
		for position, row := range rows {
			if row.SeqNo != position || row.CID < 0 || !row.Name.Valid {
				return true, listRow.Unique != 0, false, nil
			}
			columns = append(columns, strings.ToLower(strings.TrimSpace(row.Name.String)))
		}
		return true, listRow.Unique != 0, equalPaymentIndexColumns(columns, expected), nil

	case common.DatabaseTypeMySQL:
		var rows []struct {
			NonUnique int            `gorm:"column:non_unique"`
			Column    sql.NullString `gorm:"column:column_name"`
			Seq       int            `gorm:"column:seq_in_index"`
			SubPart   sql.NullInt64  `gorm:"column:sub_part"`
		}
		result := db.Raw(`SELECT NON_UNIQUE AS non_unique, COLUMN_NAME AS column_name,
			SEQ_IN_INDEX AS seq_in_index, SUB_PART AS sub_part
			FROM information_schema.statistics
			WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?
			ORDER BY SEQ_IN_INDEX`, "tasks", taskUpstreamIdentityIndexName).Scan(&rows)
		if result.Error != nil {
			return false, false, false, fmt.Errorf("inspect MySQL tasks identity index: %w", result.Error)
		}
		if len(rows) == 0 {
			return false, false, false, nil
		}
		columns := make([]string, 0, len(rows))
		for position, row := range rows {
			if row.SubPart.Valid || !row.Column.Valid || row.Seq != position+1 {
				return true, rows[0].NonUnique == 0, false, nil
			}
			columns = append(columns, strings.ToLower(strings.TrimSpace(row.Column.String)))
		}
		return true, rows[0].NonUnique == 0, equalPaymentIndexColumns(columns, expected), nil

	case common.DatabaseTypePostgreSQL:
		var rows []struct {
			Unique  bool           `gorm:"column:is_unique"`
			Ordinal int            `gorm:"column:ordinality"`
			Column  sql.NullString `gorm:"column:column_name"`
		}
		result := db.Raw(`SELECT i.indisunique AS is_unique, k.ordinality,
			a.attname AS column_name
			FROM pg_class t
			JOIN pg_namespace n ON n.oid = t.relnamespace
			JOIN pg_index i ON i.indrelid = t.oid
			JOIN pg_class idx ON idx.oid = i.indexrelid
			JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ordinality) ON TRUE
			LEFT JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = k.attnum
			WHERE n.nspname = current_schema() AND t.relname = ? AND idx.relname = ?
			ORDER BY k.ordinality`, "tasks", taskUpstreamIdentityIndexName).Scan(&rows)
		if result.Error != nil {
			return false, false, false, fmt.Errorf("inspect PostgreSQL tasks identity index: %w", result.Error)
		}
		if len(rows) == 0 {
			return false, false, false, nil
		}
		columns := make([]string, 0, len(rows))
		for position, row := range rows {
			if row.Ordinal != position+1 || !row.Column.Valid {
				return true, rows[0].Unique, false, nil
			}
			columns = append(columns, strings.ToLower(strings.TrimSpace(row.Column.String)))
		}
		return true, rows[0].Unique, equalPaymentIndexColumns(columns, expected), nil
	default:
		return false, false, false, fmt.Errorf("unsupported task identity database type %q", dbType)
	}
}

func quoteTaskIdentityIdentifier(identifier string, dbType common.DatabaseType) string {
	if dbType == common.DatabaseTypePostgreSQL {
		return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
	}
	return "`" + strings.ReplaceAll(identifier, "`", "``") + "`"
}

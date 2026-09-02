package model

import (
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"
)

const (
	// logOrderingIndexName is declared on Log with created_at as the leading
	// column. The old idx_created_at_id name was observed in production with
	// the columns reversed, so it could not satisfy the admin list ordering.
	logOrderingIndexName       = "idx_logs_created_at_id"
	legacyLogOrderingIndexName = "idx_created_at_id"
)

var expectedLogOrderingIndexColumns = []string{"created_at", "id"}

// inspectLogIndexColumns returns the columns in an index's declared order.
// GORM's HasIndex only checks the name, which is insufficient when a manual
// or interrupted migration leaves the expected name attached to an id-first
// index. SQLite's migrator does not implement GetIndexes, so use its portable
// PRAGMA there; MySQL and PostgreSQL expose the same information through the
// GORM migrator. The bool reports whether the dialect was inspected, allowing
// unknown drivers to retain the historical name-only fallback.
func inspectLogIndexColumns(db *gorm.DB, indexName string) ([]string, bool, error) {
	if db == nil || db.Dialector == nil {
		return nil, false, nil
	}
	switch db.Dialector.Name() {
	case "sqlite":
		// indexName is an internal constant, but escape it defensively because
		// SQLite PRAGMA does not accept bind parameters for identifiers.
		escapedName := strings.ReplaceAll(indexName, "'", "''")
		var rows []struct {
			Seq  int    `gorm:"column:seq"`
			Name string `gorm:"column:name"`
		}
		if err := db.Raw("PRAGMA index_info('" + escapedName + "')").Scan(&rows).Error; err != nil {
			return nil, true, err
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].Seq < rows[j].Seq })
		columns := make([]string, 0, len(rows))
		for _, row := range rows {
			columns = append(columns, row.Name)
		}
		return columns, true, nil
	case "mysql", "postgres":
		indexes, err := db.Migrator().GetIndexes(&Log{})
		if err != nil {
			return nil, true, err
		}
		for _, index := range indexes {
			if index.Name() == indexName || strings.EqualFold(index.Name(), indexName) {
				return index.Columns(), true, nil
			}
		}
		return nil, true, nil
	default:
		return nil, false, nil
	}
}

func logOrderingIndexMatches(columns []string) bool {
	if len(columns) != len(expectedLogOrderingIndexColumns) {
		return false
	}
	for i, column := range columns {
		if !strings.EqualFold(strings.Trim(column, " `\""), expectedLogOrderingIndexColumns[i]) {
			return false
		}
	}
	return true
}

// ensureLogOrderingIndex installs the corrected composite index and removes
// the legacy index after the replacement exists. It is intentionally limited
// to SQL log stores; ClickHouse uses the table's MergeTree ORDER BY instead.
// The operation is index-only and therefore does not rewrite or delete log
// rows. A concurrent startup may race while creating the replacement; the
// second creator rechecks the catalog before returning the error.
func ensureLogOrderingIndex(db *gorm.DB) error {
	if db == nil || db.Dialector == nil || db.Dialector.Name() == "clickhouse" {
		return nil
	}
	if !db.Migrator().HasTable(&Log{}) {
		return nil
	}

	migrator := db.Migrator()
	hasOrderingIndex := migrator.HasIndex(&Log{}, logOrderingIndexName)
	orderingIndexCorrect := false
	if hasOrderingIndex {
		columns, inspected, err := inspectLogIndexColumns(db, logOrderingIndexName)
		if err != nil {
			return fmt.Errorf("inspect log ordering index: %w", err)
		}
		// Unknown dialects retain the previous name-only behavior. All SQL
		// dialects supported by this project are inspected above.
		orderingIndexCorrect = !inspected || logOrderingIndexMatches(columns)
	}
	if !orderingIndexCorrect {
		if hasOrderingIndex {
			if err := migrator.DropIndex(&Log{}, logOrderingIndexName); err != nil && migrator.HasIndex(&Log{}, logOrderingIndexName) {
				return fmt.Errorf("drop drifted log ordering index: %w", err)
			}
		}
		if err := migrator.CreateIndex(&Log{}, logOrderingIndexName); err != nil && !migrator.HasIndex(&Log{}, logOrderingIndexName) {
			return fmt.Errorf("create log ordering index: %w", err)
		}
	}

	// Once the replacement is present, retire the old index. Keeping both
	// indexes would waste write/storage budget and can make the planner choose
	// the wrong (id-first) index. If a legacy database has no old index this is
	// a no-op.
	if migrator.HasIndex(&Log{}, legacyLogOrderingIndexName) {
		if err := migrator.DropIndex(&Log{}, legacyLogOrderingIndexName); err != nil && migrator.HasIndex(&Log{}, legacyLogOrderingIndexName) {
			return fmt.Errorf("drop legacy log ordering index: %w", err)
		}
	}
	return nil
}

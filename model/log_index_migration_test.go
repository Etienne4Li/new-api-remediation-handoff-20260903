package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEnsureLogOrderingIndexReplacesLegacyIdFirstIndex(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Log{}))
	// Simulate the production drift: an older deployment created the same
	// logical ordering index with id as the leading column.
	require.NoError(t, db.Exec("CREATE INDEX idx_created_at_id ON logs (id, created_at)").Error)

	require.NoError(t, ensureLogOrderingIndex(db))
	assert.False(t, db.Migrator().HasIndex(&Log{}, legacyLogOrderingIndexName))
	assert.True(t, db.Migrator().HasIndex(&Log{}, logOrderingIndexName))

	var columns []struct {
		Seq  int    `gorm:"column:seq"`
		Name string `gorm:"column:name"`
	}
	require.NoError(t, db.Raw("PRAGMA index_info('idx_logs_created_at_id')").Scan(&columns).Error)
	require.Len(t, columns, 2)
	assert.Equal(t, "created_at", columns[0].Name)
	assert.Equal(t, "id", columns[1].Name)
}

func TestEnsureLogOrderingIndexRebuildsSameNameWhenColumnOrderDrifts(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Log{}))
	require.NoError(t, db.Migrator().DropIndex(&Log{}, logOrderingIndexName))
	// A deployment can retain the expected name while the index definition is
	// wrong (for example, an interrupted/manual migration). Name-only checks
	// incorrectly treat this as healthy.
	require.NoError(t, db.Exec("CREATE INDEX idx_logs_created_at_id ON logs (id, created_at)").Error)

	require.NoError(t, ensureLogOrderingIndex(db))

	var columns []struct {
		Seq  int    `gorm:"column:seq"`
		Name string `gorm:"column:name"`
	}
	require.NoError(t, db.Raw("PRAGMA index_info('idx_logs_created_at_id')").Scan(&columns).Error)
	require.Len(t, columns, 2)
	assert.Equal(t, "created_at", columns[0].Name)
	assert.Equal(t, "id", columns[1].Name)
}

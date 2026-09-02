package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestConfigureSQLPoolBoundsConnectionSettings(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	t.Setenv("SQL_MAX_IDLE_CONNS", "999999999999")
	t.Setenv("SQL_MAX_OPEN_CONNS", "0")
	t.Setenv("SQL_MAX_LIFETIME", "9223372036854775807")
	configureSQLPool(sqlDB)

	stats := sqlDB.Stats()
	require.Equal(t, defaultSQLMaxOpenConns, stats.MaxOpenConnections)

	// A valid idle/open pair is retained, while an idle count larger than the
	// open count is clamped to the latter.
	t.Setenv("SQL_MAX_IDLE_CONNS", "8")
	t.Setenv("SQL_MAX_OPEN_CONNS", "4")
	t.Setenv("SQL_MAX_LIFETIME", "0")
	configureSQLPool(sqlDB)
	require.Equal(t, 4, sqlDB.Stats().MaxOpenConnections)
}

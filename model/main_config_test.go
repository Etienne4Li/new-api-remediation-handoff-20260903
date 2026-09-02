package model

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func TestChooseDBReadsDSNFromFile(t *testing.T) {
	originalSQLitePath := common.SQLitePath
	t.Cleanup(func() { common.SQLitePath = originalSQLitePath })
	common.SQLitePath = filepath.Join(t.TempDir(), "new-api.db")
	t.Setenv("SQL_DSN", "")
	dsnFile := filepath.Join(t.TempDir(), "sql_dsn")
	require.NoError(t, os.WriteFile(dsnFile, []byte("local\n"), 0600))
	t.Setenv("SQL_DSN_FILE", dsnFile)

	db, dbType, err := chooseDB("SQL_DSN", false)
	require.NoError(t, err)
	require.Equal(t, common.DatabaseTypeSQLite, dbType)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
}

package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCountPendingQuotaCacheRepairsPropagatesDatabaseError(t *testing.T) {
	originalDB := DB
	brokenDB, err := gorm.Open(sqlite.Open("file:quota-cache-repair-count-closed?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := brokenDB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	DB = brokenDB
	t.Cleanup(func() { DB = originalDB })

	_, err = countPendingQuotaCacheRepairs()
	require.Error(t, err)
}

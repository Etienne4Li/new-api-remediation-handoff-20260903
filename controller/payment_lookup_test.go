package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLookupCreemOrderPreservesDatabaseFailure(t *testing.T) {
	originalDB := model.DB
	brokenDB, err := gorm.Open(sqlite.Open("file:controller-creem-lookup-closed?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := brokenDB.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	model.DB = brokenDB
	t.Cleanup(func() { model.DB = originalDB })

	_, _, err = lookupCreemOrder("creem-db-error")
	require.Error(t, err)
	require.NotErrorIs(t, err, model.ErrTopUpNotFound)
	require.NotErrorIs(t, err, model.ErrSubscriptionOrderNotFound)
}

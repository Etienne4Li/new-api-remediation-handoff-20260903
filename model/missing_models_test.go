package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestGetMissingModelsPreservesAbilityQueryFailure(t *testing.T) {
	originalDB := DB
	db, err := gorm.Open(sqlite.Open("file:missing-models-closed?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	DB = db
	t.Cleanup(func() { DB = originalDB })

	_, err = GetMissingModels()
	require.Error(t, err)
}

func TestAbilityRoutingQueriesFailClosedWithoutDatabase(t *testing.T) {
	originalDB := DB
	DB = nil
	t.Cleanup(func() { DB = originalDB })

	_, err := GetEnabledModelsWithError()
	require.ErrorIs(t, err, ErrDatabase)
	_, err = GetGroupEnabledModelsWithError("default")
	require.ErrorIs(t, err, ErrDatabase)
	channel, err := GetChannel("default", "gpt-test", 0, "/v1/responses", nil)
	require.ErrorIs(t, err, ErrDatabase)
	assert.Nil(t, channel)

	previousMemoryCache := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = previousMemoryCache })
	channel, err = GetRandomSatisfiedChannel("default", "gpt-test", 0, "/v1/responses", nil)
	require.ErrorIs(t, err, ErrDatabase)
	assert.Nil(t, channel)
}

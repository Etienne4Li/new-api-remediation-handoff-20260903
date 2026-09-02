package model

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDeleteOldLogBatchIsBoundedAndPortable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Log{}))

	previousLogDB := LOG_DB
	previousType := common.LogDatabaseType()
	LOG_DB = db
	common.SetLogDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		LOG_DB = previousLogDB
		common.SetLogDatabaseType(previousType)
	})

	rows := []Log{
		{CreatedAt: 1, Type: LogTypeConsume},
		{CreatedAt: 2, Type: LogTypeConsume},
		{CreatedAt: 3, Type: LogTypeConsume},
		{CreatedAt: 10, Type: LogTypeConsume},
	}
	require.NoError(t, db.Create(&rows).Error)

	deleted, err := DeleteOldLogBatch(context.Background(), 10, 2)
	require.NoError(t, err)
	assert.EqualValues(t, 2, deleted)

	var remaining []Log
	require.NoError(t, db.Order("created_at asc").Find(&remaining).Error)
	require.Len(t, remaining, 2)
	assert.EqualValues(t, 3, remaining[0].CreatedAt)
	assert.EqualValues(t, 10, remaining[1].CreatedAt)
}

func TestDeleteOldLogBatchRejectsNonPositiveCutoff(t *testing.T) {
	previousLogDB := LOG_DB
	LOG_DB = nil
	t.Cleanup(func() { LOG_DB = previousLogDB })

	deleted, err := DeleteOldLogBatch(context.Background(), 0, 10)
	assert.Zero(t, deleted)
	assert.Error(t, err)
}

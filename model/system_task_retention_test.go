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

func TestDeleteOldSystemTaskBatchOnlyDeletesTerminalRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&SystemTask{}, &SystemTaskLock{}))

	previousDB := DB
	previousType := common.MainDatabaseType()
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
	})

	rows := []SystemTask{
		{TaskID: "old-success-1", Type: "test", Status: SystemTaskStatusSucceeded, UpdatedAt: 1},
		{TaskID: "old-failed-2", Type: "test", Status: SystemTaskStatusFailed, UpdatedAt: 2},
		{TaskID: "old-success-3", Type: "test", Status: SystemTaskStatusSucceeded, UpdatedAt: 3},
		{TaskID: "pending-4", Type: "test", Status: SystemTaskStatusPending, UpdatedAt: 1},
		{TaskID: "running-5", Type: "test", Status: SystemTaskStatusRunning, UpdatedAt: 1},
		{TaskID: "new-success-6", Type: "test", Status: SystemTaskStatusSucceeded, UpdatedAt: 100},
	}
	require.NoError(t, db.Create(&rows).Error)
	require.NoError(t, db.Create(&SystemTaskLock{Type: "test", TaskID: "old-success-1", LockedBy: "legacy"}).Error)

	deleted, err := DeleteOldSystemTaskBatch(context.Background(), 10, 2)
	require.NoError(t, err)
	assert.EqualValues(t, 2, deleted)

	var remaining []SystemTask
	require.NoError(t, db.Order("id asc").Find(&remaining).Error)
	remainingIDs := make([]string, 0, len(remaining))
	for _, task := range remaining {
		remainingIDs = append(remainingIDs, task.TaskID)
	}
	assert.NotContains(t, remainingIDs, "old-success-1")
	assert.NotContains(t, remainingIDs, "old-failed-2")
	assert.Contains(t, remainingIDs, "old-success-3")
	assert.Contains(t, remainingIDs, "pending-4")
	assert.Contains(t, remainingIDs, "running-5")
	assert.Contains(t, remainingIDs, "new-success-6")

	var lockCount int64
	require.NoError(t, db.Model(&SystemTaskLock{}).Where("task_id = ?", "old-success-1").Count(&lockCount).Error)
	assert.Zero(t, lockCount, "locks for removed terminal tasks must not be orphaned")
}

func TestDeleteOldSystemTaskBatchRechecksStatusBeforeDelete(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&SystemTask{}, &SystemTaskLock{}))

	previousDB := DB
	previousType := common.MainDatabaseType()
	DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
	})

	task := SystemTask{TaskID: "terminal-became-running", Type: "test", Status: SystemTaskStatusSucceeded, UpdatedAt: 1}
	require.NoError(t, db.Create(&task).Error)
	require.NoError(t, db.Create(&SystemTaskLock{
		Type: "test", TaskID: task.TaskID, LockedBy: "new-runner", LockedUntil: 100,
	}).Error)

	callbackName := "test:requeue-system-task-before-retention-delete"
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "system_tasks" {
			return
		}
		if err := tx.Session(&gorm.Session{NewDB: true, SkipHooks: true}).Model(&SystemTask{}).
			Where("id = ?", task.ID).
			Updates(map[string]any{
				"status":     SystemTaskStatusRunning,
				"updated_at": int64(50),
			}).Error; err != nil {
			tx.AddError(err)
		}
	}))
	t.Cleanup(func() { db.Callback().Delete().Remove(callbackName) })

	deleted, err := DeleteOldSystemTaskBatch(context.Background(), 10, 1)
	require.NoError(t, err)
	assert.Zero(t, deleted)

	var reloaded SystemTask
	require.NoError(t, db.Where("id = ?", task.ID).Take(&reloaded).Error)
	assert.Equal(t, SystemTaskStatusRunning, reloaded.Status)
	var lockCount int64
	require.NoError(t, db.Model(&SystemTaskLock{}).Where("task_id = ?", task.TaskID).Count(&lockCount).Error)
	assert.EqualValues(t, 1, lockCount, "a surviving active task must keep its lease")
}

func TestDeleteOldSystemTaskBatchRejectsInvalidCutoff(t *testing.T) {
	previousDB := DB
	DB = nil
	t.Cleanup(func() { DB = previousDB })

	deleted, err := DeleteOldSystemTaskBatch(context.Background(), 0, 10)
	assert.Zero(t, deleted)
	assert.Error(t, err)
}

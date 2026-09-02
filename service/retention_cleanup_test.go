package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetentionCleanupHandlerIsBoundedAndPreservesActiveRows(t *testing.T) {
	// Keep this fixture isolated from any rows left by another service test.
	require.NoError(t, model.DB.Exec("DELETE FROM logs").Error)
	require.NoError(t, model.DB.Exec("DELETE FROM system_task_locks").Error)
	require.NoError(t, model.DB.Exec("DELETE FROM system_tasks").Error)

	t.Setenv("RETENTION_CLEANUP_ENABLED", "true")
	t.Setenv("LOG_RETENTION_DAYS", "1")
	t.Setenv("SYSTEM_TASK_RETENTION_DAYS", "1")
	t.Setenv("RETENTION_CLEANUP_BATCH_SIZE", "1")
	t.Setenv("RETENTION_CLEANUP_MAX_BATCHES", "1")

	oldTimestamp := common.GetTimestamp() - 2*24*60*60
	recentTimestamp := common.GetTimestamp()
	require.NoError(t, model.LOG_DB.Create(&model.Log{CreatedAt: oldTimestamp, Type: model.LogTypeSystem}).Error)
	require.NoError(t, model.LOG_DB.Create(&model.Log{CreatedAt: oldTimestamp + 1, Type: model.LogTypeSystem}).Error)
	require.NoError(t, model.LOG_DB.Create(&model.Log{CreatedAt: recentTimestamp, Type: model.LogTypeSystem}).Error)

	oldTask, err := model.CreateSystemTask("retention_fixture", nil, nil)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.SystemTask{}).Where("id = ?", oldTask.ID).Updates(map[string]any{
		"status":     model.SystemTaskStatusSucceeded,
		"active_key": nil,
		"updated_at": oldTimestamp,
	}).Error)
	oldTask2, err := model.CreateSystemTask("retention_fixture", nil, nil)
	require.NoError(t, err)
	require.NoError(t, model.DB.Model(&model.SystemTask{}).Where("id = ?", oldTask2.ID).Updates(map[string]any{
		"status":     model.SystemTaskStatusFailed,
		"active_key": nil,
		"updated_at": oldTimestamp + 1,
	}).Error)
	pendingTask, err := model.CreateSystemTask("retention_fixture", nil, nil)
	require.NoError(t, err)

	cleanupTask, err := model.CreateSystemTask(model.SystemTaskTypeRetentionCleanup, nil, nil)
	require.NoError(t, err)
	claimed, ok, err := model.ClaimSystemTask(cleanupTask.ID, cleanupTask.Type, "retention-test", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, ok)

	retentionCleanupHandler{}.Run(context.Background(), claimed, "retention-test")

	reloaded, err := model.GetSystemTaskByTaskID(cleanupTask.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, model.SystemTaskStatusSucceeded, reloaded.Status)
	var result retentionCleanupResult
	require.NoError(t, common.UnmarshalJsonStr(reloaded.Result, &result))
	assert.True(t, result.Truncated)
	assert.EqualValues(t, 1, result.DeletedLogs)
	assert.EqualValues(t, 1, result.DeletedSystemTasks)

	var oldLogCount int64
	require.NoError(t, model.LOG_DB.Model(&model.Log{}).Where("created_at < ?", recentTimestamp).Count(&oldLogCount).Error)
	assert.EqualValues(t, 1, oldLogCount, "one old log remains for the next bounded pass")
	var pendingCount int64
	require.NoError(t, model.DB.Model(&model.SystemTask{}).Where("task_id = ? AND status = ?", pendingTask.TaskID, model.SystemTaskStatusPending).Count(&pendingCount).Error)
	assert.EqualValues(t, 1, pendingCount, "pending tasks must never be removed")
}

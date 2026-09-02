package model

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

const (
	defaultSystemTaskRetentionBatchSize = 100
	maxSystemTaskRetentionBatchSize     = 1000
)

// DeleteOldSystemTaskBatch removes at most limit terminal system-task rows
// older than cutoff. Pending and running rows are never touched. The query is
// deliberately expressed as a bounded primary-key probe followed by a
// portable IN delete: PostgreSQL does not support LIMIT directly on DELETE,
// while MySQL and SQLite do.
func DeleteOldSystemTaskBatch(ctx context.Context, cutoff int64, limit int) (int64, error) {
	if DB == nil {
		return 0, errors.New("database is not initialized")
	}
	if cutoff <= 0 {
		return 0, errors.New("cutoff must be positive")
	}
	if limit <= 0 {
		limit = defaultSystemTaskRetentionBatchSize
	}
	if limit > maxSystemTaskRetentionBatchSize {
		limit = maxSystemTaskRetentionBatchSize
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	terminalStatuses := []SystemTaskStatus{SystemTaskStatusSucceeded, SystemTaskStatusFailed}
	var deleted int64
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		type taskIdentity struct {
			ID     int64
			TaskID string
		}
		var selected []taskIdentity
		if err := lockForUpdate(tx).
			Model(&SystemTask{}).
			Where("status IN ? AND updated_at < ?", terminalStatuses, cutoff).
			Order("updated_at asc, id asc").
			Limit(limit).
			Select("id", "task_id").
			Find(&selected).Error; err != nil {
			return err
		}
		if len(selected) == 0 {
			return nil
		}

		ids := make([]int64, 0, len(selected))
		taskIDs := make([]string, 0, len(selected))
		for _, task := range selected {
			ids = append(ids, task.ID)
			if task.TaskID != "" {
				taskIDs = append(taskIDs, task.TaskID)
			}
		}

		// Recheck both terminal status and age in the DELETE itself. The row lock
		// serializes normal writers on MySQL/PostgreSQL, while this predicate is
		// the final safety fence for SQLite and for any writer that selected the
		// row before this transaction acquired its lock.
		result := tx.Where("id IN ? AND status IN ? AND updated_at < ?", ids, terminalStatuses, cutoff).
			Delete(&SystemTask{})
		if result.Error != nil {
			return result.Error
		}
		deleted = result.RowsAffected

		if len(taskIDs) == 0 {
			return nil
		}
		// Delete a lease only after its task row was actually removed. A task
		// that became pending/running after the probe remains present and keeps
		// its lock. Computing this set inside the transaction also avoids leaving
		// a task deletion committed when lock cleanup fails.
		var survivingTaskIDs []string
		if err := tx.Model(&SystemTask{}).Where("task_id IN ?", taskIDs).
			Pluck("task_id", &survivingTaskIDs).Error; err != nil {
			return err
		}
		surviving := make(map[string]struct{}, len(survivingTaskIDs))
		for _, taskID := range survivingTaskIDs {
			surviving[taskID] = struct{}{}
		}
		orphanedTaskIDs := make([]string, 0, len(taskIDs))
		for _, taskID := range taskIDs {
			if _, ok := surviving[taskID]; !ok {
				orphanedTaskIDs = append(orphanedTaskIDs, taskID)
			}
		}
		if len(orphanedTaskIDs) == 0 {
			return nil
		}
		if err := tx.Where("task_id IN ?", orphanedTaskIDs).Delete(&SystemTaskLock{}).Error; err != nil {
			return fmt.Errorf("delete orphaned system task locks: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

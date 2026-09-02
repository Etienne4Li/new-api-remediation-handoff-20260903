package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
)

const (
	defaultRetentionDays       = 30
	maxRetentionDays           = 3650
	defaultRetentionInterval   = 24
	maxRetentionIntervalHours  = 24 * 7
	defaultRetentionBatchSize  = 100
	maxRetentionBatchSize      = 1000
	defaultRetentionMaxBatches = 100
	maxRetentionMaxBatches     = 1000
)

// retentionCleanupPayload is persisted with each scheduled run so a policy
// change cannot alter the meaning of a task that is already pending.
type retentionCleanupPayload struct {
	LogRetentionDays        int `json:"log_retention_days"`
	SystemTaskRetentionDays int `json:"system_task_retention_days"`
	BatchSize               int `json:"batch_size"`
	MaxBatches              int `json:"max_batches"`
}

type retentionCleanupResult struct {
	DeletedLogs        int64 `json:"deleted_logs"`
	DeletedSystemTasks int64 `json:"deleted_system_tasks"`
	Batches            int   `json:"batches"`
	Truncated          bool  `json:"truncated"`
}

type retentionCleanupHandler struct{}

func (retentionCleanupHandler) Type() string { return model.SystemTaskTypeRetentionCleanup }

func (retentionCleanupHandler) Enabled() bool {
	return common.GetEnvOrDefaultBool("RETENTION_CLEANUP_ENABLED", false)
}

func (retentionCleanupHandler) Interval() time.Duration {
	hours := common.GetEnvOrDefaultBounded(
		"RETENTION_CLEANUP_INTERVAL_HOURS",
		defaultRetentionInterval,
		1,
		maxRetentionIntervalHours,
	)
	return time.Duration(hours) * time.Hour
}

func (retentionCleanupHandler) NewPayload() any {
	return retentionCleanupPayload{
		LogRetentionDays:        retentionDays("LOG_RETENTION_DAYS"),
		SystemTaskRetentionDays: retentionDays("SYSTEM_TASK_RETENTION_DAYS"),
		BatchSize:               common.GetEnvOrDefaultBounded("RETENTION_CLEANUP_BATCH_SIZE", defaultRetentionBatchSize, 1, maxRetentionBatchSize),
		MaxBatches:              common.GetEnvOrDefaultBounded("RETENTION_CLEANUP_MAX_BATCHES", defaultRetentionMaxBatches, 1, maxRetentionMaxBatches),
	}
}

func init() {
	RegisterSystemTaskHandler(retentionCleanupHandler{})
}

func retentionDays(envName string) int {
	return common.GetEnvOrDefaultBounded(envName, defaultRetentionDays, 1, maxRetentionDays)
}

func (retentionCleanupHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	payload := retentionCleanupPayload{}
	if err := task.DecodePayload(&payload); err != nil {
		failSystemTask(task, runnerID, err)
		return
	}
	if payload.LogRetentionDays <= 0 {
		payload.LogRetentionDays = retentionDays("LOG_RETENTION_DAYS")
	}
	if payload.SystemTaskRetentionDays <= 0 {
		payload.SystemTaskRetentionDays = retentionDays("SYSTEM_TASK_RETENTION_DAYS")
	}
	if payload.BatchSize <= 0 || payload.BatchSize > maxRetentionBatchSize {
		payload.BatchSize = common.GetEnvOrDefaultBounded("RETENTION_CLEANUP_BATCH_SIZE", defaultRetentionBatchSize, 1, maxRetentionBatchSize)
	}
	if payload.MaxBatches <= 0 || payload.MaxBatches > maxRetentionMaxBatches {
		payload.MaxBatches = common.GetEnvOrDefaultBounded("RETENTION_CLEANUP_MAX_BATCHES", defaultRetentionMaxBatches, 1, maxRetentionMaxBatches)
	}
	if payload.LogRetentionDays <= 0 || payload.LogRetentionDays > maxRetentionDays ||
		payload.SystemTaskRetentionDays <= 0 || payload.SystemTaskRetentionDays > maxRetentionDays {
		failSystemTask(task, runnerID, errors.New("retention policy is outside the supported range"))
		return
	}

	now := common.GetTimestamp()
	logCutoff := now - int64(payload.LogRetentionDays)*24*60*60
	taskCutoff := now - int64(payload.SystemTaskRetentionDays)*24*60*60
	result := retentionCleanupResult{}
	exhausted := false
	for batch := 0; batch < payload.MaxBatches; batch++ {
		if err := ctx.Err(); err != nil {
			failSystemTask(task, runnerID, err)
			return
		}
		deletedLogs, err := model.DeleteOldLogBatch(ctx, logCutoff, payload.BatchSize)
		if err != nil {
			failSystemTask(task, runnerID, fmt.Errorf("delete old logs: %w", err))
			return
		}
		deletedTasks, err := model.DeleteOldSystemTaskBatch(ctx, taskCutoff, payload.BatchSize)
		if err != nil {
			failSystemTask(task, runnerID, fmt.Errorf("delete old system tasks: %w", err))
			return
		}
		result.DeletedLogs += deletedLogs
		result.DeletedSystemTasks += deletedTasks
		result.Batches++
		if deletedLogs == 0 && deletedTasks == 0 {
			exhausted = true
			break
		}
	}
	result.Truncated = !exhausted
	logger.LogInfo(ctx, fmt.Sprintf(
		"retention cleanup completed logs=%d system_tasks=%d batches=%d truncated=%t",
		result.DeletedLogs,
		result.DeletedSystemTasks,
		result.Batches,
		result.Truncated,
	))
	if err := model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusSucceeded, result, ""); err != nil {
		logSystemTaskLockError(ctx, task, err)
	}
}

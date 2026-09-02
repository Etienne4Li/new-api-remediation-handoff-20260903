package model

import (
	"errors"
	"strings"

	"gorm.io/gorm"
)

// TaskBillingSettlementBatchLimit bounds the amount of post-submit billing
// work a polling pass performs.  A bounded batch keeps a transient database
// outage from monopolising the async-task worker.
const TaskBillingSettlementBatchLimit = 100

// GetTasksWithPendingBillingSettlementWithError returns rows whose final
// billing delta still needs to be committed. Processing rows are included
// after their lease expires so a worker crash does not strand a task forever.
// Unlike terminal refund reconciliation, reclaiming this lease is safe: the
// actual ledger mutation is fenced by BillingOperation's request/component
// unique key.
func GetTasksWithPendingBillingSettlementWithError(nowUnix int64, limit int) ([]*Task, error) {
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	if limit <= 0 || limit > TaskBillingSettlementBatchLimit {
		limit = TaskBillingSettlementBatchLimit
	}
	var tasks []*Task
	err := DB.Where("billing_settlement_state IN ?", []TaskBillingSettlementState{
		TaskBillingSettlementPending,
		TaskBillingSettlementProcessing,
	}).Where("(billing_settlement_until = 0 OR billing_settlement_until IS NULL OR billing_settlement_until <= ?)", nowUnix).
		Order("id").Limit(limit).Find(&tasks).Error
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

// GetTasksWithPendingBillingSettlement is retained for callers that use the
// historical no-error API. New scheduler/reconciliation code should call the
// error-returning variant above so a database outage cannot look like an idle
// queue.
func GetTasksWithPendingBillingSettlement(nowUnix int64, limit int) []*Task {
	tasks, _ := GetTasksWithPendingBillingSettlementWithError(nowUnix, limit)
	return tasks
}

// HasPendingBillingSettlements is the cheap scheduler probe corresponding to
// GetTasksWithPendingBillingSettlement.
func HasPendingBillingSettlementsWithError(nowUnix int64) (bool, error) {
	if DB == nil {
		return false, errors.New("database is not initialized")
	}
	var id int64
	err := DB.Model(&Task{}).
		Where("billing_settlement_state IN ?", []TaskBillingSettlementState{
			TaskBillingSettlementPending,
			TaskBillingSettlementProcessing,
		}).Where("(billing_settlement_until = 0 OR billing_settlement_until IS NULL OR billing_settlement_until <= ?)", nowUnix).
		Limit(1).Pluck("id", &id).Error
	if err != nil {
		return false, err
	}
	return id != 0, nil
}

// HasPendingBillingSettlements is the compatibility wrapper for legacy
// callers. Scheduler code should use HasPendingBillingSettlementsWithError.
func HasPendingBillingSettlements(nowUnix int64) bool {
	pending, _ := HasPendingBillingSettlementsWithError(nowUnix)
	return pending
}

// ClaimBillingSettlement atomically takes a short lease.  The compare-and-
// swap includes the current state and lease, so overlapping pollers cannot
// both run the post-submit finalisation side effects.
func (t *Task) ClaimBillingSettlement(nowUnix, leaseUntilUnix int64) (bool, error) {
	if t == nil || t.ID <= 0 {
		return false, errors.New("task id is required for billing settlement")
	}
	if DB == nil {
		return false, errors.New("database is not initialized")
	}
	if leaseUntilUnix <= nowUnix {
		return false, errors.New("billing settlement lease must be in the future")
	}
	result := DB.Model(&Task{}).
		Where("id = ?", t.ID).
		Where("billing_settlement_state IN ?", []TaskBillingSettlementState{
			TaskBillingSettlementPending,
			TaskBillingSettlementProcessing,
		}).
		Where("(billing_settlement_until = 0 OR billing_settlement_until IS NULL OR billing_settlement_until <= ?)", nowUnix).
		Updates(map[string]interface{}{
			"billing_settlement_state": TaskBillingSettlementProcessing,
			"billing_settlement_until": leaseUntilUnix,
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	t.BillingSettlementState = TaskBillingSettlementProcessing
	t.BillingSettlementUntil = leaseUntilUnix
	return true, nil
}

// MarkBillingSettlementPending releases a claim after a retryable failure.
// The next-at timestamp provides a small backoff while keeping the operation
// durable across process restarts.  Only pending/processing rows may be
// changed; a concurrent successful worker cannot be moved backwards.
func (t *Task) MarkBillingSettlementPending(nextAt int64, err error) error {
	if t == nil || t.ID <= 0 {
		return errors.New("task id is required for billing settlement")
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}
	message := ""
	if err != nil {
		message = strings.TrimSpace(err.Error())
		// Keep operator metadata bounded.  It is never returned in task DTOs,
		// but an unbounded upstream error must not grow a row indefinitely.
		if len(message) > 2000 {
			message = message[:2000]
		}
	}
	result := DB.Model(&Task{}).
		Where("id = ?", t.ID).
		Where("billing_settlement_state IN ?", []TaskBillingSettlementState{
			TaskBillingSettlementPending,
			TaskBillingSettlementProcessing,
		}).
		Updates(map[string]interface{}{
			"billing_settlement_state": TaskBillingSettlementPending,
			"billing_settlement_until": nextAt,
			"billing_settlement_error": message,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	t.BillingSettlementState = TaskBillingSettlementPending
	t.BillingSettlementUntil = nextAt
	t.BillingSettlementError = message
	return nil
}

// MarkBillingSettlementComplete clears the lease and records that the final
// ledger operation plus informational usage finalisation have completed.  It
// is conditional to protect against a stale worker overwriting a manual fence.
func (t *Task) MarkBillingSettlementComplete(finalQuota ...int) error {
	if t == nil || t.ID <= 0 {
		return errors.New("task id is required for billing settlement")
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}
	updates := map[string]interface{}{
		"billing_settlement_state": TaskBillingSettlementComplete,
		"billing_settlement_until": 0,
		"billing_settlement_error": "",
	}
	if len(finalQuota) > 0 {
		if finalQuota[0] < 0 {
			return errors.New("billing settlement quota cannot be negative")
		}
		updates["quota"] = finalQuota[0]
		t.Quota = finalQuota[0]
	}
	result := DB.Model(&Task{}).
		Where("id = ?", t.ID).
		Where("billing_settlement_state IN ?", []TaskBillingSettlementState{
			TaskBillingSettlementPending,
			TaskBillingSettlementProcessing,
		}).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	t.BillingSettlementState = TaskBillingSettlementComplete
	t.BillingSettlementUntil = 0
	t.BillingSettlementError = ""
	return nil
}

// MarkBillingSettlementManual fences a row whose immutable billing inputs are
// missing (typically a legacy task).  Such rows must be reconciled by an
// operator rather than guessed at by an automatic retry.
func (t *Task) MarkBillingSettlementManual(reason error) error {
	if t == nil || t.ID <= 0 {
		return errors.New("task id is required for billing settlement")
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}
	message := "manual reconciliation required"
	if reason != nil && strings.TrimSpace(reason.Error()) != "" {
		message = strings.TrimSpace(reason.Error())
		if len(message) > 2000 {
			message = message[:2000]
		}
	}
	result := DB.Model(&Task{}).
		Where("id = ?", t.ID).
		Where("billing_settlement_state IN ?", []TaskBillingSettlementState{
			TaskBillingSettlementPending,
			TaskBillingSettlementProcessing,
		}).
		Updates(map[string]interface{}{
			"billing_settlement_state": TaskBillingSettlementManual,
			"billing_settlement_until": 0,
			"billing_settlement_error": message,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	t.BillingSettlementState = TaskBillingSettlementManual
	t.BillingSettlementUntil = 0
	t.BillingSettlementError = message
	return nil
}

// ClaimBillingUsage is an at-most-once fence for aggregate usage/log writes.
// It intentionally marks before invoking those best-effort writes: duplicate
// financial/usage increments are more harmful than a missing informational log
// after an abrupt process exit.  A later manual audit can reconstruct missing
// logs from the task row and BillingOperation marker.
func (t *Task) ClaimBillingUsage() (bool, error) {
	if t == nil || t.ID <= 0 {
		return false, errors.New("task id is required for billing usage")
	}
	if DB == nil {
		return false, errors.New("database is not initialized")
	}
	result := DB.Model(&Task{}).
		// Older rows may predate this column (or have been imported with a
		// NULL value). Treat NULL exactly like the zero value so a schema
		// rollout cannot strand usage accounting forever.
		Where("id = ? AND (billing_usage_recorded = ? OR billing_usage_recorded IS NULL)", t.ID, false).
		Update("billing_usage_recorded", true)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	t.BillingUsageRecorded = true
	return true, nil
}

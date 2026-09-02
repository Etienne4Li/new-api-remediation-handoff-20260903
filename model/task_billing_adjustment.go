package model

import (
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// TaskBillingAdjustmentBatchLimit bounds terminal billing work performed by a
// single polling pass.
const TaskBillingAdjustmentBatchLimit = 100

// EnsureBillingAdjustmentTarget installs the first terminal target when a
// caller observes a successful task that was written by an older code path.
// The empty-state predicate is the one-time fence; an already pending target
// is never overwritten by a later, potentially stale provider response.
func (t *Task) EnsureBillingAdjustmentTarget(target int) (bool, error) {
	if t == nil || t.ID <= 0 {
		return false, errors.New("task id is required for billing adjustment")
	}
	if target < 0 || target > common.MaxQuota {
		return false, errors.New("billing adjustment quota is out of range")
	}
	if DB == nil {
		return false, errors.New("database is not initialized")
	}
	result := DB.Model(&Task{}).
		Where("id = ? AND status = ?", t.ID, TaskStatusSuccess).
		Where("(billing_adjustment_state = '' OR billing_adjustment_state IS NULL)").
		Updates(map[string]interface{}{
			"billing_final_quota":       target,
			"billing_adjustment_reason": t.BillingAdjustmentReason,
			"billing_adjustment_state":  TaskBillingAdjustmentPending,
			"billing_adjustment_until":  0,
			"billing_adjustment_error":  "",
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	t.BillingFinalQuota = target
	t.BillingAdjustmentState = TaskBillingAdjustmentPending
	t.BillingAdjustmentUntil = 0
	t.BillingAdjustmentError = ""
	return true, nil
}

// GetTasksWithPendingBillingAdjustmentWithError returns successful tasks whose
// durable terminal adjustment has not completed. Processing rows are
// reclaimable after the lease because the corresponding ledger mutation is
// fenced by the BillingOperation request/component key.
func GetTasksWithPendingBillingAdjustmentWithError(nowUnix int64, limit int) ([]*Task, error) {
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	if limit <= 0 || limit > TaskBillingAdjustmentBatchLimit {
		limit = TaskBillingAdjustmentBatchLimit
	}
	var tasks []*Task
	err := DB.Where("status = ?", TaskStatusSuccess).
		Where("TRIM(billing_request_id) <> ''").
		Where("(billing_adjustment_state IN ? OR billing_adjustment_state = '' OR billing_adjustment_state IS NULL)", []TaskBillingAdjustmentState{
			TaskBillingAdjustmentPending,
			TaskBillingAdjustmentProcessing,
		}).
		Where("(billing_adjustment_until = 0 OR billing_adjustment_until IS NULL OR billing_adjustment_until <= ?)", nowUnix).
		Order("id").Limit(limit).Find(&tasks).Error
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

// GetTasksWithPendingBillingAdjustment is retained for compatibility. New
// scheduler/reconciliation code should use the error-returning variant so a
// database outage cannot be mistaken for an empty queue.
func GetTasksWithPendingBillingAdjustment(nowUnix int64, limit int) []*Task {
	tasks, _ := GetTasksWithPendingBillingAdjustmentWithError(nowUnix, limit)
	return tasks
}

// HasPendingBillingAdjustments is the scheduler probe corresponding to
// GetTasksWithPendingBillingAdjustment.
func HasPendingBillingAdjustmentsWithError(nowUnix int64) (bool, error) {
	if DB == nil {
		return false, errors.New("database is not initialized")
	}
	var id int64
	err := DB.Model(&Task{}).
		Where("status = ?", TaskStatusSuccess).
		Where("TRIM(billing_request_id) <> ''").
		Where("(billing_adjustment_state IN ? OR billing_adjustment_state = '' OR billing_adjustment_state IS NULL)", []TaskBillingAdjustmentState{
			TaskBillingAdjustmentPending,
			TaskBillingAdjustmentProcessing,
		}).
		Where("(billing_adjustment_until = 0 OR billing_adjustment_until IS NULL OR billing_adjustment_until <= ?)", nowUnix).
		Limit(1).Pluck("id", &id).Error
	if err != nil {
		return false, err
	}
	return id != 0, nil
}

// HasPendingBillingAdjustments is the compatibility wrapper. Scheduler code
// should use HasPendingBillingAdjustmentsWithError.
func HasPendingBillingAdjustments(nowUnix int64) bool {
	pending, _ := HasPendingBillingAdjustmentsWithError(nowUnix)
	return pending
}

// ClaimBillingAdjustment atomically takes a short lease.  The state and lease
// predicates form a compare-and-swap fence for overlapping pollers.
func (t *Task) ClaimBillingAdjustment(nowUnix, leaseUntilUnix int64) (bool, error) {
	if t == nil || t.ID <= 0 {
		return false, errors.New("task id is required for billing adjustment")
	}
	if DB == nil {
		return false, errors.New("database is not initialized")
	}
	if leaseUntilUnix <= nowUnix {
		return false, errors.New("billing adjustment lease must be in the future")
	}
	result := DB.Model(&Task{}).
		Where("id = ? AND status = ?", t.ID, TaskStatusSuccess).
		Where("(billing_adjustment_state IN ? OR billing_adjustment_state = '' OR billing_adjustment_state IS NULL)", []TaskBillingAdjustmentState{
			TaskBillingAdjustmentPending,
			TaskBillingAdjustmentProcessing,
		}).
		Where("(billing_adjustment_until = 0 OR billing_adjustment_until IS NULL OR billing_adjustment_until <= ?)", nowUnix).
		Updates(map[string]interface{}{
			"billing_adjustment_state": TaskBillingAdjustmentProcessing,
			"billing_adjustment_until": leaseUntilUnix,
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	t.BillingAdjustmentState = TaskBillingAdjustmentProcessing
	t.BillingAdjustmentUntil = leaseUntilUnix
	return true, nil
}

// MarkBillingAdjustmentPending releases a retryable claim with a bounded
// operator-facing error.  It is conditional so a concurrent completion cannot
// be moved backwards by a stale worker.
func (t *Task) MarkBillingAdjustmentPending(nextAt int64, err error) error {
	if t == nil || t.ID <= 0 {
		return errors.New("task id is required for billing adjustment")
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}
	message := ""
	if err != nil {
		message = strings.TrimSpace(err.Error())
		if len(message) > 2000 {
			message = message[:2000]
		}
	}
	result := DB.Model(&Task{}).
		Where("id = ? AND status = ?", t.ID, TaskStatusSuccess).
		Where("billing_adjustment_state IN ?", []TaskBillingAdjustmentState{
			TaskBillingAdjustmentPending,
			TaskBillingAdjustmentProcessing,
		}).
		Updates(map[string]interface{}{
			"billing_adjustment_state": TaskBillingAdjustmentPending,
			"billing_adjustment_until": nextAt,
			"billing_adjustment_error": message,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	t.BillingAdjustmentState = TaskBillingAdjustmentPending
	t.BillingAdjustmentUntil = nextAt
	t.BillingAdjustmentError = message
	return nil
}

// MarkBillingAdjustmentComplete records the final task quota and clears the
// lease.  The status/state predicates prevent a stale worker from overwriting
// a manual fence or a newer terminal transition.
func (t *Task) MarkBillingAdjustmentComplete(finalQuota int) error {
	if t == nil || t.ID <= 0 {
		return errors.New("task id is required for billing adjustment")
	}
	if finalQuota < 0 {
		return errors.New("billing adjustment quota cannot be negative")
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}
	result := DB.Model(&Task{}).
		Where("id = ? AND status = ?", t.ID, TaskStatusSuccess).
		Where("billing_adjustment_state IN ?", []TaskBillingAdjustmentState{
			TaskBillingAdjustmentPending,
			TaskBillingAdjustmentProcessing,
		}).
		Updates(map[string]interface{}{
			"quota":                    finalQuota,
			"billing_final_quota":      finalQuota,
			"billing_adjustment_state": TaskBillingAdjustmentComplete,
			"billing_adjustment_until": 0,
			"billing_adjustment_error": "",
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	t.Quota = finalQuota
	t.BillingFinalQuota = finalQuota
	t.BillingAdjustmentState = TaskBillingAdjustmentComplete
	t.BillingAdjustmentUntil = 0
	t.BillingAdjustmentError = ""
	return nil
}

// MarkBillingAdjustmentManual fences an unsafe row for operator review.
func (t *Task) MarkBillingAdjustmentManual(reason error) error {
	if t == nil || t.ID <= 0 {
		return errors.New("task id is required for billing adjustment")
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
		Where("id = ? AND status = ?", t.ID, TaskStatusSuccess).
		Where("billing_adjustment_state IN ?", []TaskBillingAdjustmentState{
			TaskBillingAdjustmentPending,
			TaskBillingAdjustmentProcessing,
		}).
		Updates(map[string]interface{}{
			"billing_adjustment_state": TaskBillingAdjustmentManual,
			"billing_adjustment_until": 0,
			"billing_adjustment_error": message,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	t.BillingAdjustmentState = TaskBillingAdjustmentManual
	t.BillingAdjustmentUntil = 0
	t.BillingAdjustmentError = message
	return nil
}

// ClaimBillingAdjustmentUsage is an at-most-once fence for the aggregate
// usage/log delta emitted after a terminal adjustment.  The financial journal
// remains authoritative; this marker prevents retries from double counting.
func (t *Task) ClaimBillingAdjustmentUsage() (bool, error) {
	if t == nil || t.ID <= 0 {
		return false, errors.New("task id is required for billing adjustment usage")
	}
	if DB == nil {
		return false, errors.New("database is not initialized")
	}
	result := DB.Model(&Task{}).
		// Keep imported/legacy rows with a NULL marker claimable. Without the
		// explicit IS NULL branch SQL's three-valued comparison would make the
		// row look permanently claimed even though no usage was recorded.
		Where("id = ? AND (billing_adjustment_usage_recorded = ? OR billing_adjustment_usage_recorded IS NULL)", t.ID, false).
		Update("billing_adjustment_usage_recorded", true)
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	t.BillingAdjustmentUsageRecorded = true
	return true, nil
}

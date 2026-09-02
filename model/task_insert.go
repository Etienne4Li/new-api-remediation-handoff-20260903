package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrTaskInsertFenceConflict means that a billing request has already been
// associated with a different task identity (or its durable create fence says
// the task was committed but the row cannot be found).  Retrying an upstream
// submission in that situation could create a second provider task, so callers
// must fail closed and reconcile the request instead.
var ErrTaskInsertFenceConflict = errors.New("task insert fence conflicts with an existing task")

const taskInsertBillingComponent = "task_insert"

// InsertWithBillingFence persists an accepted asynchronous task together with
// a durable, request-scoped create marker.  The marker and task row are written
// in one transaction so an ambiguous database error can be retried safely:
//
//   - if both writes committed, the retry observes the marker and loads the
//     existing row instead of inserting a duplicate;
//   - if either write rolled back, the retry creates both again;
//   - a marker committed without a task is treated as a conflict rather than
//     blindly replaying an upstream submission.
//
// Requests created by older callers without BillingRequestId retain the plain
// INSERT behavior for backwards compatibility.  The method mutates t with the
// persisted row when a previous attempt already committed it.
func (t *Task) InsertWithBillingFence() error {
	if t == nil {
		return errors.New("task is nil")
	}
	if t.ID != 0 {
		return errors.New("task id must be zero before insert")
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}

	requestID := strings.TrimSpace(t.BillingRequestId)
	if requestID == "" {
		return DB.Create(t).Error
	}
	// Keep the persisted identity canonical so a retry that trims the request
	// ID resolves the same row rather than treating surrounding whitespace as a
	// missing task.
	t.BillingRequestId = requestID
	spec, err := normalizeBillingOperationSpec(BillingOperationSpec{
		RequestID: requestID,
		Component: taskInsertBillingComponent,
		UserID:    t.UserId,
	})
	if err != nil {
		return fmt.Errorf("task insert fence: %w", err)
	}

	return DB.Transaction(func(tx *gorm.DB) error {
		// A row may have committed even when the original INSERT returned an
		// ambiguous network/connection error. Check it before creating a marker.
		// Locking is important on MySQL/PostgreSQL; SQLite's helper intentionally
		// omits FOR UPDATE because SQLite does not support that syntax.
		existing, err := findTaskForInsertFence(lockForUpdate(tx), t.UserId, requestID)
		if err == nil {
			if !taskInsertMatches(existing, t) {
				return ErrTaskInsertFenceConflict
			}
			if _, err := ensureTaskInsertMarkerTx(tx, spec, existing); err != nil {
				return err
			}
			*t = *existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		marker, err := ensureTaskInsertMarkerTx(tx, spec, nil)
		if err != nil {
			return err
		}
		if marker.Status == BillingOperationApplied {
			existing, err = findTaskForInsertFence(lockForUpdate(tx), t.UserId, requestID)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrTaskInsertFenceConflict
				}
				return err
			}
			if !taskInsertMatches(existing, t) {
				return ErrTaskInsertFenceConflict
			}
			*t = *existing
			return nil
		}
		if marker.Status != BillingOperationPending {
			return fmt.Errorf("%w: unknown marker status %q", ErrTaskInsertFenceConflict, marker.Status)
		}

		if err := tx.Create(t).Error; err != nil {
			return err
		}
		if err := tx.Model(&BillingOperation{}).
			Where("id = ?", marker.Id).
			Updates(map[string]interface{}{
				"status":     BillingOperationApplied,
				"updated_at": common.GetTimestamp(),
			}).Error; err != nil {
			return err
		}
		return nil
	})
}

// ensureTaskInsertMarkerTx creates (or validates) the request-scoped marker
// and returns it locked. If existingTask is non-nil, the marker is repaired to
// applied as part of the same transaction; this covers rows created before the
// fence was introduced and prevents a stale pending marker from suppressing a
// later retry forever.
func ensureTaskInsertMarkerTx(tx *gorm.DB, spec BillingOperationSpec, existingTask *Task) (*BillingOperation, error) {
	candidate := billingOperationFromSpec(spec)
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate).Error; err != nil {
		return nil, err
	}
	var marker BillingOperation
	if err := lockForUpdate(tx).
		Where("operation_key = ?", candidate.OperationKey).
		First(&marker).Error; err != nil {
		return nil, err
	}
	if !billingOperationMatches(&marker, spec) {
		return nil, ErrTaskInsertFenceConflict
	}
	if marker.Status == BillingOperationApplied {
		return &marker, nil
	}
	if marker.Status != BillingOperationPending {
		return nil, fmt.Errorf("%w: unknown marker status %q", ErrTaskInsertFenceConflict, marker.Status)
	}
	if existingTask == nil {
		return &marker, nil
	}
	if err := tx.Model(&BillingOperation{}).
		Where("id = ?", marker.Id).
		Updates(map[string]interface{}{
			"status":     BillingOperationApplied,
			"updated_at": common.GetTimestamp(),
		}).Error; err != nil {
		return nil, err
	}
	marker.Status = BillingOperationApplied
	return &marker, nil
}

func findTaskForInsertFence(tx *gorm.DB, userID int, requestID string) (*Task, error) {
	var tasks []Task
	err := tx.Where("user_id = ? AND billing_request_id = ?", userID, requestID).
		Order("id").Limit(2).Find(&tasks).Error
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if len(tasks) > 1 {
		// Multiple rows for one request ID indicate historical corruption or a
		// manually reused request ID. Picking one would make the caller settle an
		// arbitrary task, so fence the request for operator reconciliation.
		return nil, ErrTaskInsertFenceConflict
	}
	return &tasks[0], nil
}

// taskInsertMatches compares immutable identity fields. Billing values are
// included so a reused request ID cannot make the caller settle a different
// amount against an existing task row.
func taskInsertMatches(existing, candidate *Task) bool {
	if existing == nil || candidate == nil {
		return false
	}
	return existing.UserId == candidate.UserId &&
		existing.TaskID == candidate.TaskID &&
		// The public TaskID is generated locally; the provider identity is the
		// value used for polling and result retrieval. A reused request fence
		// with a different upstream ID must never load the old row, otherwise a
		// retry could expose/settle the wrong provider task.
		existing.GetUpstreamTaskID() == candidate.GetUpstreamTaskID() &&
		existing.Platform == candidate.Platform &&
		existing.ChannelId == candidate.ChannelId &&
		existing.Action == candidate.Action &&
		existing.Quota == candidate.Quota &&
		existing.BillingRequestId == candidate.BillingRequestId &&
		existing.BillingPreConsumedQuota == candidate.BillingPreConsumedQuota &&
		existing.BillingSettlementQuota == candidate.BillingSettlementQuota &&
		existing.BillingTokenUnlimited == candidate.BillingTokenUnlimited &&
		existing.BillingPlayground == candidate.BillingPlayground
}

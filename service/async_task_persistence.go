package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"gorm.io/gorm"
)

// AsyncTaskPersistenceAttempts bounds the amount of work spent retrying a
// local write after an upstream provider has accepted an asynchronous task.
// The provider request is not replayed by these retries; InsertWithBillingFence
// makes each attempt idempotent for the same request identity.
const AsyncTaskPersistenceAttempts = 3

const asyncTaskPersistenceRetryDelay = 20 * time.Millisecond

// AsyncTaskPersistenceResult describes the two durable boundaries involved in
// an accepted async task.  MarkerError and InsertError are kept separately so
// callers can distinguish "billing intent was not journaled" from "the task
// row could not be created" and route either case to manual reconciliation.
type AsyncTaskPersistenceResult struct {
	TaskPersisted  bool
	MarkersDurable bool
	MarkerError    error
	InsertError    error
}

// Error returns a bounded, human-readable summary of the persistence failure.
// It intentionally does not include provider payloads or task data.
func (r AsyncTaskPersistenceResult) Error() error {
	if r.MarkerError == nil && r.InsertError == nil {
		return nil
	}
	parts := make([]string, 0, 2)
	if r.MarkerError != nil {
		parts = append(parts, "billing markers: "+common.SensitiveLogMeta(r.MarkerError.Error()))
	}
	if r.InsertError != nil {
		parts = append(parts, "task insert: "+common.SensitiveLogMeta(r.InsertError.Error()))
	}
	return errors.New(strings.Join(parts, "; "))
}

// PersistAcceptedAsyncTask establishes the local durability boundary after a
// provider has accepted an async request.  Marker creation is attempted first
// and is fail-closed: an error is never logged-and-ignored.  The task row is
// then inserted through its request fence with a small bounded retry budget.
//
// If marker creation cannot be completed but the row itself is available, the
// row is persisted in the durable MANUAL settlement state.  This keeps the
// provider identity and billing snapshot discoverable without allowing a
// worker to guess at a missing ledger operation.  If both writes fail, no
// in-memory settlement/refund is attempted; the caller must emit an explicit
// manual-reconciliation event because the provider task may be orphaned.
func PersistAcceptedAsyncTask(task *model.Task, relayInfo *relaycommon.RelayInfo, actualQuota int) AsyncTaskPersistenceResult {
	result := AsyncTaskPersistenceResult{}
	if task == nil {
		result.InsertError = errors.New("task is nil")
		return result
	}
	if strings.TrimSpace(task.BillingRequestId) == "" {
		// A plain Task.Insert has no request-scoped idempotency fence and is not
		// safe after a provider accepts a task. Refuse to persist an unfenced row;
		// the caller must reconcile the provider response manually.
		result.InsertError = errors.New("task billing request id is missing")
		setTaskSettlementManual(task, result.InsertError)
		return result
	}
	if relayInfo == nil {
		result.MarkerError = errors.New("relay info is nil")
		// There is still value in preserving the task identity if the caller has
		// already assembled it, but without RelayInfo we cannot create billing
		// markers. Fence the row as manual before attempting the insert.
		setTaskSettlementManual(task, result.MarkerError)
	} else {
		result.MarkerError = ensureAsyncTaskBillingOperationsRetry(relayInfo, actualQuota)
		if result.MarkerError != nil {
			// Do not leave a pending state that a future worker could interpret as
			// an authorized settlement when the marker transaction did not commit.
			setTaskSettlementManual(task, result.MarkerError)
		} else {
			result.MarkersDurable = true
		}
	}

	result.InsertError = insertAsyncTaskWithRetry(task)
	if result.InsertError == nil {
		result.TaskPersisted = task.ID > 0
		if result.MarkerError != nil && result.TaskPersisted {
			// InsertWithBillingFence may have loaded a row committed by an
			// earlier ambiguous attempt, replacing the in-memory state. Re-apply
			// the manual fence conditionally so that row is not accidentally left
			// pending after marker creation failed in this process.
			if task.BillingSettlementState != model.TaskBillingSettlementManual {
				if markErr := task.MarkBillingSettlementManual(result.MarkerError); markErr != nil {
					// A completed row must never be downgraded. For all other
					// failures, preserve the original marker error and let the
					// caller surface the inability to fence it.
					if !errors.Is(markErr, gorm.ErrRecordNotFound) {
						result.InsertError = fmt.Errorf("mark task settlement manual: %w", markErr)
					}
				}
			}
		}
	}
	return result
}

// ensureAsyncTaskBillingOperationsRetry retries only the local marker
// transaction. It never retries the upstream provider request.
func ensureAsyncTaskBillingOperationsRetry(relayInfo *relaycommon.RelayInfo, actualQuota int) error {
	var lastErr error
	for attempt := 0; attempt < AsyncTaskPersistenceAttempts; attempt++ {
		lastErr = EnsureAsyncTaskBillingOperations(relayInfo, actualQuota)
		if lastErr == nil {
			return nil
		}
		// Immutable-spec conflicts and malformed input cannot be repaired by a
		// retry and should fail closed immediately.
		if errors.Is(lastErr, model.ErrBillingOperationConflict) ||
			errors.Is(lastErr, model.ErrBillingOperationInvalid) {
			return lastErr
		}
		if attempt+1 < AsyncTaskPersistenceAttempts {
			time.Sleep(asyncTaskPersistenceRetryDelay)
		}
	}
	return fmt.Errorf("async task billing marker transaction failed after %d attempts: %w", AsyncTaskPersistenceAttempts, lastErr)
}

func insertAsyncTaskWithRetry(task *model.Task) error {
	if task == nil {
		return errors.New("task is nil")
	}
	return retryAsyncTaskInsert(task.InsertWithBillingFence)
}

func retryAsyncTaskInsert(insert func() error) error {
	if insert == nil {
		return errors.New("task insert function is nil")
	}
	var lastErr error
	for attempt := 0; attempt < AsyncTaskPersistenceAttempts; attempt++ {
		lastErr = insert()
		if lastErr == nil {
			return nil
		}
		// A fence conflict is an identity violation, not a transient database
		// outage. Retrying it cannot make the request safe.
		if errors.Is(lastErr, model.ErrTaskInsertFenceConflict) {
			return lastErr
		}
		if attempt+1 < AsyncTaskPersistenceAttempts {
			time.Sleep(asyncTaskPersistenceRetryDelay)
		}
	}
	return fmt.Errorf("async task insert failed after %d attempts: %w", AsyncTaskPersistenceAttempts, lastErr)
}

func setTaskSettlementManual(task *model.Task, reason error) {
	if task == nil {
		return
	}
	task.BillingSettlementState = model.TaskBillingSettlementManual
	task.BillingSettlementUntil = 0
	message := "manual reconciliation required"
	if reason != nil && strings.TrimSpace(reason.Error()) != "" {
		message = common.MaskSensitiveInfo(strings.TrimSpace(reason.Error()))
	}
	if len(message) > 2000 {
		message = message[:2000]
	}
	task.BillingSettlementError = message
}

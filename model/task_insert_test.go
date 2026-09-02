package model

import (
	"errors"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newFencedTask() *Task {
	return &Task{
		TaskID:                  "task-fenced-insert",
		Platform:                constant.TaskPlatformSuno,
		UserId:                  9301,
		ChannelId:               9301,
		Action:                  "video",
		Quota:                   420,
		BillingRequestId:        "fenced-insert-request",
		BillingPreConsumedQuota: 400,
		BillingSettlementQuota:  420,
		Status:                  TaskStatusSubmitted,
		Progress:                "0%",
	}
}

func TestInsertWithBillingFenceLoadsCommittedRowOnRetry(t *testing.T) {
	truncateTables(t)

	first := newFencedTask()
	require.NoError(t, first.InsertWithBillingFence())
	require.NotZero(t, first.ID)

	retry := newFencedTask()
	require.NoError(t, retry.InsertWithBillingFence())
	assert.Equal(t, first.ID, retry.ID)
	assert.Equal(t, first.TaskID, retry.TaskID)

	var taskCount int64
	require.NoError(t, DB.Model(&Task{}).Where("user_id = ? AND billing_request_id = ?", first.UserId, first.BillingRequestId).Count(&taskCount).Error)
	assert.EqualValues(t, 1, taskCount)
	var markerCount int64
	require.NoError(t, DB.Model(&BillingOperation{}).Where("request_id = ? AND component = ?", first.BillingRequestId, taskInsertBillingComponent).Count(&markerCount).Error)
	assert.EqualValues(t, 1, markerCount)

	var marker BillingOperation
	require.NoError(t, DB.Where("request_id = ? AND component = ?", first.BillingRequestId, taskInsertBillingComponent).First(&marker).Error)
	assert.Equal(t, BillingOperationApplied, marker.Status)
}

func TestInsertWithBillingFenceRepairsMarkerForPreexistingTask(t *testing.T) {
	truncateTables(t)

	legacy := newFencedTask()
	// Simulate a row written by the deployment that predates the create fence.
	require.NoError(t, DB.Create(legacy).Error)
	retry := newFencedTask()
	retry.ID = 0
	require.NoError(t, retry.InsertWithBillingFence())
	assert.Equal(t, legacy.ID, retry.ID)

	var marker BillingOperation
	require.NoError(t, DB.Where("request_id = ? AND component = ?", retry.BillingRequestId, taskInsertBillingComponent).First(&marker).Error)
	assert.Equal(t, BillingOperationApplied, marker.Status)
}

func TestInsertWithBillingFenceRepairsPendingMarkerForExistingTask(t *testing.T) {
	truncateTables(t)
	legacy := newFencedTask()
	require.NoError(t, DB.Create(legacy).Error)
	require.NoError(t, DB.Create(&BillingOperation{
		OperationKey: BillingOperationKey(legacy.BillingRequestId, taskInsertBillingComponent),
		RequestId:    legacy.BillingRequestId,
		Component:    taskInsertBillingComponent,
		UserId:       legacy.UserId,
		Status:       BillingOperationPending,
	}).Error)

	retry := newFencedTask()
	require.NoError(t, retry.InsertWithBillingFence())
	assert.Equal(t, legacy.ID, retry.ID)
	var marker BillingOperation
	require.NoError(t, DB.Where("request_id = ? AND component = ?", legacy.BillingRequestId, taskInsertBillingComponent).First(&marker).Error)
	assert.Equal(t, BillingOperationApplied, marker.Status)
}

func TestInsertWithBillingFenceRollsBackMarkerWhenTaskInsertFails(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Exec(`
		CREATE TRIGGER fail_fenced_task_insert
		BEFORE INSERT ON tasks
		WHEN NEW.user_id = 9302
		BEGIN
			SELECT RAISE(ABORT, 'forced task insert failure');
		END;
	`).Error)

	task := newFencedTask()
	task.UserId = 9302
	task.TaskID = "task-fenced-rollback"
	task.BillingRequestId = "fenced-rollback-request"
	err := task.InsertWithBillingFence()
	assert.Error(t, err)

	var taskCount, markerCount int64
	require.NoError(t, DB.Model(&Task{}).Where("billing_request_id = ?", task.BillingRequestId).Count(&taskCount).Error)
	require.NoError(t, DB.Model(&BillingOperation{}).Where("request_id = ? AND component = ?", task.BillingRequestId, taskInsertBillingComponent).Count(&markerCount).Error)
	assert.Zero(t, taskCount)
	assert.Zero(t, markerCount)

	require.NoError(t, DB.Exec("DROP TRIGGER IF EXISTS fail_fenced_task_insert").Error)
	require.NoError(t, task.InsertWithBillingFence())
	assert.NotZero(t, task.ID)
}

func TestInsertWithBillingFenceRejectsRequestReuse(t *testing.T) {
	truncateTables(t)
	first := newFencedTask()
	require.NoError(t, first.InsertWithBillingFence())

	conflicting := newFencedTask()
	conflicting.TaskID = "task-fenced-different"
	err := conflicting.InsertWithBillingFence()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTaskInsertFenceConflict))

	var taskCount int64
	require.NoError(t, DB.Model(&Task{}).Where("billing_request_id = ?", first.BillingRequestId).Count(&taskCount).Error)
	assert.EqualValues(t, 1, taskCount)
}

func TestInsertWithBillingFenceRejectsDifferentUpstreamIdentity(t *testing.T) {
	truncateTables(t)
	first := newFencedTask()
	first.PrivateData.UpstreamTaskID = "provider-task-a"
	require.NoError(t, first.InsertWithBillingFence())

	conflicting := newFencedTask()
	conflicting.PrivateData.UpstreamTaskID = "provider-task-b"
	err := conflicting.InsertWithBillingFence()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTaskInsertFenceConflict))

	var taskCount int64
	require.NoError(t, DB.Model(&Task{}).
		Where("billing_request_id = ?", first.BillingRequestId).
		Count(&taskCount).Error)
	assert.EqualValues(t, 1, taskCount)
}

func TestInsertWithBillingFenceFailsClosedForOrphanMarker(t *testing.T) {
	truncateTables(t)
	require.NoError(t, ApplyBillingOperation(BillingOperationSpec{
		RequestID: "fenced-orphan-request",
		Component: taskInsertBillingComponent,
		UserID:    9303,
	}))

	task := newFencedTask()
	task.UserId = 9303
	task.TaskID = "task-fenced-orphan"
	task.BillingRequestId = "fenced-orphan-request"
	err := task.InsertWithBillingFence()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTaskInsertFenceConflict))

	var taskCount int64
	require.NoError(t, DB.Model(&Task{}).Where("billing_request_id = ?", task.BillingRequestId).Count(&taskCount).Error)
	assert.Zero(t, taskCount)
}

func TestInsertWithBillingFenceConcurrentCallersShareOneRow(t *testing.T) {
	truncateTables(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	rows := make(chan int64, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task := newFencedTask()
			errs <- task.InsertWithBillingFence()
			rows <- task.ID
		}()
	}
	wg.Wait()
	close(errs)
	close(rows)
	var gotErrs []error
	for err := range errs {
		if err != nil {
			gotErrs = append(gotErrs, err)
		}
	}
	// SQLite can reject the losing writer with a transient busy error; callers
	// may retry that error, but at most one committed row/marker is allowed.
	assert.LessOrEqual(t, len(gotErrs), 1)
	var taskCount, markerCount int64
	require.NoError(t, DB.Model(&Task{}).Where("billing_request_id = ?", "fenced-insert-request").Count(&taskCount).Error)
	require.NoError(t, DB.Model(&BillingOperation{}).Where("request_id = ? AND component = ?", "fenced-insert-request", taskInsertBillingComponent).Count(&markerCount).Error)
	assert.EqualValues(t, 1, taskCount)
	assert.EqualValues(t, 1, markerCount)
}

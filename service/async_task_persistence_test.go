package service

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newAcceptedAsyncTaskPersistenceFixture() (*model.Task, *relaycommon.RelayInfo) {
	info := &relaycommon.RelayInfo{
		RequestId:   "accepted-persistence-request",
		UserId:      9310,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 9310},
	}
	task := &model.Task{
		TaskID:                  "task-accepted-persistence",
		Platform:                constant.TaskPlatformSuno,
		UserId:                  info.UserId,
		ChannelId:               info.ChannelId,
		Action:                  "video",
		Quota:                   30,
		BillingRequestId:        info.RequestId,
		BillingPreConsumedQuota: 20,
		BillingSettlementQuota:  30,
		BillingSettlementState:  model.TaskBillingSettlementPending,
		Status:                  model.TaskStatusSubmitted,
		Progress:                "0%",
	}
	return task, info
}

func TestRetryAsyncTaskInsertUsesBoundedAttemptsAndStopsOnFenceConflict(t *testing.T) {
	calls := 0
	transient := errors.New("temporary database outage")
	err := retryAsyncTaskInsert(func() error {
		calls++
		return transient
	})
	require.Error(t, err)
	assert.Equal(t, AsyncTaskPersistenceAttempts, calls)

	calls = 0
	err = retryAsyncTaskInsert(func() error {
		calls++
		return model.ErrTaskInsertFenceConflict
	})
	require.ErrorIs(t, err, model.ErrTaskInsertFenceConflict)
	assert.Equal(t, 1, calls, "an identity conflict is not retryable")
}

func TestPersistAcceptedAsyncTaskFailsClosedWhenMarkersCannotBeCreated(t *testing.T) {
	truncate(t)
	task, info := newAcceptedAsyncTaskPersistenceFixture()
	// An out-of-range provider-reported quota is a permanent marker-spec error.
	// The local task may still be retained, but it must be fenced for manual
	// reconciliation rather than left pending for an automatic charge.
	result := PersistAcceptedAsyncTask(task, info, common.MaxQuota+1)
	require.Error(t, result.Error())
	assert.False(t, result.MarkersDurable)
	assert.True(t, result.TaskPersisted)
	assert.Equal(t, model.TaskBillingSettlementManual, task.BillingSettlementState)

	var persisted model.Task
	require.NoError(t, model.DB.Where("billing_request_id = ?", info.RequestId).First(&persisted).Error)
	assert.Equal(t, model.TaskBillingSettlementManual, persisted.BillingSettlementState)
	assert.NotEmpty(t, persisted.BillingSettlementError)
	var markerCount int64
	require.NoError(t, model.DB.Model(&model.BillingOperation{}).
		Where("request_id = ? AND component IN ?", info.RequestId, []string{"settle", "settle_usage"}).Count(&markerCount).Error)
	assert.Zero(t, markerCount, "a failed marker transaction must not advertise settlement intent")
}

func TestPersistAcceptedAsyncTaskLeavesDurableMarkersWhenTaskInsertFails(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID = 9311, 9311, 9311
	seedUser(t, userID, 100)
	seedToken(t, tokenID, userID, "accepted-persistence-token", 100)
	seedChannel(t, channelID)
	info := &relaycommon.RelayInfo{
		RequestId:       "accepted-insert-failure-request",
		UserId:          userID,
		TokenId:         tokenID,
		TokenKey:        "accepted-persistence-token",
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: channelID},
		ForcePreConsume: true,
	}
	session, apiErr := NewBillingSession(newBillingSessionTestContext(), info, 20)
	require.Nil(t, apiErr)
	info.Billing = session
	task := &model.Task{
		TaskID:                  "task-accepted-insert-failure",
		Platform:                constant.TaskPlatformSuno,
		UserId:                  userID,
		ChannelId:               channelID,
		Action:                  "video",
		Quota:                   30,
		BillingRequestId:        info.RequestId,
		BillingPreConsumedQuota: 20,
		BillingSettlementQuota:  30,
		BillingSettlementState:  model.TaskBillingSettlementPending,
		Status:                  model.TaskStatusSubmitted,
		Progress:                "0%",
	}
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_accepted_async_task_insert
		BEFORE INSERT ON tasks
		WHEN NEW.user_id = 9311
		BEGIN
			SELECT RAISE(ABORT, 'forced accepted task insert failure');
		END;
	`).Error)

	result := PersistAcceptedAsyncTask(task, info, 30)
	assert.False(t, result.TaskPersisted)
	assert.True(t, result.MarkersDurable)
	assert.Error(t, result.Error())

	var taskCount int64
	require.NoError(t, model.DB.Model(&model.Task{}).
		Where("billing_request_id = ?", info.RequestId).Count(&taskCount).Error)
	assert.Zero(t, taskCount)
	var pending []model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND status = ?", info.RequestId, model.BillingOperationPending).Find(&pending).Error)
	assert.Len(t, pending, 2, "settlement and usage intent remain recoverable for the worker")
	assert.Equal(t, 80, userQuotaForBillingTest(t, userID), "persistence failure must not settle from an unpersisted task")
	assert.Equal(t, 80, tokenRemainForBillingTest(t, tokenID))

	// The generic billing markers do not contain the provider task identity.
	// Reconciliation must leave them pending rather than charging for a task the
	// user cannot query and the poller cannot refund.
	candidates, completed, retryPending := ReconcilePendingTaskBillingOperations(context.Background(), 100)
	assert.Equal(t, 2, candidates)
	assert.Zero(t, completed)
	assert.Equal(t, 2, retryPending)
	assert.Equal(t, 80, userQuotaForBillingTest(t, userID))
	assert.Equal(t, 80, tokenRemainForBillingTest(t, tokenID))

	require.NoError(t, model.DB.Exec("DROP TRIGGER IF EXISTS fail_accepted_async_task_insert").Error)
}

// Keep the test context constructor local to this file's fixture when the
// package's broader billing tests are built with a reduced set of files.
var _ = gin.CreateTestContext
var _ = httptest.NewRecorder

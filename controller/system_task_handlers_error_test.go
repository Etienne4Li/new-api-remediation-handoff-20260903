package controller

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAsyncTaskPollHandlerMarksOperationalErrorsFailed(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:async-task-handler-error?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.SystemTask{}, &model.SystemTaskLock{}))

	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })

	task, err := model.CreateSystemTask(model.SystemTaskTypeAsyncTaskPoll, nil, nil)
	require.NoError(t, err)
	const runnerID = "async-error-runner"
	claimed, ok, err := model.ClaimSystemTask(task.ID, task.Type, runnerID, 1<<62)
	require.NoError(t, err)
	require.True(t, ok)

	// The deliberately incomplete schema makes the timeout/task queue queries
	// fail. The handler must preserve the summary and mark the run failed instead
	// of presenting that database outage as a successful empty polling pass.
	asyncTaskPollHandler{}.Run(context.Background(), claimed, runnerID)

	stored, err := model.GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, model.SystemTaskStatusFailed, stored.Status)
	assert.NotEmpty(t, stored.Error)
	assert.Contains(t, stored.Result, "failed_stages")
}

func TestAsyncTaskPollHandlerEnabledIncludesAllDurableBillingAliases(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:async-task-handler-billing-alias?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Task{}, &model.BillingOperation{}))

	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	user := &model.User{Id: 9401, Username: "billing-alias-probe", Password: "unused", Status: 1, Quota: 100}
	require.NoError(t, db.Create(user).Error)

	// This marker is not one of the historical four names previously hard-coded
	// by the scheduler. With no unfinished provider task, Enabled must still
	// wake the reconciliation worker so the marker cannot remain pending forever.
	require.NoError(t, model.EnsureBillingOperation(model.BillingOperationSpec{
		RequestID: "billing-alias-probe-request",
		Component: model.BillingOperationTerminalSettleUsageComponent,
		UserID:    user.Id,
	}))
	assert.True(t, (asyncTaskPollHandler{}).Enabled())
}

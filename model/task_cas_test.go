package model

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		panic("failed to open test db: " + err.Error())
	}
	DB = db
	LOG_DB = db

	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	common.BatchUpdateEnabled = false
	common.SetLogConsumeEnabled(true)
	initCol()

	sqlDB, err := db.DB()
	if err != nil {
		panic("failed to get sql.DB: " + err.Error())
	}
	sqlDB.SetMaxOpenConns(1)

	if err := db.AutoMigrate(
		&Task{},
		&User{},
		&UserSession{},
		&AuthFlow{},
		&ExternalIdentityClaim{},
		&Token{},
		&PasskeyCredential{},
		&TwoFA{},
		&TwoFABackupCode{},
		&Log{},
		&Channel{},
		&QuotaData{},
		&Ability{},
		&TopUp{},
		&PaymentEvent{},
		&ProviderPaymentBinding{},
		&ProviderRefundEvent{},
		&ProviderReversalEffect{},
		&SubscriptionPlan{},
		&SubscriptionOrder{},
		&UserSubscription{},
		&SubscriptionPreConsumeRecord{},
		&SubscriptionProviderBindingRecord{},
		&BillingOperation{},
		&QuotaCacheRepair{},
		&Checkin{},
		&UserOAuthBinding{},
		&PerfMetric{},
		&SystemInstance{},
		&SystemTask{},
		&SystemTaskLock{},
	); err != nil {
		panic("failed to migrate: " + err.Error())
	}

	os.Exit(m.Run())
}

func truncateTables(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		DB.Exec("DELETE FROM tasks")
		DB.Exec("DELETE FROM auth_flows")
		DB.Exec("DELETE FROM external_identity_claims")
		DB.Exec("DELETE FROM user_sessions")
		DB.Exec("DELETE FROM passkey_credentials")
		DB.Exec("DELETE FROM two_fa_backup_codes")
		DB.Exec("DELETE FROM two_fas")
		DB.Exec("DELETE FROM tokens")
		DB.Exec("DELETE FROM user_oauth_bindings")
		DB.Exec("DELETE FROM users")
		DB.Exec("DELETE FROM logs")
		DB.Exec("DELETE FROM channels")
		DB.Exec("DELETE FROM quota_data")
		DB.Exec("DELETE FROM abilities")
		DB.Exec("DELETE FROM payment_events")
		DB.Exec("DELETE FROM provider_payment_bindings")
		DB.Exec("DELETE FROM provider_refund_events")
		DB.Exec("DELETE FROM provider_reversal_effects")
		DB.Exec("DELETE FROM top_ups")
		DB.Exec("DELETE FROM subscription_orders")
		DB.Exec("DELETE FROM subscription_plans")
		DB.Exec("DELETE FROM subscription_pre_consume_records")
		DB.Exec("DELETE FROM user_subscriptions")
		DB.Exec("DELETE FROM subscription_provider_bindings")
		DB.Exec("DELETE FROM billing_operations")
		DB.Exec("DELETE FROM quota_cache_repairs")
		DB.Exec("DELETE FROM checkins")
		DB.Exec("DELETE FROM perf_metrics")
		DB.Exec("DELETE FROM system_instances")
		DB.Exec("DELETE FROM system_task_locks")
		DB.Exec("DELETE FROM system_tasks")
	})
}

func insertTask(t *testing.T, task *Task) {
	t.Helper()
	task.CreatedAt = time.Now().Unix()
	task.UpdatedAt = time.Now().Unix()
	require.NoError(t, DB.Create(task).Error)
}

// ---------------------------------------------------------------------------
// Snapshot / Equal — pure logic tests (no DB)
// ---------------------------------------------------------------------------

func TestSnapshotEqual_Same(t *testing.T) {
	s := taskSnapshot{
		Status:     TaskStatusInProgress,
		Progress:   "50%",
		StartTime:  1000,
		FinishTime: 0,
		FailReason: "",
		ResultURL:  "",
		Data:       json.RawMessage(`{"key":"value"}`),
	}
	assert.True(t, s.Equal(s))
}

func TestSnapshotEqual_DifferentStatus(t *testing.T) {
	a := taskSnapshot{Status: TaskStatusInProgress, Data: json.RawMessage(`{}`)}
	b := taskSnapshot{Status: TaskStatusSuccess, Data: json.RawMessage(`{}`)}
	assert.False(t, a.Equal(b))
}

func TestSnapshotEqual_DifferentProgress(t *testing.T) {
	a := taskSnapshot{Status: TaskStatusInProgress, Progress: "30%", Data: json.RawMessage(`{}`)}
	b := taskSnapshot{Status: TaskStatusInProgress, Progress: "60%", Data: json.RawMessage(`{}`)}
	assert.False(t, a.Equal(b))
}

func TestSnapshotEqual_DifferentData(t *testing.T) {
	a := taskSnapshot{Status: TaskStatusInProgress, Data: json.RawMessage(`{"a":1}`)}
	b := taskSnapshot{Status: TaskStatusInProgress, Data: json.RawMessage(`{"a":2}`)}
	assert.False(t, a.Equal(b))
}

func TestSnapshotEqual_NilVsEmpty(t *testing.T) {
	a := taskSnapshot{Status: TaskStatusInProgress, Data: nil}
	b := taskSnapshot{Status: TaskStatusInProgress, Data: json.RawMessage{}}
	// bytes.Equal(nil, []byte{}) == true
	assert.True(t, a.Equal(b))
}

func TestSnapshot_Roundtrip(t *testing.T) {
	task := &Task{
		Status:     TaskStatusInProgress,
		Progress:   "42%",
		StartTime:  1234,
		FinishTime: 5678,
		FailReason: "timeout",
		PrivateData: TaskPrivateData{
			ResultURL: "https://example.com/result.mp4",
		},
		Data: json.RawMessage(`{"model":"test-model"}`),
	}
	snap := task.Snapshot()
	assert.Equal(t, task.Status, snap.Status)
	assert.Equal(t, task.Progress, snap.Progress)
	assert.Equal(t, task.StartTime, snap.StartTime)
	assert.Equal(t, task.FinishTime, snap.FinishTime)
	assert.Equal(t, task.FailReason, snap.FailReason)
	assert.Equal(t, task.PrivateData.ResultURL, snap.ResultURL)
	assert.JSONEq(t, string(task.Data), string(snap.Data))
}

func TestIsTaskStatusStale(t *testing.T) {
	tests := []struct {
		name     string
		current  TaskStatus
		incoming TaskStatus
		stale    bool
	}{
		// Equal states are allowed so a poll can still refresh progress,
		// timestamps, failure details, or provider metadata.
		{name: "same in progress allows metadata", current: TaskStatusInProgress, incoming: TaskStatusInProgress, stale: false},
		{name: "same success allows metadata", current: TaskStatusSuccess, incoming: TaskStatusSuccess, stale: false},
		{name: "same failure allows metadata", current: TaskStatusFailure, incoming: TaskStatusFailure, stale: false},

		// Provider responses can arrive out of order. A delayed queued/submitted
		// response must not move an in-flight task backwards.
		{name: "in progress rejects queued", current: TaskStatusInProgress, incoming: TaskStatusQueued, stale: true},
		{name: "in progress rejects submitted", current: TaskStatusInProgress, incoming: TaskStatusSubmitted, stale: true},
		{name: "queued rejects submitted", current: TaskStatusQueued, incoming: TaskStatusSubmitted, stale: true},
		{name: "submitted rejects not start", current: TaskStatusSubmitted, incoming: TaskStatusNotStart, stale: true},

		// A terminal row is authoritative for billing. Neither a delayed
		// in-flight response nor the opposite terminal state may replace it.
		{name: "success rejects failure", current: TaskStatusSuccess, incoming: TaskStatusFailure, stale: true},
		{name: "success rejects in progress", current: TaskStatusSuccess, incoming: TaskStatusInProgress, stale: true},
		{name: "failure rejects success", current: TaskStatusFailure, incoming: TaskStatusSuccess, stale: true},
		{name: "failure rejects queued", current: TaskStatusFailure, incoming: TaskStatusQueued, stale: true},

		// Forward transitions remain valid, including either terminal outcome
		// from an unfinished task.
		{name: "not start to submitted", current: TaskStatusNotStart, incoming: TaskStatusSubmitted, stale: false},
		{name: "submitted to queued", current: TaskStatusSubmitted, incoming: TaskStatusQueued, stale: false},
		{name: "queued to in progress", current: TaskStatusQueued, incoming: TaskStatusInProgress, stale: false},
		{name: "in progress to success", current: TaskStatusInProgress, incoming: TaskStatusSuccess, stale: false},
		{name: "in progress to failure", current: TaskStatusInProgress, incoming: TaskStatusFailure, stale: false},
		{name: "queued to success", current: TaskStatusQueued, incoming: TaskStatusSuccess, stale: false},

		// Empty/UNKNOWN provider states are not authoritative. A legacy row with
		// an unknown current state can be recovered by an explicit valid state.
		{name: "empty incoming is unusable", current: TaskStatusInProgress, incoming: TaskStatus(""), stale: true},
		{name: "unknown incoming is unusable", current: TaskStatusInProgress, incoming: TaskStatusUnknown, stale: true},
		{name: "unknown current recovers", current: TaskStatusUnknown, incoming: TaskStatusInProgress, stale: false},
		{name: "empty current recovers", current: TaskStatus(""), incoming: TaskStatusSuccess, stale: false},
		{name: "arbitrary unknown current recovers", current: TaskStatus("provider-new-state"), incoming: TaskStatusQueued, stale: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.stale, IsTaskStatusStale(tt.current, tt.incoming))
		})
	}
}

// ---------------------------------------------------------------------------
// UpdateWithStatus CAS — DB integration tests
// ---------------------------------------------------------------------------

func TestUpdateWithStatus_Win(t *testing.T) {
	truncateTables(t)

	task := &Task{
		TaskID:   "task_cas_win",
		Status:   TaskStatusInProgress,
		Progress: "50%",
		Data:     json.RawMessage(`{}`),
	}
	insertTask(t, task)

	task.Status = TaskStatusSuccess
	task.Progress = "100%"
	won, err := task.UpdateWithStatus(TaskStatusInProgress)
	require.NoError(t, err)
	assert.True(t, won)

	var reloaded Task
	require.NoError(t, DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, TaskStatusSuccess, reloaded.Status)
	assert.Equal(t, "100%", reloaded.Progress)
}

func TestUpdateWithStatus_RejectsStaleStatusTransitions(t *testing.T) {
	truncateTables(t)

	tests := []struct {
		name     string
		current  TaskStatus
		incoming TaskStatus
	}{
		{name: "in progress queued", current: TaskStatusInProgress, incoming: TaskStatusQueued},
		{name: "in progress submitted", current: TaskStatusInProgress, incoming: TaskStatusSubmitted},
		{name: "success failure", current: TaskStatusSuccess, incoming: TaskStatusFailure},
		{name: "success in progress", current: TaskStatusSuccess, incoming: TaskStatusInProgress},
		{name: "failure success", current: TaskStatusFailure, incoming: TaskStatusSuccess},
		{name: "failure in progress", current: TaskStatusFailure, incoming: TaskStatusInProgress},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &Task{
				TaskID:   "task_cas_stale_" + tt.name,
				Status:   tt.current,
				Progress: "50%",
				Quota:    123,
				Data:     json.RawMessage(`{"before":true}`),
			}
			insertTask(t, task)

			task.Status = tt.incoming
			task.Progress = "0%"
			task.Data = json.RawMessage(`{"after":true}`)
			won, err := task.UpdateWithStatus(tt.current)
			require.NoError(t, err)
			assert.False(t, won, "a stale status response must not win the CAS")

			var reloaded Task
			require.NoError(t, DB.First(&reloaded, task.ID).Error)
			assert.Equal(t, tt.current, reloaded.Status)
			assert.Equal(t, "50%", reloaded.Progress)
			assert.JSONEq(t, `{"before":true}`, string(reloaded.Data))
		})
	}
}

func TestUpdateWithStatus_SameStatusStillUpdatesMetadata(t *testing.T) {
	truncateTables(t)

	task := &Task{
		TaskID:   "task_cas_same_status_metadata",
		Status:   TaskStatusInProgress,
		Progress: "25%",
		Quota:    321,
		Data:     json.RawMessage(`{"phase":1}`),
	}
	insertTask(t, task)

	task.Progress = "75%"
	task.Data = json.RawMessage(`{"phase":2}`)
	won, err := task.UpdateWithStatus(TaskStatusInProgress)
	require.NoError(t, err)
	assert.True(t, won, "same-status metadata refresh must remain allowed")

	var reloaded Task
	require.NoError(t, DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, TaskStatusInProgress, reloaded.Status)
	assert.Equal(t, "75%", reloaded.Progress)
	assert.JSONEq(t, `{"phase":2}`, string(reloaded.Data))
}

func TestUpdateWithStatusRejectsChangedProviderIdentity(t *testing.T) {
	truncateTables(t)

	task := &Task{
		TaskID:   "task_cas_identity_guard",
		Status:   TaskStatusInProgress,
		Progress: "50%",
		PrivateData: TaskPrivateData{
			UpstreamTaskID: "provider-identity-original",
		},
		Data: json.RawMessage(`{"before":true}`),
	}
	insertTask(t, task)

	// A stale or malicious in-memory object must not be able to replace the
	// provider correlation ID while using the ordinary status CAS map update.
	task.Status = TaskStatusSuccess
	task.Progress = "100%"
	task.PrivateData.UpstreamTaskID = "provider-identity-attacker"
	task.Data = json.RawMessage(`{"after":true}`)
	won, err := task.UpdateWithStatus(TaskStatusInProgress)
	require.NoError(t, err)
	assert.False(t, won)

	var reloaded Task
	require.NoError(t, DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, TaskStatusInProgress, reloaded.Status)
	assert.Equal(t, "provider-identity-original", reloaded.GetUpstreamTaskID())
	assert.JSONEq(t, `{"before":true}`, string(reloaded.Data))
}

func TestUpdateWithStatusRejectsChangedPersistedIdentity(t *testing.T) {
	truncateTables(t)

	task := &Task{
		TaskID:   "task_cas_identity_persisted_guard",
		Status:   TaskStatusInProgress,
		Progress: "50%",
		PrivateData: TaskPrivateData{
			UpstreamTaskID: "provider-identity-original-2",
		},
	}
	insertTask(t, task)

	// Simulate another writer replacing the denormalized identity after this
	// worker loaded its snapshot. The status update must lose its CAS rather
	// than overwrite private_data on the newly associated provider task.
	otherDigest, ok := TaskUpstreamIdentityDigest("provider-identity-other-2")
	require.True(t, ok)
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).
		Update("upstream_task_identity", otherDigest).Error)
	task.Status = TaskStatusSuccess
	task.Progress = "100%"
	won, err := task.UpdateWithStatus(TaskStatusInProgress)
	require.NoError(t, err)
	assert.False(t, won)

	var reloaded Task
	require.NoError(t, DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, TaskStatusInProgress, reloaded.Status)
	assert.Equal(t, otherDigest, strings.TrimSpace(*reloaded.UpstreamTaskIdentity))
}

func TestUpdateWithStatusBackfillsLegacyNullIdentity(t *testing.T) {
	truncateTables(t)

	task := &Task{
		TaskID:   "task_cas_legacy_identity",
		Status:   TaskStatusInProgress,
		Progress: "40%",
		PrivateData: TaskPrivateData{
			UpstreamTaskID: "provider-legacy-identity",
			ResultURL:      "https://example.invalid/result",
		},
	}
	insertTask(t, task)
	// Simulate a row written before the denormalized identity column was
	// introduced. The first status refresh should still win, atomically publish
	// the digest, and retain private metadata not changed by the caller.
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).
		Update("upstream_task_identity", nil).Error)

	task.Status = TaskStatusSuccess
	task.Progress = "100%"
	won, err := task.UpdateWithStatus(TaskStatusInProgress)
	require.NoError(t, err)
	assert.True(t, won)

	var reloaded Task
	require.NoError(t, DB.First(&reloaded, task.ID).Error)
	wantDigest, ok := TaskUpstreamIdentityDigest("provider-legacy-identity")
	require.True(t, ok)
	require.NotNil(t, reloaded.UpstreamTaskIdentity)
	assert.Equal(t, wantDigest, strings.TrimSpace(*reloaded.UpstreamTaskIdentity))
	assert.EqualValues(t, TaskStatusSuccess, reloaded.Status)
	assert.Equal(t, "https://example.invalid/result", reloaded.PrivateData.ResultURL)
}

func TestUpdateQuotaScopesWriteToTaskID(t *testing.T) {
	truncateTables(t)

	first := &Task{TaskID: "quota-scope-first", Quota: 100, Status: TaskStatusInProgress}
	second := &Task{TaskID: "quota-scope-second", Quota: 200, Status: TaskStatusInProgress}
	insertTask(t, first)
	insertTask(t, second)

	first.Quota = 125
	require.NoError(t, first.UpdateQuota())

	var gotFirst, gotSecond Task
	require.NoError(t, DB.First(&gotFirst, first.ID).Error)
	require.NoError(t, DB.First(&gotSecond, second.ID).Error)
	assert.Equal(t, 125, gotFirst.Quota)
	assert.Equal(t, 200, gotSecond.Quota)
}

func TestUpdateQuotaRejectsInvalidOrMissingTask(t *testing.T) {
	truncateTables(t)

	invalid := &Task{Quota: 999}
	err := invalid.UpdateQuota()
	assert.EqualError(t, err, "task id is required for quota update")

	missing := &Task{ID: 987654, Quota: 999}
	err = missing.UpdateQuota()
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestGetTerminalTasksWithPendingQuotaFiltersUnsafeRows(t *testing.T) {
	truncateTables(t)

	now := time.Now().Unix()
	eligible := &Task{
		TaskID:                "terminal-pending-eligible",
		Status:                TaskStatusFailure,
		Quota:                 100,
		SubmitTime:            TaskRefundLegacyCutoff + 1,
		FinishTime:            now,
		BillingReconcileUntil: 0,
	}
	legacy := &Task{
		TaskID:     "terminal-pending-legacy",
		Status:     TaskStatusFailure,
		Quota:      200,
		SubmitTime: TaskRefundLegacyCutoff - 1,
		FinishTime: now,
	}
	noFinish := &Task{
		TaskID:     "terminal-pending-no-finish",
		Status:     TaskStatusFailure,
		Quota:      300,
		SubmitTime: now,
	}
	missingSubmitTime := &Task{
		TaskID:     "terminal-pending-missing-submit-time",
		Status:     TaskStatusFailure,
		Quota:      350,
		SubmitTime: 0,
		FinishTime: now,
	}
	success := &Task{
		TaskID:     "terminal-success-charged",
		Status:     TaskStatusSuccess,
		Quota:      400,
		SubmitTime: now,
		FinishTime: now,
	}
	for _, task := range []*Task{eligible, legacy, noFinish, missingSubmitTime, success} {
		insertTask(t, task)
	}

	tasks := GetTerminalTasksWithPendingQuota(TaskRefundLegacyCutoff, now, 1000)
	require.Len(t, tasks, 3)
	assert.ElementsMatch(t, []int64{eligible.ID, noFinish.ID, missingSubmitTime.ID}, []int64{tasks[0].ID, tasks[1].ID, tasks[2].ID})
	assert.True(t, HasTerminalTasksWithPendingQuota(TaskRefundLegacyCutoff, now))

	// A live lease suppresses the row until it expires.
	eligible.BillingReconcileUntil = now + 60
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", eligible.ID).Update("billing_reconcile_until", eligible.BillingReconcileUntil).Error)
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", noFinish.ID).Update("billing_reconcile_until", now+60).Error)
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", missingSubmitTime.ID).Update("billing_reconcile_until", now+60).Error)
	assert.Empty(t, GetTerminalTasksWithPendingQuota(TaskRefundLegacyCutoff, now, 100))
	assert.False(t, HasTerminalTasksWithPendingQuota(TaskRefundLegacyCutoff, now))
}

func TestGetTerminalTasksWithPendingQuotaReportsDatabaseFailure(t *testing.T) {
	previousDB := DB
	DB = nil
	t.Cleanup(func() { DB = previousDB })

	tasks, err := GetTerminalTasksWithPendingQuotaWithError(TaskRefundLegacyCutoff, time.Now().Unix(), 100)
	require.Error(t, err)
	assert.Nil(t, tasks)
}

func TestTerminalBillingReconciliationDoesNotReplayWhitespaceRequestID(t *testing.T) {
	truncateTables(t)

	now := time.Now().Unix()
	task := &Task{
		TaskID:                "terminal-whitespace-request-id",
		Status:                TaskStatusFailure,
		Quota:                 100,
		SubmitTime:            now,
		FinishTime:            now,
		BillingRequestId:      "   ",
		BillingReconcileState: TaskBillingReconcileProcessing,
		BillingReconcileUntil: now - 1,
	}
	insertTask(t, task)

	tasks, err := GetTerminalTasksWithPendingQuotaWithError(TaskRefundLegacyCutoff, now, 100)
	require.NoError(t, err)
	assert.Empty(t, tasks)

	pending, err := HasTerminalTasksWithPendingQuotaWithError(TaskRefundLegacyCutoff, now)
	require.NoError(t, err)
	assert.False(t, pending)

	claimed, err := task.ClaimBillingReconciliation(now, now+60)
	require.NoError(t, err)
	assert.False(t, claimed)
}

func TestPendingBillingAdjustmentIgnoresWhitespaceRequestID(t *testing.T) {
	truncateTables(t)

	now := time.Now().Unix()
	task := &Task{
		TaskID:                 "adjustment-whitespace-request-id",
		Status:                 TaskStatusSuccess,
		Quota:                  100,
		BillingRequestId:       "   ",
		BillingAdjustmentState: TaskBillingAdjustmentPending,
	}
	insertTask(t, task)

	tasks, err := GetTasksWithPendingBillingAdjustmentWithError(now, 100)
	require.NoError(t, err)
	assert.Empty(t, tasks)

	pending, err := HasPendingBillingAdjustmentsWithError(now)
	require.NoError(t, err)
	assert.False(t, pending)
}

func TestUnfinishedTaskQueriesDoNotTrustProgressAsTerminalState(t *testing.T) {
	truncateTables(t)

	now := time.Now().Unix()
	task := &Task{
		TaskID:     "in-progress-with-complete-progress",
		Status:     TaskStatusInProgress,
		Progress:   "100%",
		SubmitTime: now - 3600,
	}
	insertTask(t, task)

	unfinished, err := GetAllUnFinishSyncTasksWithError(100)
	require.NoError(t, err)
	require.Len(t, unfinished, 1)
	assert.Equal(t, task.ID, unfinished[0].ID)

	hasUnfinished, err := HasUnfinishedSyncTasksWithError()
	require.NoError(t, err)
	assert.True(t, hasUnfinished)

	timedOut, err := GetTimedOutUnfinishedTasksWithError(now, 100)
	require.NoError(t, err)
	require.Len(t, timedOut, 1)
	assert.Equal(t, task.ID, timedOut[0].ID)
}

func TestUnfinishedTaskPollingBatchesRotateBeyondLimit(t *testing.T) {
	truncateTables(t)

	tasks := make([]*Task, 0, 3)
	for i := 0; i < 3; i++ {
		task := &Task{
			TaskID:   fmt.Sprintf("polling-rotation-%d", i),
			Status:   TaskStatusInProgress,
			Progress: "25%",
		}
		insertTask(t, task)
		tasks = append(tasks, task)
	}

	firstBatch, err := GetAllUnFinishSyncTasksWithError(2)
	require.NoError(t, err)
	require.Len(t, firstBatch, 2)
	assert.Equal(t, []int64{tasks[0].ID, tasks[1].ID}, []int64{firstBatch[0].ID, firstBatch[1].ID})

	secondBatch, err := GetAllUnFinishSyncTasksWithError(2)
	require.NoError(t, err)
	require.Len(t, secondBatch, 2)
	assert.Equal(t, tasks[2].ID, secondBatch[0].ID, "the row beyond the first limit must be polled on the next pass")
}

func TestTaskPollingCursorSchemaIsIndexedAndNonNull(t *testing.T) {
	statement := &gorm.Statement{DB: DB}
	require.NoError(t, statement.Parse(&Task{}))
	field := statement.Schema.LookUpField("LastPolledAt")
	require.NotNil(t, field)
	assert.True(t, field.NotNull)
	assert.Equal(t, "0", field.DefaultValue)
	assert.True(t, DB.Migrator().HasIndex(&Task{}, "LastPolledAt"))
}

func TestClaimBillingReconciliationUsesCAS(t *testing.T) {
	truncateTables(t)

	now := time.Now().Unix()
	task := &Task{
		TaskID:     "terminal-pending-cas",
		Status:     TaskStatusFailure,
		Quota:      777,
		SubmitTime: now,
		FinishTime: now,
	}
	insertTask(t, task)

	const workers = 8
	wins := make(chan bool, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			candidate := &Task{ID: task.ID, Status: TaskStatusFailure, Quota: task.Quota}
			won, err := candidate.ClaimBillingReconciliation(now, now+120)
			if err != nil {
				won = false
			}
			wins <- won
		}()
	}
	close(start)
	wg.Wait()
	close(wins)

	winCount := 0
	for won := range wins {
		if won {
			winCount++
		}
	}
	assert.Equal(t, 1, winCount, "exactly one worker should claim the billing lease")

	var reloaded Task
	require.NoError(t, DB.First(&reloaded, task.ID).Error)
	assert.Equal(t, now+120, reloaded.BillingReconcileUntil)
	// The same lease cannot be stolen before expiry, even with a fresh object.
	second, err := (&Task{ID: task.ID, Quota: task.Quota}).ClaimBillingReconciliation(now, now+240)
	require.NoError(t, err)
	assert.False(t, second)
}

func TestClaimBillingUsageTreatsNullMarkerAsUnrecorded(t *testing.T) {
	truncateTables(t)

	task := &Task{TaskID: "task_usage_null_marker", Status: TaskStatusSubmitted}
	insertTask(t, task)
	// Simulate a row written by a deployment whose newly-added boolean column
	// was nullable (or an import that preserved NULL). SQL's `= false` alone
	// would never match this row.
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).
		Update("billing_usage_recorded", nil).Error)

	claimed, err := task.ClaimBillingUsage()
	require.NoError(t, err)
	assert.True(t, claimed)

	var reloaded Task
	require.NoError(t, DB.First(&reloaded, task.ID).Error)
	assert.True(t, reloaded.BillingUsageRecorded)
	claimed, err = reloaded.ClaimBillingUsage()
	require.NoError(t, err)
	assert.False(t, claimed, "a second claim must remain fenced")
}

func TestClaimBillingAdjustmentUsageTreatsNullMarkerAsUnrecorded(t *testing.T) {
	truncateTables(t)

	task := &Task{TaskID: "task_adjustment_usage_null_marker", Status: TaskStatusSuccess}
	insertTask(t, task)
	require.NoError(t, DB.Model(&Task{}).Where("id = ?", task.ID).
		Update("billing_adjustment_usage_recorded", nil).Error)

	claimed, err := task.ClaimBillingAdjustmentUsage()
	require.NoError(t, err)
	assert.True(t, claimed)

	var reloaded Task
	require.NoError(t, DB.First(&reloaded, task.ID).Error)
	assert.True(t, reloaded.BillingAdjustmentUsageRecorded)
	claimed, err = reloaded.ClaimBillingAdjustmentUsage()
	require.NoError(t, err)
	assert.False(t, claimed, "a second adjustment usage claim must remain fenced")
}

func TestUpdateWithStatus_Lose(t *testing.T) {
	truncateTables(t)

	task := &Task{
		TaskID: "task_cas_lose",
		Status: TaskStatusFailure,
		Data:   json.RawMessage(`{}`),
	}
	insertTask(t, task)

	task.Status = TaskStatusSuccess
	won, err := task.UpdateWithStatus(TaskStatusInProgress) // wrong fromStatus
	require.NoError(t, err)
	assert.False(t, won)

	var reloaded Task
	require.NoError(t, DB.First(&reloaded, task.ID).Error)
	assert.EqualValues(t, TaskStatusFailure, reloaded.Status) // unchanged
}

func TestUpdateWithStatus_ConcurrentWinner(t *testing.T) {
	truncateTables(t)

	task := &Task{
		TaskID: "task_cas_race",
		Status: TaskStatusInProgress,
		Quota:  1000,
		Data:   json.RawMessage(`{}`),
	}
	insertTask(t, task)

	const goroutines = 5
	wins := make([]bool, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			t := &Task{}
			*t = Task{
				ID:       task.ID,
				TaskID:   task.TaskID,
				Status:   TaskStatusSuccess,
				Progress: "100%",
				Quota:    task.Quota,
				Data:     json.RawMessage(`{}`),
			}
			t.CreatedAt = task.CreatedAt
			t.UpdatedAt = time.Now().Unix()
			won, err := t.UpdateWithStatus(TaskStatusInProgress)
			if err == nil {
				wins[idx] = won
			}
		}(i)
	}
	wg.Wait()

	winCount := 0
	for _, w := range wins {
		if w {
			winCount++
		}
	}
	assert.Equal(t, 1, winCount, "exactly one goroutine should win the CAS")
}

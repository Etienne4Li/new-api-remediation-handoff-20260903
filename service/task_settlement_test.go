package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFinalizePendingTaskBillingAdjustmentIsDurableAndIdempotent(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID = 9210, 9210, 9210
	const submitQuota, finalQuota = 120, 80
	seedUser(t, userID, 10_000-submitQuota)
	seedToken(t, tokenID, userID, "task-adjustment-token", 10_000-submitQuota)
	seedChannel(t, channelID)
	// Submit-time usage was already recorded before the terminal adjustment.
	seedChargedAccounting(t, userID, channelID, tokenID, submitQuota, 1)
	task := makeTask(userID, channelID, submitQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task-terminal-adjustment"
	task.Status = model.TaskStatusSuccess
	task.Progress = "100%"
	task.BillingRequestId = "task-terminal-adjustment-request"
	task.BillingSettlementQuota = submitQuota
	task.BillingFinalQuota = finalQuota
	task.BillingSettlementState = model.TaskBillingSettlementComplete
	task.BillingAdjustmentState = model.TaskBillingAdjustmentPending
	task.BillingAdjustmentReason = "provider final usage"
	require.NoError(t, model.DB.Create(task).Error)

	// Two concurrent observers may both see the terminal response. Only one
	// may apply the non-idempotent usage delta; the journal makes the financial
	// mutation a no-op for the loser/retry.
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = FinalizePendingTaskBillingAdjustment(context.Background(), &model.Task{ID: task.ID, BillingAdjustmentState: model.TaskBillingAdjustmentPending})
		}()
	}
	wg.Wait()
	// A loser can observe the winner's lease; the next scheduler pass completes
	// any remaining marker transition.
	var refreshed model.Task
	require.NoError(t, model.DB.First(&refreshed, task.ID).Error)
	if refreshed.BillingAdjustmentState != model.TaskBillingAdjustmentComplete {
		refreshed.BillingAdjustmentUntil = time.Now().Add(-time.Minute).Unix()
		require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{
			"billing_adjustment_until": refreshed.BillingAdjustmentUntil,
		}).Error)
		require.NoError(t, FinalizePendingTaskBillingAdjustment(context.Background(), &refreshed))
	}

	require.NoError(t, FinalizePendingTaskBillingAdjustment(context.Background(), &refreshed))
	var got model.Task
	require.NoError(t, model.DB.First(&got, task.ID).Error)
	assert.Equal(t, model.TaskBillingAdjustmentComplete, got.BillingAdjustmentState)
	assert.True(t, got.BillingAdjustmentUsageRecorded)
	assert.Equal(t, finalQuota, got.Quota)
	assert.Equal(t, 10_000-finalQuota, getUserQuota(t, userID))
	assert.Equal(t, 10_000-finalQuota, getTokenRemainQuota(t, tokenID))
	used, requests := getUserUsageAccounting(t, userID)
	assert.Equal(t, finalQuota, used)
	assert.Equal(t, 1, requests)
	assert.Equal(t, int64(finalQuota), getChannelUsedQuota(t, channelID))
	assert.Equal(t, int64(1), countLogs(t), "terminal adjustment log must be recorded once")

	var op model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", task.BillingRequestId, "terminal_settle").First(&op).Error)
	assert.Equal(t, model.BillingOperationApplied, op.Status)
}

// A terminal adjustment whose final amount equals the submit amount still
// needs a durable financial companion.  The usage marker must not be the only
// proof that the terminal state was evaluated, otherwise a crash/replay can
// leave an orphan marker with no lifecycle identity.
func TestFinalizePendingTaskBillingAdjustmentZeroDeltaPersistsFinancialMarker(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID = 9214, 9214, 9214
	const quota = 120
	seedUser(t, userID, 10_000-quota)
	seedToken(t, tokenID, userID, "task-adjustment-zero-delta", 10_000-quota)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, quota, 1)

	task := makeTask(userID, channelID, quota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task-terminal-adjustment-zero-delta"
	task.Status = model.TaskStatusSuccess
	task.Progress = "100%"
	task.BillingRequestId = "task-terminal-adjustment-zero-delta-request"
	task.BillingSettlementQuota = quota
	task.BillingFinalQuota = quota
	task.BillingSettlementState = model.TaskBillingSettlementComplete
	task.BillingAdjustmentState = model.TaskBillingAdjustmentPending
	task.BillingAdjustmentReason = "provider final usage unchanged"
	require.NoError(t, model.DB.Create(task).Error)

	require.NoError(t, FinalizePendingTaskBillingAdjustment(context.Background(), task))

	var financial, usage model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", task.BillingRequestId, "terminal_settle").First(&financial).Error)
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", task.BillingRequestId, "terminal_settle_usage").First(&usage).Error)
	assert.Equal(t, model.BillingOperationApplied, financial.Status)
	assert.Equal(t, model.BillingOperationApplied, usage.Status)
	assert.Equal(t, userID, financial.UserId)
	assert.Equal(t, tokenID, financial.TokenId)
	assert.Zero(t, financial.WalletDelta)
	assert.Zero(t, financial.TokenDelta)
	assert.Equal(t, model.TaskBillingAdjustmentComplete, taskState(t, task.ID).BillingAdjustmentState)
}

func taskState(t *testing.T, id int64) model.Task {
	t.Helper()
	var task model.Task
	require.NoError(t, model.DB.First(&task, id).Error)
	return task
}

func TestFinalizePendingTaskBillingAdjustmentFencesManualSubmitSettlement(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID = 9212, 9212, 9212
	const initialUserQuota, initialTokenQuota = 10_000, 6_000
	const preConsumed, settlementQuota, finalQuota = 80, 120, 100
	seedUser(t, userID, initialUserQuota-preConsumed)
	seedToken(t, tokenID, userID, "task-adjustment-manual-settlement", initialTokenQuota-preConsumed)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Update("used_quota", preConsumed).Error)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, settlementQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task-adjustment-manual-settlement"
	task.Status = model.TaskStatusSuccess
	task.Progress = "100%"
	task.BillingRequestId = "task-adjustment-manual-settlement-request"
	task.BillingPreConsumedQuota = preConsumed
	task.BillingSettlementQuota = settlementQuota
	task.BillingSettlementState = model.TaskBillingSettlementManual
	task.BillingSettlementError = "immutable submit snapshot requires review"
	task.BillingFinalQuota = finalQuota
	task.BillingAdjustmentState = model.TaskBillingAdjustmentPending
	task.BillingAdjustmentReason = "provider final usage"
	require.NoError(t, model.DB.Create(task).Error)

	err := FinalizePendingTaskBillingAdjustment(context.Background(), task)
	require.Error(t, err)

	var got model.Task
	require.NoError(t, model.DB.First(&got, task.ID).Error)
	assert.Equal(t, model.TaskBillingSettlementManual, got.BillingSettlementState)
	assert.Equal(t, model.TaskBillingAdjustmentManual, got.BillingAdjustmentState)
	assert.Equal(t, initialUserQuota-preConsumed, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota-preConsumed, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, countBillingOperations(t, "terminal_settle"))
}

func TestFinalizePendingTaskBillingAdjustmentRebuildsMissingSubmitSettlementState(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID = 9213, 9213, 9213
	const initialUserQuota, initialTokenQuota = 10_000, 6_000
	const preConsumed, settlementQuota, finalQuota = 80, 120, 100
	seedUser(t, userID, initialUserQuota-preConsumed)
	seedToken(t, tokenID, userID, "task-adjustment-missing-settlement-state", initialTokenQuota-preConsumed)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Update("used_quota", preConsumed).Error)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, settlementQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task-adjustment-missing-settlement-state"
	task.Status = model.TaskStatusSuccess
	task.Progress = "100%"
	task.BillingRequestId = "task-adjustment-missing-settlement-state-request"
	task.BillingPreConsumedQuota = preConsumed
	task.BillingSettlementQuota = settlementQuota
	task.BillingSettlementState = ""
	task.BillingFinalQuota = finalQuota
	task.BillingAdjustmentState = model.TaskBillingAdjustmentPending
	task.BillingAdjustmentReason = "provider final usage"
	require.NoError(t, model.DB.Create(task).Error)

	require.NoError(t, FinalizePendingTaskBillingAdjustment(context.Background(), task))

	var got model.Task
	require.NoError(t, model.DB.First(&got, task.ID).Error)
	assert.Equal(t, model.TaskBillingSettlementComplete, got.BillingSettlementState)
	assert.Equal(t, model.TaskBillingAdjustmentComplete, got.BillingAdjustmentState)
	assert.Equal(t, finalQuota, got.Quota)
	assert.Equal(t, initialUserQuota-finalQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota-finalQuota, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, int64(1), countBillingOperations(t, "settle"))
	assert.Equal(t, int64(1), countBillingOperations(t, "terminal_settle"))
}

func TestPrepareTaskBillingAdjustmentTargetIsImmutable(t *testing.T) {
	truncate(t)
	const userID, channelID = 9211, 9211
	seedUser(t, userID, 1000)
	seedChannel(t, channelID)
	task := makeTask(userID, channelID, 100, 0, BillingSourceWallet, 0)
	task.Status = model.TaskStatusSuccess
	task.BillingRequestId = "immutable-terminal-target"
	task.BillingSettlementQuota = 100
	require.NoError(t, model.DB.Create(task).Error)

	prepared, err := PrepareTaskBillingAdjustment(task, nil, nil)
	require.NoError(t, err)
	assert.True(t, prepared)
	require.NoError(t, model.DB.Model(&model.Task{}).Where("id = ?", task.ID).Updates(map[string]any{
		"billing_final_quota":      task.BillingFinalQuota,
		"billing_adjustment_state": task.BillingAdjustmentState,
	}).Error)

	// A later response cannot replace the first target with a different amount.
	task.BillingFinalQuota = 200
	_, err = PrepareTaskBillingAdjustment(task, nil, nil)
	assert.Error(t, err)
}

func TestFinalizePendingTaskBillingIsDurableAndIdempotent(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID = 9201, 9201, 9201
	const preConsumed, finalQuota = 100, 135
	seedUser(t, userID, 10_000-preConsumed)
	seedToken(t, tokenID, userID, "task-settlement-token", 10_000-preConsumed)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, finalQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task-settlement-durable"
	task.Status = model.TaskStatusSubmitted
	task.Progress = "0%"
	task.SubmitTime = time.Now().Unix()
	task.BillingRequestId = "task-settlement-request"
	task.BillingPreConsumedQuota = preConsumed
	task.BillingSettlementQuota = finalQuota
	task.BillingSettlementState = model.TaskBillingSettlementPending
	task.BillingTokenUnlimited = false
	require.NoError(t, model.DB.Create(task).Error)

	// The first pass applies only the delta (35), then records usage and marks
	// the row complete.  Calling it again must be a no-op across all ledgers.
	require.NoError(t, FinalizePendingTaskBilling(context.Background(), task))
	require.NoError(t, FinalizePendingTaskBilling(context.Background(), task))

	var got model.Task
	require.NoError(t, model.DB.First(&got, task.ID).Error)
	assert.Equal(t, model.TaskBillingSettlementComplete, got.BillingSettlementState)
	assert.True(t, got.BillingUsageRecorded)
	assert.Equal(t, 10_000-finalQuota, getUserQuota(t, userID))
	assert.Equal(t, 10_000-finalQuota, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, int64(1), countLogs(t), "usage log must be recorded once")
	used, requests := getUserUsageAccounting(t, userID)
	assert.Equal(t, finalQuota, used)
	assert.Equal(t, 1, requests)
	assert.Equal(t, int64(finalQuota), getChannelUsedQuota(t, channelID))

	var op model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", task.BillingRequestId, "settle").First(&op).Error)
	assert.Equal(t, model.BillingOperationApplied, op.Status)
}

func TestFinalizePendingTaskBillingReclaimsExpiredLease(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID = 9202, 9202, 9202
	seedUser(t, userID, 5_000-100)
	seedToken(t, tokenID, userID, "task-settlement-lease-token", 5_000-100)
	seedChannel(t, channelID)
	task := makeTask(userID, channelID, 100, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task-settlement-expired-lease"
	task.BillingRequestId = "task-settlement-lease-request"
	task.BillingPreConsumedQuota = 100
	task.BillingSettlementQuota = 100
	task.BillingSettlementState = model.TaskBillingSettlementProcessing
	task.BillingSettlementUntil = time.Now().Add(-time.Minute).Unix()
	require.NoError(t, model.DB.Create(task).Error)

	require.NoError(t, FinalizePendingTaskBilling(context.Background(), task))
	var got model.Task
	require.NoError(t, model.DB.First(&got, task.ID).Error)
	assert.Equal(t, model.TaskBillingSettlementComplete, got.BillingSettlementState)
	assert.Equal(t, 5_000-100, getUserQuota(t, userID), "zero delta must not alter wallet")
	assert.Equal(t, int64(1), countLogs(t))
}

// A zero-delta settlement still has an immutable operation identity.  The
// marker must be applied (and retain token/subscription IDs) so a replay cannot
// reinterpret a successful settlement as an abandoned reservation.
func TestFinalizePendingTaskBillingZeroDeltaAppliesMarkerWithIdentity(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID = 9203, 9203, 9203
	seedUser(t, userID, 5_000-100)
	seedToken(t, tokenID, userID, "task-zero-delta-token", 5_000-100)
	seedChannel(t, channelID)
	task := makeTask(userID, channelID, 100, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task-zero-delta-marker"
	task.Status = model.TaskStatusSubmitted
	task.BillingRequestId = "task-zero-delta-marker-request"
	task.BillingPreConsumedQuota = 100
	task.BillingSettlementQuota = 100
	task.BillingSettlementState = model.TaskBillingSettlementPending
	require.NoError(t, model.DB.Create(task).Error)

	require.NoError(t, FinalizePendingTaskBilling(context.Background(), task))
	var op model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", task.BillingRequestId, taskBillingSettlementComponent).First(&op).Error)
	assert.Equal(t, model.BillingOperationApplied, op.Status)
	assert.Equal(t, tokenID, op.TokenId)
	assert.Equal(t, userID, op.UserId)
	assert.Equal(t, int64(0), op.TokenDelta)
	assert.Equal(t, int64(0), op.WalletDelta)
}

func TestTaskSettlementSpecZeroDeltaKeepsSubscriptionIdentity(t *testing.T) {
	task := &model.Task{
		UserId:                  1,
		BillingRequestId:        "zero-subscription-spec",
		BillingPreConsumedQuota: 40,
		BillingSettlementQuota:  40,
		PrivateData: model.TaskPrivateData{
			BillingSource:  BillingSourceSubscription,
			SubscriptionId: 77,
			TokenId:        88,
		},
	}
	spec, err := taskSettlementSpec(task)
	require.NoError(t, err)
	assert.Equal(t, 77, spec.SubscriptionID)
	assert.Equal(t, 88, spec.TokenID)
	assert.Zero(t, spec.SubscriptionDelta)
	assert.Zero(t, spec.TokenDelta)
}

func TestTaskSettlementSpecRejectsMissingImmutableInputs(t *testing.T) {
	task := &model.Task{UserId: 1, Quota: 100, BillingSettlementQuota: 100}
	_, err := taskSettlementSpec(task)
	assert.Error(t, err)

	task.BillingRequestId = "request"
	task.PrivateData.BillingSource = "unknown"
	_, err = taskSettlementSpec(task)
	assert.Error(t, err)
}

func TestTaskSettlementSpecPlaygroundSkipsTokenLedger(t *testing.T) {
	task := &model.Task{
		UserId:                  1,
		BillingRequestId:        "playground-request",
		BillingPreConsumedQuota: 10,
		BillingSettlementQuota:  20,
		BillingPlayground:       true,
		PrivateData: model.TaskPrivateData{
			BillingSource: BillingSourceWallet,
			TokenId:       2,
		},
	}
	spec, err := taskSettlementSpec(task)
	require.NoError(t, err)
	assert.Equal(t, int64(-10), spec.WalletDelta)
	assert.Zero(t, spec.TokenDelta)
}

func TestTaskBillingAdjustmentSpecRejectsOutOfRangeSnapshots(t *testing.T) {
	for name, task := range map[string]*model.Task{
		"final quota above single-request limit": {
			UserId:                 1,
			PrivateData:            model.TaskPrivateData{BillingSource: BillingSourceWallet, TokenId: 1},
			BillingRequestId:       "adjustment-limit-final",
			BillingSettlementQuota: common.MaxQuota,
			BillingFinalQuota:      common.MaxQuota + 1,
			BillingTokenUnlimited:  true,
		},
		"settlement quota above single-request limit": {
			UserId:                 1,
			PrivateData:            model.TaskPrivateData{BillingSource: BillingSourceWallet, TokenId: 1},
			BillingRequestId:       "adjustment-limit-settlement",
			BillingSettlementQuota: common.MaxQuota + 1,
			BillingFinalQuota:      common.MaxQuota,
			BillingTokenUnlimited:  true,
		},
		"machine integer overflow cannot wrap into a valid delta": {
			UserId:                 1,
			PrivateData:            model.TaskPrivateData{BillingSource: BillingSourceWallet, TokenId: 1},
			BillingRequestId:       "adjustment-int-overflow",
			BillingSettlementQuota: 1,
			BillingFinalQuota:      int(^uint(0) >> 1),
			BillingTokenUnlimited:  true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := taskBillingAdjustmentSpec(task)
			require.Error(t, err)
		})
	}
}

func TestRecordTaskBillingUsageOnceWithoutTaskRowIsDurable(t *testing.T) {
	truncate(t)
	const userID, channelID, quota = 9230, 9230, 640
	seedUser(t, userID, 10_000)
	seedChannel(t, channelID)

	requestID := "accepted-task-without-local-row"
	// The usage helper is intentionally not allowed to invent a financial
	// operation from (requestID, userID, quota): that tuple does not identify
	// whether the request used wallet, token, or subscription funding. Simulate
	// the durable marker written before the task INSERT; the helper must replay
	// this exact pending settlement before applying usage.
	require.NoError(t, model.EnsureBillingOperation(model.BillingOperationSpec{
		RequestID:   requestID,
		Component:   taskBillingSettlementComponent,
		UserID:      userID,
		WalletDelta: -quota,
	}))
	require.NoError(t, RecordTaskBillingUsageOnce(requestID, userID, channelID, quota))
	// A retry after a later task insert (or process restart) must not double
	// increment the informational aggregates.
	require.NoError(t, RecordTaskBillingUsageOnce(requestID, userID, channelID, quota))
	assert.Equal(t, 10_000-quota, getUserQuota(t, userID), "the persisted settlement companion is applied exactly once")
	used, requests := getUserUsageAccounting(t, userID)
	assert.Equal(t, quota, used)
	assert.Equal(t, 1, requests)
	assert.Equal(t, int64(quota), getChannelUsedQuota(t, channelID))

	var op model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", requestID, "settle_usage").First(&op).Error)
	assert.Equal(t, model.BillingOperationApplied, op.Status)
	var financial model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", requestID, taskBillingSettlementComponent).First(&financial).Error)
	assert.Equal(t, model.BillingOperationApplied, financial.Status)
}

// A usage-only call cannot safely proceed when the accepted request has no
// durable settlement companion. Keep the model invariant visible at the
// service seam and, importantly, do not leave a pending usage marker behind.
func TestRecordTaskBillingUsageOnceRejectsOrphanUsage(t *testing.T) {
	truncate(t)
	const userID, channelID, quota = 9233, 9233, 640
	seedUser(t, userID, 10_000)
	seedChannel(t, channelID)

	err := RecordTaskBillingUsageOnce("accepted-task-without-settlement", userID, channelID, quota)
	require.ErrorIs(t, err, model.ErrBillingOperationConflict)
	assert.Equal(t, 10_000, getUserQuota(t, userID))
	used, requests := getUserUsageAccounting(t, userID)
	assert.Zero(t, used)
	assert.Zero(t, requests)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	assert.Zero(t, countBillingOperations(t, taskBillingSettlementUsageComponent))
}

func TestReconcilePendingTaskBillingOperationsDoesNotApplyOrphanUsageMarker(t *testing.T) {
	truncate(t)
	const userID, channelID, quota = 9231, 9231, 640
	seedUser(t, userID, 1000)
	seedChannel(t, channelID)
	requestID := "orphan-settle-usage"
	require.NoError(t, model.EnsureBillingOperation(model.BillingOperationSpec{
		RequestID:             requestID,
		Component:             "settle_usage",
		UserID:                userID,
		UserUsedQuotaDelta:    quota,
		UserRequestCountDelta: 1,
		ChannelID:             channelID,
		ChannelUsedQuotaDelta: int64(quota),
	}))

	candidates, completed, pending := ReconcilePendingTaskBillingOperations(context.Background(), 100)
	assert.Equal(t, 1, candidates)
	assert.Zero(t, completed)
	assert.Equal(t, 1, pending)
	used, requests := getUserUsageAccounting(t, userID)
	assert.Zero(t, used)
	assert.Zero(t, requests)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	var op model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", requestID, "settle_usage").First(&op).Error)
	assert.Equal(t, model.BillingOperationPending, op.Status)
}

func TestReconcilePendingTaskBillingOperationsPrioritizesFinancialRowsBeforeLimit(t *testing.T) {
	truncate(t)
	const userID = 9232
	seedUser(t, userID, 100)

	for _, requestID := range []string{"orphan-usage-before-financial-1", "orphan-usage-before-financial-2"} {
		require.NoError(t, model.EnsureBillingOperation(model.BillingOperationSpec{
			RequestID:             requestID,
			Component:             taskBillingSettlementUsageComponent,
			UserID:                userID,
			UserUsedQuotaDelta:    10,
			UserRequestCountDelta: 1,
		}))
	}
	financialRequestID := "financial-after-orphan-usage"
	owner := &model.Task{
		TaskID:                  "financial-after-orphan-usage-task",
		UserId:                  userID,
		Quota:                   10,
		BillingRequestId:        financialRequestID,
		BillingSettlementQuota:  10,
		BillingSettlementState:  model.TaskBillingSettlementPending,
		BillingPlayground:       true,
		BillingTokenUnlimited:   false,
		BillingPreConsumedQuota: 0,
	}
	require.NoError(t, owner.InsertWithBillingFence())
	require.NoError(t, model.EnsureBillingOperation(model.BillingOperationSpec{
		RequestID:   financialRequestID,
		Component:   taskBillingSettlementComponent,
		UserID:      userID,
		WalletDelta: -10,
	}))

	candidates, completed, pending := ReconcilePendingTaskBillingOperations(context.Background(), 2)
	assert.Equal(t, 2, candidates)
	assert.Equal(t, 1, completed)
	assert.Equal(t, 1, pending)
	assert.Equal(t, 90, getUserQuota(t, userID))

	var financial model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", financialRequestID, taskBillingSettlementComponent).First(&financial).Error)
	assert.Equal(t, model.BillingOperationApplied, financial.Status)
}

func TestReconcilePendingTaskBillingOperationsRotatesBlockedFinancialRows(t *testing.T) {
	truncate(t)
	const userID = 9234
	seedUser(t, userID, 100)
	for _, requestID := range []string{"blocked-financial-1", "blocked-financial-2"} {
		require.NoError(t, model.EnsureBillingOperation(model.BillingOperationSpec{
			RequestID:   requestID,
			Component:   taskBillingSettlementComponent,
			UserID:      userID,
			WalletDelta: -10,
		}))
	}

	validRequestID := "valid-financial-after-blocked-rows"
	owner := &model.Task{
		TaskID:                 "valid-financial-after-blocked-task",
		UserId:                 userID,
		Quota:                  10,
		BillingRequestId:       validRequestID,
		BillingSettlementQuota: 10,
		BillingSettlementState: model.TaskBillingSettlementPending,
		BillingPlayground:      true,
	}
	require.NoError(t, owner.InsertWithBillingFence())
	require.NoError(t, model.EnsureBillingOperation(model.BillingOperationSpec{
		RequestID:   validRequestID,
		Component:   taskBillingSettlementComponent,
		UserID:      userID,
		WalletDelta: -10,
	}))

	// The first bounded page contains only orphan markers. They remain pending,
	// but their retry timestamps move behind untouched work.
	candidates, completed, pending := ReconcilePendingTaskBillingOperations(context.Background(), 2)
	assert.Equal(t, 2, candidates)
	assert.Zero(t, completed)
	assert.Equal(t, 2, pending)
	assert.Equal(t, 100, getUserQuota(t, userID))

	// The next page must reach the valid task instead of selecting the same two
	// permanently blocked rows forever.
	candidates, completed, pending = ReconcilePendingTaskBillingOperations(context.Background(), 2)
	assert.Equal(t, 2, candidates)
	assert.Equal(t, 1, completed)
	assert.Equal(t, 1, pending)
	assert.Equal(t, 90, getUserQuota(t, userID))
	var financial model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", validRequestID, taskBillingSettlementComponent).First(&financial).Error)
	assert.Equal(t, model.BillingOperationApplied, financial.Status)
}

// A provider can report FAILURE before the submit handler has committed its
// post-submit settlement.  Refunding task.Quota at that point would reverse
// the final amount before the pending settlement delta is applied, producing a
// net over-refund (or a later charge against the wrong baseline).  The failure
// path must finish the durable submit settlement first, then refund exactly the
// final task amount.
func TestFailedTaskRefundWaitsForPendingSubmitSettlement(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID = 9220, 9220, 9220
	const initialUserQuota, initialTokenQuota = 10_000, 6_000
	const preConsumed, finalQuota = 80, 120
	seedUser(t, userID, initialUserQuota-preConsumed)
	seedToken(t, tokenID, userID, "task-refund-after-settlement", initialTokenQuota-preConsumed)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Update("used_quota", preConsumed).Error)
	seedChannel(t, channelID)
	// The submit path has pre-consumed the wallet/token ledgers, but it records
	// usage counters only after the durable settlement succeeds.

	task := makeTask(userID, channelID, finalQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task-refund-after-pending-settlement"
	task.Status = model.TaskStatusFailure
	task.Progress = "100%"
	task.SubmitTime = time.Now().Unix()
	task.FinishTime = task.SubmitTime
	task.BillingRequestId = "task-refund-after-pending-settlement-request"
	task.BillingPreConsumedQuota = preConsumed
	task.BillingSettlementQuota = finalQuota
	task.BillingSettlementState = model.TaskBillingSettlementPending
	require.NoError(t, model.DB.Create(task).Error)

	require.True(t, claimAndRefundTaskQuota(context.Background(), task, "upstream failure"))

	var got model.Task
	require.NoError(t, model.DB.First(&got, task.ID).Error)
	assert.Equal(t, model.TaskBillingSettlementComplete, got.BillingSettlementState)
	assert.Equal(t, model.TaskBillingReconcileComplete, got.BillingReconcileState)
	assert.Zero(t, got.Quota)
	assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Zero(t, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Equal(t, int64(0), getChannelUsedQuota(t, channelID))
}

func TestFailedTaskRefundRebuildsMissingSubmitSettlementState(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID = 9223, 9223, 9223
	const initialUserQuota, initialTokenQuota = 10_000, 6_000
	const preConsumed, finalQuota = 80, 120
	seedUser(t, userID, initialUserQuota-preConsumed)
	seedToken(t, tokenID, userID, "task-refund-missing-settlement-state", initialTokenQuota-preConsumed)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Update("used_quota", preConsumed).Error)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, finalQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task-refund-missing-settlement-state"
	task.Status = model.TaskStatusFailure
	task.Progress = "100%"
	task.SubmitTime = time.Now().Unix()
	task.FinishTime = task.SubmitTime
	task.BillingRequestId = "task-refund-missing-settlement-state-request"
	task.BillingPreConsumedQuota = preConsumed
	task.BillingSettlementQuota = finalQuota
	task.BillingSettlementState = ""
	require.NoError(t, model.DB.Create(task).Error)

	require.True(t, claimAndRefundTaskQuota(context.Background(), task, "upstream failure"))

	var got model.Task
	require.NoError(t, model.DB.First(&got, task.ID).Error)
	assert.Equal(t, model.TaskBillingSettlementComplete, got.BillingSettlementState)
	assert.Equal(t, model.TaskBillingReconcileComplete, got.BillingReconcileState)
	assert.Zero(t, got.Quota)
	assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Zero(t, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
}

// The scheduled reconciler calls RefundTaskQuota after claiming its own lease
// (rather than going through claimAndRefundTaskQuota).  Keep a regression seam
// for that path too, so a future refactor cannot reintroduce the ordering bug.
func TestTerminalReconcilerDoesNotRefundBeforeSubmitSettlement(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID = 9221, 9221, 9221
	const initialUserQuota, initialTokenQuota = 10_000, 6_000
	const preConsumed, finalQuota = 80, 120
	seedUser(t, userID, initialUserQuota-preConsumed)
	seedToken(t, tokenID, userID, "terminal-reconcile-settlement-order", initialTokenQuota-preConsumed)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Update("used_quota", preConsumed).Error)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, finalQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "terminal-reconcile-settlement-order"
	task.Status = model.TaskStatusFailure
	task.Progress = "100%"
	task.SubmitTime = time.Now().Unix()
	task.FinishTime = task.SubmitTime
	task.BillingRequestId = "terminal-reconcile-settlement-order-request"
	task.BillingPreConsumedQuota = preConsumed
	task.BillingSettlementQuota = finalQuota
	task.BillingSettlementState = model.TaskBillingSettlementPending
	require.NoError(t, model.DB.Create(task).Error)

	candidates, refunded, pending := reconcileTerminalTaskBilling(context.Background(), 100)
	assert.Equal(t, 1, candidates)
	assert.Equal(t, 1, refunded)
	assert.Zero(t, pending)
	assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Zero(t, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
}

func TestTerminalReconcilerFencesManualSubmitSettlement(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID = 9224, 9224, 9224
	const initialUserQuota, initialTokenQuota, taskQuota = 10_000, 6_000, 120
	seedUser(t, userID, initialUserQuota-taskQuota)
	seedToken(t, tokenID, userID, "terminal-reconcile-manual-settlement", initialTokenQuota-taskQuota)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, taskQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "terminal-reconcile-manual-settlement"
	task.Status = model.TaskStatusFailure
	task.Progress = "100%"
	task.SubmitTime = time.Now().Unix()
	task.FinishTime = task.SubmitTime
	task.BillingRequestId = "terminal-reconcile-manual-settlement-request"
	task.BillingPreConsumedQuota = taskQuota
	task.BillingSettlementQuota = taskQuota
	task.BillingSettlementState = model.TaskBillingSettlementManual
	task.BillingReconcileState = model.TaskBillingReconcilePending
	require.NoError(t, model.DB.Create(task).Error)

	candidates, refunded, pending := reconcileTerminalTaskBilling(context.Background(), 100)
	assert.Equal(t, 1, candidates)
	assert.Zero(t, refunded)
	assert.Equal(t, 1, pending)

	var got model.Task
	require.NoError(t, model.DB.First(&got, task.ID).Error)
	assert.Equal(t, model.TaskBillingReconcileManual, got.BillingReconcileState)
	assert.Equal(t, taskQuota, got.Quota)
	assert.Equal(t, initialUserQuota-taskQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota-taskQuota, getTokenRemainQuota(t, tokenID))

	// A manual settlement baseline cannot become retryable merely because its
	// short refund lease expired. It must stay out of the automatic queue until
	// an operator repairs and explicitly requeues it.
	candidates, refunded, pending = reconcileTerminalTaskBilling(context.Background(), 100)
	assert.Zero(t, candidates)
	assert.Zero(t, refunded)
	assert.Zero(t, pending)
}

// A trusted wallet request can legitimately have a zero pre-consume amount
// while still carrying a positive final task charge.  If the settlement state
// was lost during an older schema rollout, finalization must reconstruct the
// pending state from the immutable settlement snapshot instead of treating
// BillingPreConsumedQuota == 0 as a free task.
func TestFinalizePendingTaskBillingChargesZeroPreconsumePaidTask(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID = 9222, 9222, 9222
	const initialUserQuota, initialTokenQuota, finalQuota = 10_000, 6_000, 275
	seedUser(t, userID, initialUserQuota)
	seedToken(t, tokenID, userID, "task-zero-preconsume", initialTokenQuota)
	seedChannel(t, channelID)

	task := makeTask(userID, channelID, finalQuota, tokenID, BillingSourceWallet, 0)
	task.TaskID = "task-zero-preconsume-paid"
	task.Status = model.TaskStatusSubmitted
	task.Progress = "0%"
	task.BillingRequestId = "task-zero-preconsume-paid-request"
	task.BillingPreConsumedQuota = 0
	task.BillingSettlementQuota = finalQuota
	// Simulate a row written by a pre-state-machine deployment: the immutable
	// request/settlement snapshot exists, but the state column is empty.
	task.BillingSettlementState = ""
	require.NoError(t, model.DB.Create(task).Error)

	require.NoError(t, FinalizePendingTaskBilling(context.Background(), task))

	var got model.Task
	require.NoError(t, model.DB.First(&got, task.ID).Error)
	assert.Equal(t, model.TaskBillingSettlementComplete, got.BillingSettlementState)
	assert.Equal(t, finalQuota, got.Quota)
	assert.Equal(t, initialUserQuota-finalQuota, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota-finalQuota, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, finalQuota, getTokenUsedQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Equal(t, finalQuota, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Equal(t, int64(finalQuota), getChannelUsedQuota(t, channelID))
}

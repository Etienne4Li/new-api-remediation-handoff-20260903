package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A modern task's immutable settlement snapshot is authoritative even when
// the amount is zero.  In particular, a stale/non-zero Task.Quota must not be
// used as a fallback and inflate informational usage counters.
func TestRecordTaskConsumptionOnceModernZeroSnapshotDoesNotFallbackToTaskQuota(t *testing.T) {
	truncate(t)
	const userID, channelID = 9360, 9360
	seedUser(t, userID, 1000)
	seedChannel(t, channelID)
	task := makeTask(userID, channelID, 25, 0, BillingSourceWallet, 0)
	task.TaskID = "task-modern-zero-snapshot"
	task.BillingRequestId = "modern-zero-snapshot-request"
	task.BillingPreConsumedQuota = 0
	task.BillingSettlementQuota = 0
	task.BillingSettlementState = model.TaskBillingSettlementComplete
	require.NoError(t, model.DB.Create(task).Error)
	// A COMPLETE modern task must have a durable financial companion. The
	// zero-delta marker is the proof that settlement was evaluated; usage is
	// still recorded exactly once even though no quota was consumed. Keep the
	// marker pending here to exercise the recovery seam: the usage helper must
	// replay the exact persisted companion before applying its own operation.
	require.NoError(t, model.EnsureBillingOperation(model.BillingOperationSpec{
		RequestID: task.BillingRequestId,
		Component: taskBillingSettlementComponent,
		UserID:    userID,
	}))

	require.NoError(t, RecordTaskConsumptionOnce(context.Background(), task))
	used, requests := getUserUsageAccounting(t, userID)
	assert.Zero(t, used)
	assert.Equal(t, 1, requests)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	var financial model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", task.BillingRequestId, taskBillingSettlementComponent).First(&financial).Error)
	assert.Equal(t, model.BillingOperationApplied, financial.Status)
}

// A non-zero reservation makes a zero final amount unambiguous: all reserved
// quota may legitimately be returned when the provider reports no billable
// work. The mutable task quota must still not be used as a usage fallback.
func TestRecordTaskConsumptionOnceModernZeroFinalQuotaAfterPreconsume(t *testing.T) {
	truncate(t)
	const userID, channelID = 9365, 9365
	seedUser(t, userID, 1000)
	seedChannel(t, channelID)
	task := makeTask(userID, channelID, 40, 0, BillingSourceWallet, 0)
	task.TaskID = "task-modern-zero-final-after-preconsume"
	task.BillingRequestId = "modern-zero-final-after-preconsume-request"
	task.BillingPreConsumedQuota = 40
	task.BillingSettlementQuota = 0
	task.BillingSettlementState = model.TaskBillingSettlementComplete
	require.NoError(t, model.DB.Create(task).Error)
	require.NoError(t, model.ApplyBillingOperation(model.BillingOperationSpec{
		RequestID: task.BillingRequestId,
		Component: taskBillingSettlementComponent,
		UserID:    userID,
	}))

	require.NoError(t, RecordTaskConsumptionOnce(context.Background(), task))
	used, requests := getUserUsageAccounting(t, userID)
	assert.Zero(t, used)
	assert.Equal(t, 1, requests)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
}

// A request-scoped task row with a positive Task.Quota but no settlement
// snapshot is ambiguous.  Finalization must fence it for manual review before
// creating/applying a zero-charge marker; guessing the fallback amount would
// either charge the wrong ledger or silently lose the intended charge.
func TestFinalizePendingTaskBillingMissingModernSnapshotIsManual(t *testing.T) {
	truncate(t)
	const userID, channelID = 9361, 9361
	seedUser(t, userID, 1000)
	seedChannel(t, channelID)
	task := makeTask(userID, channelID, 125, 0, BillingSourceWallet, 0)
	task.TaskID = "task-modern-missing-snapshot"
	task.BillingRequestId = "modern-missing-snapshot-request"
	task.BillingPreConsumedQuota = 0
	task.BillingSettlementQuota = 0
	task.BillingSettlementState = model.TaskBillingSettlementPending
	require.NoError(t, model.DB.Create(task).Error)

	err := FinalizePendingTaskBilling(context.Background(), task)
	require.Error(t, err)
	var got model.Task
	require.NoError(t, model.DB.First(&got, task.ID).Error)
	assert.Equal(t, model.TaskBillingSettlementManual, got.BillingSettlementState)
	assert.Equal(t, 1000, getUserQuota(t, userID))
	used, _ := getUserUsageAccounting(t, userID)
	assert.Zero(t, used)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	assert.Zero(t, countBillingOperations(t, taskBillingSettlementComponent))
	assert.Zero(t, countBillingOperations(t, taskBillingSettlementUsageComponent))
}

// Legacy rows have no request-scoped identity.  Their historical quota field
// remains the only available snapshot, so the old fallback is still allowed.
func TestRecordTaskConsumptionOnceLegacyFallsBackToTaskQuota(t *testing.T) {
	truncate(t)
	const userID, channelID = 9362, 9362
	seedUser(t, userID, 1000)
	seedChannel(t, channelID)
	task := makeTask(userID, channelID, 75, 0, BillingSourceWallet, 0)
	task.TaskID = "task-legacy-fallback"
	task.BillingRequestId = ""
	task.BillingSettlementQuota = 0
	task.BillingSettlementState = ""
	require.NoError(t, model.DB.Create(task).Error)

	require.NoError(t, RecordTaskConsumptionOnce(context.Background(), task))
	used, requests := getUserUsageAccounting(t, userID)
	assert.Equal(t, 75, used)
	assert.Equal(t, 1, requests)
	assert.Equal(t, int64(75), getChannelUsedQuota(t, channelID))
}

func TestEnsureAsyncTaskBillingOperationsRejectsMissingChannelMeta(t *testing.T) {
	truncate(t)
	const userID = 9363
	seedUser(t, userID, 1000)
	info := &relaycommon.RelayInfo{
		UserId:    userID,
		RequestId: "missing-channel-meta-request",
		PriceData: hosttypes.PriceData{FreeModel: true},
	}

	err := EnsureAsyncTaskBillingOperations(info, 0)
	require.Error(t, err)
	assert.Zero(t, countBillingOperations(t, taskBillingSettlementComponent))
	assert.Zero(t, countBillingOperations(t, taskBillingSettlementUsageComponent))
}

// A zero-valued ChannelMeta is a deliberate “no channel ledger” identity and
// must be normalized consistently in the usage marker instead of panicking on
// the embedded pointer.
func TestEnsureAsyncTaskBillingOperationsNormalizesZeroChannelIdentity(t *testing.T) {
	truncate(t)
	const userID = 9364
	seedUser(t, userID, 1000)
	info := &relaycommon.RelayInfo{
		UserId:      userID,
		RequestId:   "zero-channel-meta-request",
		PriceData:   hosttypes.PriceData{FreeModel: true},
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 0},
	}

	require.NoError(t, EnsureAsyncTaskBillingOperations(info, 0))
	var usage model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", info.RequestId, taskBillingSettlementUsageComponent).First(&usage).Error)
	assert.Zero(t, usage.ChannelId)
	assert.Zero(t, usage.ChannelUsedQuotaDelta)
}

func TestEnsureAsyncTaskBillingOperationsRejectsPaidTaskWithoutSession(t *testing.T) {
	truncate(t)
	const userID = 9366
	seedUser(t, userID, 1000)
	info := &relaycommon.RelayInfo{
		UserId:      userID,
		RequestId:   "paid-without-session-request",
		PriceData:   hosttypes.PriceData{UsePrice: true, ModelPrice: 1},
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 1},
	}

	err := EnsureAsyncTaskBillingOperations(info, 100)
	require.Error(t, err)
	assert.Zero(t, countBillingOperations(t, taskBillingSettlementUsageComponent))
}

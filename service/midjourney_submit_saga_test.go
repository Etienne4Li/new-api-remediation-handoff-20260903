package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An accepted MJ request journals both its financial and usage intent before
// the local row is written. If the row INSERT fails, a fresh worker can apply
// those immutable markers; retrying the INSERT/settlement afterward must not
// charge or count the request twice.
func TestMidjourneySubmitSagaReplaysAfterLocalInsertFailure(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID, quota = 710, 710, 710, 2400
	seedUser(t, userID, 10000)
	seedToken(t, tokenID, userID, "sk-mj-saga", 8000)
	seedChannel(t, channelID)

	info := &relaycommon.RelayInfo{
		RequestId:      "mj-saga-insert-failure",
		UserId:         userID,
		TokenId:        tokenID,
		TokenKey:       "sk-mj-saga",
		TokenUnlimited: false,
		ChannelMeta:    &relaycommon.ChannelMeta{ChannelId: channelID},
	}
	task := &model.Midjourney{
		UserId: userID, MjId: "provider-mj-saga", Action: "IMAGINE",
		ChannelId: channelID, Progress: "0%",
	}

	prepared, err := PrepareMidjourneyTaskBilling(info, task, quota, true)
	require.NoError(t, err)
	require.True(t, prepared)
	assert.Equal(t, info.RequestId, task.BillingRequestId)
	assert.Equal(t, tokenID, task.TokenId, "token identity must be persisted before INSERT")
	assert.Equal(t, channelID, task.BillingChannelId)

	var financial model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", info.RequestId, legacyBillingSettlementComponent).First(&financial).Error)
	assert.Equal(t, model.BillingOperationPending, financial.Status)
	var usage model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", info.RequestId, taskBillingSettlementUsageComponent).First(&usage).Error)
	assert.Equal(t, model.BillingOperationPending, usage.Status)

	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_mj_saga_insert
		BEFORE INSERT ON midjourneys
		BEGIN
			SELECT RAISE(ABORT, 'forced midjourney insert failure');
		END;
	`).Error)
	insertErr := task.InsertWithBillingFence()
	assert.Error(t, insertErr)
	assert.Zero(t, task.Id)
	assert.Equal(t, 10000, getUserQuota(t, userID))
	assert.Equal(t, 8000, getTokenRemainQuota(t, tokenID))

	// The marker transaction is independent from the failed row INSERT and is
	// therefore visible to a fresh reconciliation pass.
	candidates, completed, pending := ReconcilePendingTaskBillingOperations(context.Background(), 100)
	assert.Equal(t, 2, candidates)
	assert.Equal(t, 2, completed)
	assert.Zero(t, pending)
	assert.Equal(t, 10000-quota, getUserQuota(t, userID))
	assert.Equal(t, 8000-quota, getTokenRemainQuota(t, tokenID))
	used, requests := getUserUsageAccounting(t, userID)
	assert.EqualValues(t, quota, used)
	assert.Equal(t, 1, requests)
	assert.EqualValues(t, quota, getChannelUsedQuota(t, channelID))

	require.NoError(t, model.DB.Exec("DROP TRIGGER IF EXISTS fail_mj_saga_insert").Error)
	require.NoError(t, task.InsertWithBillingFence())
	require.NotZero(t, task.Id)

	// Settlement is an idempotent replay of the already-applied markers. It
	// must not alter either financial or informational counters.
	billed, settleErr := SettleMidjourneyTaskBilling(info, task, prepared)
	require.NoError(t, settleErr)
	assert.True(t, billed)
	assert.Equal(t, 10000-quota, getUserQuota(t, userID))
	assert.Equal(t, 8000-quota, getTokenRemainQuota(t, tokenID))
	used, requests = getUserUsageAccounting(t, userID)
	assert.EqualValues(t, quota, used)
	assert.Equal(t, 1, requests)
	assert.EqualValues(t, quota, getChannelUsedQuota(t, channelID))
}

// A request-fence retry that presents a different provider identity must fail
// closed. Replaying the upstream call would otherwise create a second job
// under the same billing marker.
func TestMidjourneySubmitSagaRejectsRequestIdentityReuse(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID, quota = 711, 711, 711, 1200
	seedUser(t, userID, 10000)
	seedToken(t, tokenID, userID, "sk-mj-saga-conflict", 8000)
	seedChannel(t, channelID)
	info := &relaycommon.RelayInfo{
		RequestId: "mj-saga-conflict", UserId: userID, TokenId: tokenID,
		TokenKey: "sk-mj-saga-conflict", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID},
	}
	first := &model.Midjourney{UserId: userID, MjId: "provider-a", Action: "IMAGINE", ChannelId: channelID, Progress: "0%"}
	prepared, err := PrepareMidjourneyTaskBilling(info, first, quota, true)
	require.NoError(t, err)
	require.True(t, prepared)
	require.NoError(t, first.InsertWithBillingFence())

	second := &model.Midjourney{
		UserId: userID, MjId: "provider-b", Action: "IMAGINE", ChannelId: channelID,
		Progress: "0%", Quota: quota, TokenId: tokenID, BillingChannelId: channelID,
		BillingRequestId: info.RequestId,
	}
	assert.ErrorIs(t, second.InsertWithBillingFence(), model.ErrMidjourneyInsertFenceConflict)
	var count int64
	require.NoError(t, model.DB.Model(&model.Midjourney{}).Where("billing_request_id = ?", info.RequestId).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

// Settlement is deliberately a second durable boundary after the task row.
// A funding/database failure must leave the marker pending; the next worker
// then replays the exact operation instead of falling back to independent
// wallet/token writes.
func TestMidjourneySubmitSagaReplaysAfterSettlementFailure(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID, quota = 712, 712, 712, 1800
	seedUser(t, userID, 9000)
	seedToken(t, tokenID, userID, "sk-mj-saga-settle", 7000)
	seedChannel(t, channelID)
	info := &relaycommon.RelayInfo{
		RequestId: "mj-saga-settlement-failure", UserId: userID,
		TokenId: tokenID, TokenKey: "sk-mj-saga-settle",
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID},
	}
	task := &model.Midjourney{
		UserId: userID, MjId: "provider-mj-settle", Action: "IMAGINE",
		ChannelId: channelID, Progress: "0%",
	}
	prepared, err := PrepareMidjourneyTaskBilling(info, task, quota, true)
	require.NoError(t, err)
	require.True(t, prepared)
	require.NoError(t, task.InsertWithBillingFence())

	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_mj_saga_settlement
		BEFORE UPDATE ON users
		WHEN OLD.id = 712
		BEGIN
			SELECT RAISE(ABORT, 'forced midjourney settlement failure');
		END;
	`).Error)
	billed, settleErr := SettleMidjourneyTaskBilling(info, task, prepared)
	assert.False(t, billed)
	assert.Error(t, settleErr)
	assert.Equal(t, 9000, getUserQuota(t, userID))
	assert.Equal(t, 7000, getTokenRemainQuota(t, tokenID))

	var marker model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", info.RequestId, legacyBillingSettlementComponent).First(&marker).Error)
	assert.Equal(t, model.BillingOperationPending, marker.Status)

	require.NoError(t, model.DB.Exec("DROP TRIGGER IF EXISTS fail_mj_saga_settlement").Error)
	candidates, completed, pending := ReconcilePendingTaskBillingOperations(context.Background(), 100)
	assert.Equal(t, 2, candidates)
	assert.Equal(t, 2, completed)
	assert.Zero(t, pending)
	assert.Equal(t, 9000-quota, getUserQuota(t, userID))
	assert.Equal(t, 7000-quota, getTokenRemainQuota(t, tokenID))
	used, requests := getUserUsageAccounting(t, userID)
	assert.EqualValues(t, quota, used)
	assert.Equal(t, 1, requests)
}

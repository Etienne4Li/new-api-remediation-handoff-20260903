package service

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A terminal failure may be persisted before the submit settlement has run
// (for example the process dies after the provider response/status CAS). The
// Midjourney reconciler must finish the submit markers first, then refund the
// exact same request fence, leaving every ledger at its pre-request value.
func TestMidjourneyPendingTerminalRefundSettlesThenRefunds(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID, quota = 603, 603, 603, 1800
	seedUser(t, userID, 10000)
	seedToken(t, tokenID, userID, "sk-mj-terminal-replay", 8000)
	seedChannel(t, channelID)
	info := &relaycommon.RelayInfo{
		RequestId: "mj-terminal-replay",
		UserId:    userID,
		TokenId:   tokenID,
		TokenKey:  "sk-mj-terminal-replay",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelId: channelID,
		},
	}
	task := &model.Midjourney{
		UserId: userID, MjId: "mj-terminal-replay", Action: "IMAGINE",
		ChannelId: channelID, Progress: "0%",
	}
	prepared, err := PrepareMidjourneyTaskBilling(info, task, quota, true)
	require.NoError(t, err)
	require.True(t, prepared)
	require.NoError(t, task.InsertWithBillingFence())

	// The terminal status CAS wins, but no synchronous refund is called before
	// the process is considered dead. Both submit markers are still pending.
	task.Status = "FAILURE"
	task.Progress = "100%"
	task.FailReason = "provider rejected job"
	won, err := task.UpdateWithStatus("")
	require.NoError(t, err)
	// An empty fromStatus is not a valid CAS for the model; persist the terminal
	// fixture directly to model the already-committed status boundary.
	if !won {
		require.NoError(t, model.DB.Model(&model.Midjourney{}).Where("id = ?", task.Id).Updates(map[string]any{
			"status":      "FAILURE",
			"progress":    "100%",
			"fail_reason": task.FailReason,
		}).Error)
	}

	candidates, completed, pending := ReconcilePendingMidjourneyRefunds(ctx, 100)
	assert.Equal(t, 1, candidates)
	assert.Equal(t, 1, completed)
	assert.Zero(t, pending)
	assert.Equal(t, 10000, getUserQuota(t, userID))
	assert.Equal(t, 8000, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Zero(t, usedQuota)
	assert.Equal(t, 1, requestCount, "refund reverses used quota but preserves request count")
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	assert.Zero(t, getMidjourneyTask(t, task.Id).Quota)

	var refund model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", info.RequestId, model.BillingOperationLegacyTaskRefundComponent).First(&refund).Error)
	assert.Equal(t, model.BillingOperationApplied, refund.Status)
	var count int64
	require.NoError(t, model.DB.Model(&model.BillingOperation{}).Where("request_id = ?", info.RequestId).Count(&count).Error)
	assert.Equal(t, int64(3), count, "submit financial + usage + terminal refund share one request fence")
}

// Two pollers may load the same failed row concurrently. BillingOperation is
// the ledger fence and ClearBillingQuota is the log fence; exactly one human
// refund record must be emitted.
func TestMidjourneyPendingTerminalRefundConcurrentPollersLogOnce(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID, quota = 604, 604, 604, 1200
	seedUser(t, userID, 10000)
	seedToken(t, tokenID, userID, "sk-mj-concurrent-refund", 8000)
	seedChannel(t, channelID)
	info := &relaycommon.RelayInfo{
		RequestId: "mj-concurrent-refund", UserId: userID, TokenId: tokenID,
		TokenKey: "sk-mj-concurrent-refund", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID},
	}
	task := &model.Midjourney{UserId: userID, MjId: "mj-concurrent-refund", Action: "IMAGINE", ChannelId: channelID, Progress: "0%"}
	prepared, err := PrepareMidjourneyTaskBilling(info, task, quota, true)
	require.NoError(t, err)
	require.True(t, prepared)
	require.NoError(t, task.InsertWithBillingFence())
	billed, err := SettleMidjourneyTaskBilling(info, task, prepared)
	require.NoError(t, err)
	require.True(t, billed)
	require.NoError(t, model.DB.Model(&model.Midjourney{}).Where("id = ?", task.Id).Updates(map[string]any{
		"status": "FAILURE", "progress": "100%", "fail_reason": "provider failure",
	}).Error)

	const workers = 8
	results := make(chan bool, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			var loaded model.Midjourney
			if loadErr := model.DB.First(&loaded, task.Id).Error; loadErr != nil {
				results <- false
				return
			}
			results <- RefundMidjourneyQuota(ctx, &loaded, fmt.Sprintf("poller-%d", index))
		}(i)
	}
	wg.Wait()
	close(results)
	for result := range results {
		assert.True(t, result)
	}
	assert.Equal(t, 10000, getUserQuota(t, userID))
	assert.Equal(t, 8000, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	assert.Zero(t, getMidjourneyTask(t, task.Id).Quota)
	assert.Equal(t, int64(1), countLogs(t))
}

// A pending legacy-MJ refund marker must be enough for a fresh process to
// replay the complete ledger mutation. The task row is intentionally left
// untouched by the marker reconciler; the normal poller then clears that row
// with the same operation key.
func TestMidjourneyRefundPendingMarkerReplaysAndThenClearsTask(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID, chargedQuota = 601, 601, 601, 3000
	seedUser(t, userID, 7000) // wallet after the original charge
	seedToken(t, tokenID, userID, "sk-mj-replay", 2000)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, chargedQuota, 1)
	task := &model.Midjourney{
		UserId: userID, MjId: "mj-replay", Action: "IMAGINE",
		ChannelId: channelID, BillingChannelId: channelID,
		Quota: chargedQuota, TokenId: tokenID, Progress: "0%",
	}
	require.NoError(t, task.Insert())

	requestID := midjourneyRefundRequestID(task, chargedQuota, channelID)
	spec := model.BillingOperationSpec{
		RequestID:             requestID,
		Component:             model.BillingOperationLegacyTaskRefundComponent,
		UserID:                userID,
		TokenID:               tokenID,
		WalletDelta:           chargedQuota,
		TokenDelta:            -chargedQuota,
		UserUsedQuotaDelta:    -chargedQuota,
		ChannelID:             channelID,
		ChannelUsedQuotaDelta: -chargedQuota,
	}
	require.NoError(t, model.EnsureBillingOperation(spec))

	candidates, completed, pending := ReconcilePendingBillingRefunds(ctx, 100)
	assert.Equal(t, 1, candidates)
	assert.Equal(t, 1, completed)
	assert.Zero(t, pending)
	assert.Equal(t, 10000, getUserQuota(t, userID))
	assert.Equal(t, 5000, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Zero(t, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	assert.Equal(t, chargedQuota, getMidjourneyTask(t, task.Id).Quota,
		"reconciler must not clear the task marker it cannot atomically lock")

	// The polling process retries with the same immutable operation key. The
	// operation is already applied, so this call only repairs the task marker
	// and emits one human-facing refund log.
	require.True(t, RefundMidjourneyQuota(ctx, task, "replay cleanup"))
	assert.Zero(t, getMidjourneyTask(t, task.Id).Quota)
	assert.Equal(t, int64(1), countLogs(t))
	require.True(t, RefundMidjourneyQuota(ctx, task, "duplicate"))
	assert.Equal(t, int64(1), countLogs(t))
}

// If the operation commits but the task-marker write fails, a retry must not
// apply the wallet/token/usage deltas a second time.
func TestMidjourneyRefundMarkerWriteFailureIsIdempotent(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	const userID, tokenID, channelID, chargedQuota = 602, 602, 602, 2000
	seedUser(t, userID, 8000)
	seedToken(t, tokenID, userID, "sk-mj-marker-retry", 3000)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, chargedQuota, 1)
	task := &model.Midjourney{
		UserId: userID, MjId: "mj-marker-retry", Action: "IMAGINE",
		ChannelId: channelID, BillingChannelId: channelID,
		Quota: chargedQuota, TokenId: tokenID, Progress: "0%",
	}
	require.NoError(t, task.Insert())

	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_midjourney_marker_update
		BEFORE UPDATE ON midjourneys
		BEGIN
			SELECT RAISE(ABORT, 'forced marker update failure');
		END;
	`).Error)

	assert.False(t, RefundMidjourneyQuota(ctx, task, "marker unavailable"))
	assert.Equal(t, 10000, getUserQuota(t, userID))
	assert.Equal(t, 5000, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	assert.Equal(t, chargedQuota, getMidjourneyTask(t, task.Id).Quota)
	assert.Zero(t, countLogs(t))

	require.NoError(t, model.DB.Exec("DROP TRIGGER IF EXISTS fail_midjourney_marker_update").Error)
	assert.True(t, RefundMidjourneyQuota(ctx, task, "marker retry"))
	assert.Equal(t, 10000, getUserQuota(t, userID))
	assert.Equal(t, 5000, getTokenRemainQuota(t, tokenID))
	assert.Zero(t, getTokenUsedQuota(t, tokenID))
	assert.Zero(t, getMidjourneyTask(t, task.Id).Quota)
	assert.Equal(t, int64(1), countLogs(t))
}

// A permanently malformed row must not occupy the first slot of every bounded
// refund pass. The durable quota marker stays intact for manual repair, while a
// retry cursor moves the row behind untouched work.
func TestMidjourneyPendingRefundsRotateBlockedRows(t *testing.T) {
	truncate(t)
	ctx := context.Background()

	blocked := &model.Midjourney{
		UserId:     0,
		MjId:       "mj-blocked-refund",
		Action:     "IMAGINE",
		Status:     "FAILURE",
		Progress:   "100%",
		FailReason: "missing billing owner",
		Quota:      900,
	}
	require.NoError(t, blocked.Insert())

	const userID, tokenID, channelID, quota = 605, 605, 605, 1400
	seedUser(t, userID, 10000-quota)
	seedToken(t, tokenID, userID, "sk-mj-refund-rotation", 8000-quota)
	seedChannel(t, channelID)
	seedChargedAccounting(t, userID, channelID, tokenID, quota, 1)
	refundable := &model.Midjourney{
		UserId: userID, MjId: "mj-refund-after-blocked", Action: "IMAGINE",
		ChannelId: channelID, BillingChannelId: channelID, TokenId: tokenID,
		Status: "FAILURE", Progress: "100%", FailReason: "provider rejected job",
		Quota: quota,
	}
	require.NoError(t, refundable.Insert())
	require.Less(t, blocked.Id, refundable.Id)

	candidates, completed, pending, runErr := ReconcilePendingMidjourneyRefundsWithError(ctx, 1)
	assert.Equal(t, 1, candidates)
	assert.Zero(t, completed)
	assert.Equal(t, 1, pending)
	require.Error(t, runErr)
	assert.Equal(t, 900, getMidjourneyTask(t, blocked.Id).Quota)

	candidates, completed, pending, runErr = ReconcilePendingMidjourneyRefundsWithError(ctx, 1)
	assert.Equal(t, 1, candidates)
	assert.Equal(t, 1, completed)
	assert.Zero(t, pending)
	require.NoError(t, runErr)
	assert.Equal(t, 900, getMidjourneyTask(t, blocked.Id).Quota)
	assert.Zero(t, getMidjourneyTask(t, refundable.Id).Quota)
	assert.Equal(t, 10000, getUserQuota(t, userID))
}

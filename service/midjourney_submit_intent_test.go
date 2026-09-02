package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMidjourneySubmitIntentAcceptsInTwoPhasesAndReplays(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID, quota = 730, 730, 730, 1600
	seedUser(t, userID, 10000)
	seedToken(t, tokenID, userID, "sk-mj-intent", 8000)
	seedChannel(t, channelID)
	info := &relaycommon.RelayInfo{
		RequestId: "mj-intent-atomic", UserId: userID, TokenId: tokenID,
		TokenKey: "sk-mj-intent", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID},
	}
	created, err := PrepareMidjourneySubmitIntent(info, "IMAGINE", channelID, quota)
	require.NoError(t, err)
	require.True(t, created)
	task := &model.Midjourney{UserId: userID, MjId: "provider-intent", Action: "IMAGINE", ChannelId: channelID}
	financial, usage, err := midjourneyBillingSpecs(info, task, quota)
	require.NoError(t, err)
	require.NoError(t, model.AcceptMidjourneySubmitIntent(info.RequestId, task.MjId, financial, usage))

	var intent model.MidjourneySubmitIntent
	require.NoError(t, model.DB.Where("request_id = ?", info.RequestId).First(&intent).Error)
	assert.Equal(t, model.MidjourneySubmitIntentAccepted, intent.Status)
	assert.Zero(t, intent.ReconciledAt)
	var marker model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", info.RequestId, legacyBillingSettlementComponent).First(&marker).Error)
	assert.Equal(t, model.BillingOperationPending, marker.Status)

	// A fresh worker can replay accepted intents even if no task row survived.
	// Recovery must recreate a pollable pending row before charging it; treating
	// an unknown provider outcome as SUCCESS would hide failures and defeat the
	// normal refund lifecycle.
	candidates, completed, pending := ReconcilePendingMidjourneySubmitIntents(context.Background(), 100)
	assert.Equal(t, 1, candidates)
	assert.Equal(t, 1, completed)
	assert.Zero(t, pending)
	assert.Equal(t, 10000-quota, getUserQuota(t, userID))
	assert.Equal(t, 8000-quota, getTokenRemainQuota(t, tokenID))
	used, requests := getUserUsageAccounting(t, userID)
	assert.EqualValues(t, quota, used)
	assert.Equal(t, 1, requests)
	assert.EqualValues(t, quota, getChannelUsedQuota(t, channelID))

	require.NoError(t, model.DB.Where("request_id = ?", info.RequestId).First(&intent).Error)
	assert.NotZero(t, intent.ReconciledAt)
	assert.Equal(t, model.BillingOperationApplied, getBillingOperationStatus(t, info.RequestId, legacyBillingSettlementComponent))
	assert.Equal(t, model.BillingOperationApplied, getBillingOperationStatus(t, info.RequestId, taskBillingSettlementUsageComponent))
	recovered, err := model.FindByBillingRequestID(info.RequestId)
	require.NoError(t, err)
	assert.Equal(t, task.MjId, recovered.MjId)
	assert.Equal(t, info.UserId, recovered.UserId)
	assert.Equal(t, info.RequestId, recovered.BillingRequestId)
	assert.Equal(t, quota, recovered.Quota)
	assert.Equal(t, channelID, recovered.ChannelId)
	assert.Equal(t, channelID, recovered.BillingChannelId)
	assert.Equal(t, "0%", recovered.Progress)
	assert.Empty(t, recovered.Status)
	assert.Zero(t, recovered.FinishTime)
	// A second reconciliation is a strict idempotent no-op: it must not create
	// another local row or increment any ledger counter.
	candidates, completed, pending = ReconcilePendingMidjourneySubmitIntents(context.Background(), 100)
	assert.Zero(t, candidates)
	assert.Zero(t, completed)
	assert.Zero(t, pending)
	var taskCount int64
	require.NoError(t, model.DB.Model(&model.Midjourney{}).Where("billing_request_id = ?", info.RequestId).Count(&taskCount).Error)
	assert.EqualValues(t, 1, taskCount)
}

func TestMidjourneySubmitIntentDoesNotChargeUnknownPendingRequest(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID, quota = 731, 731, 731, 900
	seedUser(t, userID, 10000)
	seedToken(t, tokenID, userID, "sk-mj-intent-pending", 8000)
	seedChannel(t, channelID)
	info := &relaycommon.RelayInfo{
		RequestId: "mj-intent-pending", UserId: userID, TokenId: tokenID,
		TokenKey: "sk-mj-intent-pending", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID},
	}
	created, err := PrepareMidjourneySubmitIntent(info, "IMAGINE", channelID, quota)
	require.NoError(t, err)
	require.True(t, created)
	assert.Equal(t, 10000, getUserQuota(t, userID))
	assert.Equal(t, 8000, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, model.ErrMidjourneySubmitIntentInFlight, func() error {
		_, retryErr := PrepareMidjourneySubmitIntent(info, "IMAGINE", channelID, quota)
		return retryErr
	}())
	// The accepted-intent reconciler intentionally ignores pending rows.
	candidates, completed, pending := ReconcilePendingMidjourneySubmitIntents(context.Background(), 100)
	assert.Zero(t, candidates)
	assert.Zero(t, completed)
	assert.Zero(t, pending)
}

func TestMidjourneySubmitIntentAcceptFailureKeepsProviderIdentity(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID, quota = 732, 732, 732, 700
	seedUser(t, userID, 10000)
	seedToken(t, tokenID, userID, "sk-mj-intent-rollback", 8000)
	seedChannel(t, channelID)
	info := &relaycommon.RelayInfo{
		RequestId: "mj-intent-rollback", UserId: userID, TokenId: tokenID,
		TokenKey: "sk-mj-intent-rollback", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID},
	}
	created, err := PrepareMidjourneySubmitIntent(info, "IMAGINE", channelID, quota)
	require.NoError(t, err)
	require.True(t, created)
	task := &model.Midjourney{UserId: userID, MjId: "provider-rollback", Action: "IMAGINE", ChannelId: channelID}
	financial, usage, err := midjourneyBillingSpecs(info, task, quota)
	require.NoError(t, err)
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_intent_marker_insert
		BEFORE INSERT ON billing_operations
		BEGIN
			SELECT RAISE(ABORT, 'forced intent marker failure');
		END;
	`).Error)
	assert.Error(t, model.AcceptMidjourneySubmitIntent(info.RequestId, task.MjId, financial, usage))
	require.NoError(t, model.DB.Exec("DROP TRIGGER IF EXISTS fail_intent_marker_insert").Error)
	var intent model.MidjourneySubmitIntent
	require.NoError(t, model.DB.Where("request_id = ?", info.RequestId).First(&intent).Error)
	assert.Equal(t, model.MidjourneySubmitIntentAcceptedPending, intent.Status)
	assert.Equal(t, task.MjId, intent.ProviderTaskId)
	var markerCount int64
	require.NoError(t, model.DB.Model(&model.BillingOperation{}).Where("request_id = ?", info.RequestId).Count(&markerCount).Error)
	assert.Zero(t, markerCount)
	assert.Equal(t, 10000, getUserQuota(t, userID))
	assert.Equal(t, 8000, getTokenRemainQuota(t, tokenID))
	require.NoError(t, model.AcceptMidjourneySubmitIntent(info.RequestId, task.MjId, financial, usage))
}

// Once the provider identity has been committed, a fresh process must be able
// to finish the accepted request even when the original handler died before
// marker creation. The reconciler recreates a local pollable task before it
// charges the accepted provider task, preserving the normal completion/refund
// lifecycle without guessing that a network timeout was a rejection.
func TestMidjourneyAcceptedPendingIntentReconciles(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID, quota = 733, 733, 733, 1100
	seedUser(t, userID, 10000)
	seedToken(t, tokenID, userID, "sk-mj-intent-recover", 8000)
	seedChannel(t, channelID)
	info := &relaycommon.RelayInfo{
		RequestId: "mj-intent-recover", UserId: userID, TokenId: tokenID,
		TokenKey: "sk-mj-intent-recover", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID},
	}
	created, err := PrepareMidjourneySubmitIntent(info, "IMAGINE", channelID, quota)
	require.NoError(t, err)
	require.True(t, created)
	task := &model.Midjourney{UserId: userID, MjId: "provider-recover", Action: "IMAGINE", ChannelId: channelID}
	financial, usage, err := midjourneyBillingSpecs(info, task, quota)
	require.NoError(t, err)
	require.NoError(t, model.MarkMidjourneySubmitIntentAcceptedPending(info.RequestId, task.MjId))

	// Force phase two to fail after the provider identity has been committed.
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_intent_recovery_marker_insert
		BEFORE INSERT ON billing_operations
		BEGIN
			SELECT RAISE(ABORT, 'forced intent recovery marker failure');
		END;
	`).Error)
	assert.Error(t, model.FinalizeMidjourneySubmitIntent(info.RequestId, task.MjId, financial, usage))
	require.NoError(t, model.DB.Exec("DROP TRIGGER IF EXISTS fail_intent_recovery_marker_insert").Error)

	var intent model.MidjourneySubmitIntent
	require.NoError(t, model.DB.Where("request_id = ?", info.RequestId).First(&intent).Error)
	assert.Equal(t, model.MidjourneySubmitIntentAcceptedPending, intent.Status)
	assert.Equal(t, task.MjId, intent.ProviderTaskId)
	assert.Equal(t, 10000, getUserQuota(t, userID))
	assert.Equal(t, 8000, getTokenRemainQuota(t, tokenID))

	candidates, completed, pending := ReconcilePendingMidjourneySubmitIntents(context.Background(), 100)
	assert.Equal(t, 1, candidates)
	assert.Equal(t, 1, completed)
	assert.Zero(t, pending)
	assert.Equal(t, 10000-quota, getUserQuota(t, userID))
	assert.Equal(t, 8000-quota, getTokenRemainQuota(t, tokenID))
	used, requests := getUserUsageAccounting(t, userID)
	assert.EqualValues(t, quota, used)
	assert.EqualValues(t, 1, requests)
}

// A row that already claims the request fence but presents a different
// provider identity must stop reconciliation before any charge is applied.
// Selecting an arbitrary row here would make the provider task and ledger
// belong to different jobs/accounts.
func TestMidjourneySubmitIntentRecoveryRejectsTaskIdentityConflict(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID, quota = 734, 734, 734, 950
	seedUser(t, userID, 10000)
	seedToken(t, tokenID, userID, "sk-mj-intent-conflict", 8000)
	seedChannel(t, channelID)
	info := &relaycommon.RelayInfo{
		RequestId: "mj-intent-task-conflict", UserId: userID, TokenId: tokenID,
		TokenKey: "sk-mj-intent-conflict", ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID},
	}
	created, err := PrepareMidjourneySubmitIntent(info, "IMAGINE", channelID, quota)
	require.NoError(t, err)
	require.True(t, created)
	accepted := &model.Midjourney{UserId: userID, MjId: "provider-authoritative", Action: "IMAGINE", ChannelId: channelID}
	financial, usage, err := midjourneyBillingSpecs(info, accepted, quota)
	require.NoError(t, err)
	require.NoError(t, model.AcceptMidjourneySubmitIntent(info.RequestId, accepted.MjId, financial, usage))

	// Bypass the fence to model a corrupt/legacy duplicate that an operator or
	// older deployment left behind.
	conflict := &model.Midjourney{
		UserId: userID, MjId: "provider-wrong", Action: "IMAGINE", ChannelId: channelID,
		BillingChannelId: channelID, Quota: quota, TokenId: tokenID,
		BillingRequestId: info.RequestId, Progress: "0%",
	}
	require.NoError(t, model.DB.Create(conflict).Error)

	candidates, completed, pending := ReconcilePendingMidjourneySubmitIntents(context.Background(), 100)
	assert.Equal(t, 1, candidates)
	assert.Zero(t, completed)
	assert.Equal(t, 1, pending)
	assert.Equal(t, 10000, getUserQuota(t, userID))
	assert.Equal(t, 8000, getTokenRemainQuota(t, tokenID))
	var marker model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", info.RequestId, legacyBillingSettlementComponent).First(&marker).Error)
	assert.Equal(t, model.BillingOperationPending, marker.Status)
}

func TestMidjourneyAcceptedIntentReconciliationRotatesBlockedRows(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID, quota = 735, 735, 735, 100
	seedUser(t, userID, 1000)
	seedToken(t, tokenID, userID, "mj-intent-rotation-token", 1000)
	seedChannel(t, channelID)

	for _, requestID := range []string{"mj-blocked-intent-1", "mj-blocked-intent-2"} {
		// These rows model historical corruption: accepted_pending without the
		// provider identity required to rebuild a pollable task.
		require.NoError(t, model.DB.Create(&model.MidjourneySubmitIntent{
			RequestId:        requestID,
			Status:           model.MidjourneySubmitIntentAcceptedPending,
			UserId:           userID,
			TokenId:          tokenID,
			ChannelId:        channelID,
			BillingChannelId: channelID,
			Quota:            quota,
			Action:           "IMAGINE",
		}).Error)
	}
	validRequestID := "mj-valid-intent-after-blocked-rows"
	require.NoError(t, model.DB.Create(&model.MidjourneySubmitIntent{
		RequestId:        validRequestID,
		Status:           model.MidjourneySubmitIntentAcceptedPending,
		UserId:           userID,
		TokenId:          tokenID,
		ChannelId:        channelID,
		BillingChannelId: channelID,
		Quota:            quota,
		Action:           "IMAGINE",
		ProviderTaskId:   "provider-valid-after-blocked",
	}).Error)

	candidates, completed, pending := ReconcilePendingMidjourneySubmitIntents(context.Background(), 2)
	assert.Equal(t, 2, candidates)
	assert.Zero(t, completed)
	assert.Equal(t, 2, pending)
	assert.Equal(t, 1000, getUserQuota(t, userID))
	assert.Equal(t, 1000, getTokenRemainQuota(t, tokenID))

	candidates, completed, pending = ReconcilePendingMidjourneySubmitIntents(context.Background(), 2)
	assert.Equal(t, 2, candidates)
	assert.Equal(t, 1, completed)
	assert.Equal(t, 1, pending)
	assert.Equal(t, 1000-quota, getUserQuota(t, userID))
	assert.Equal(t, 1000-quota, getTokenRemainQuota(t, tokenID))
	used, requests := getUserUsageAccounting(t, userID)
	assert.EqualValues(t, quota, used)
	assert.Equal(t, 1, requests)
	assert.EqualValues(t, quota, getChannelUsedQuota(t, channelID))
	recovered, err := model.FindByBillingRequestID(validRequestID)
	require.NoError(t, err)
	assert.Equal(t, "provider-valid-after-blocked", recovered.MjId)
	var intent model.MidjourneySubmitIntent
	require.NoError(t, model.DB.Where("request_id = ?", validRequestID).First(&intent).Error)
	assert.NotZero(t, intent.ReconciledAt)
}

func getBillingOperationStatus(t *testing.T, requestID, component string) model.BillingOperationStatus {
	t.Helper()
	var row model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", requestID, component).First(&row).Error)
	return row.Status
}

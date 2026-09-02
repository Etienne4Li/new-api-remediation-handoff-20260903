package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relaydto "github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewBillingSessionRejectsOutOfRangePreConsumeQuota(t *testing.T) {
	info := &relaycommon.RelayInfo{UserId: 1, RequestId: "invalid-preconsume"}
	for _, quota := range []int{-1, common.MaxQuota + 1} {
		session, apiErr := NewBillingSession(newBillingSessionTestContext(), info, quota)
		require.Nil(t, session)
		require.NotNil(t, apiErr)
		assert.Equal(t, types.ErrorCodeInvalidRequest, apiErr.GetErrorCode())
	}
}

func TestBillingSessionSettleRejectsOutOfRangeActualQuota(t *testing.T) {
	s := &BillingSession{relayInfo: &relaycommon.RelayInfo{}, funding: &WalletFunding{userId: 1}}
	for _, quota := range []int{-1, common.MaxQuota + 1} {
		assert.Error(t, s.Settle(quota))
	}
}

func TestBillingSessionWalletLifecycleIsIdempotentAcrossSessionInstances(t *testing.T) {
	truncate(t)
	const userID, tokenID = 9201, 9201
	seedUser(t, userID, 100)
	seedToken(t, tokenID, userID, "billing-session-idempotent-token", 100)

	newInfo := func() *relaycommon.RelayInfo {
		return &relaycommon.RelayInfo{
			UserId:          userID,
			TokenId:         tokenID,
			TokenKey:        "billing-session-idempotent-token",
			RequestId:       "billing-session-wallet-lifecycle",
			OriginModelName: "test-model",
			ForcePreConsume: true,
		}
	}

	first, apiErr := NewBillingSession(newBillingSessionTestContext(), newInfo(), 20)
	require.Nil(t, apiErr)
	second, apiErr := NewBillingSession(newBillingSessionTestContext(), newInfo(), 20)
	require.Nil(t, apiErr)

	assert.Equal(t, 80, userQuotaForBillingTest(t, userID))
	assert.Equal(t, 80, tokenRemainForBillingTest(t, tokenID))
	assert.Equal(t, 20, tokenUsedForBillingTest(t, tokenID))

	require.NoError(t, first.Settle(30))
	require.NoError(t, second.Settle(30), "a second process/session replay must observe the applied marker")
	assert.Equal(t, 70, userQuotaForBillingTest(t, userID))
	assert.Equal(t, 70, tokenRemainForBillingTest(t, tokenID))
	assert.Equal(t, 30, tokenUsedForBillingTest(t, tokenID))
}

func TestBillingSessionZeroDeltaPersistsSettlementMarker(t *testing.T) {
	truncate(t)
	const userID, tokenID = 9211, 9211
	const reserved = 20
	seedUser(t, userID, 100)
	seedToken(t, tokenID, userID, "billing-session-zero-delta-token", 100)
	info := &relaycommon.RelayInfo{
		UserId:          userID,
		TokenId:         tokenID,
		TokenKey:        "billing-session-zero-delta-token",
		RequestId:       "billing-session-zero-delta",
		ForcePreConsume: true,
	}
	session, apiErr := NewBillingSession(newBillingSessionTestContext(), info, reserved)
	require.Nil(t, apiErr)

	// The final charge equals the reservation. The successful zero-delta
	// transition must nevertheless be durable before the process can forget the
	// in-memory session.
	require.NoError(t, session.Settle(reserved))
	var marker model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", info.RequestId, "settle").First(&marker).Error)
	assert.Equal(t, model.BillingOperationApplied, marker.Status)
	assert.Equal(t, 80, userQuotaForBillingTest(t, userID))
	assert.Equal(t, 80, tokenRemainForBillingTest(t, tokenID))
	assert.Equal(t, reserved, tokenUsedForBillingTest(t, tokenID))

	// A replayed session must observe the applied marker and remain a no-op.
	replayed, apiErr := NewBillingSession(newBillingSessionTestContext(), &relaycommon.RelayInfo{
		UserId:          userID,
		TokenId:         tokenID,
		TokenKey:        info.TokenKey,
		RequestId:       info.RequestId,
		ForcePreConsume: true,
	}, reserved)
	require.Nil(t, apiErr)
	require.NoError(t, replayed.Settle(reserved))
	assert.Equal(t, 80, userQuotaForBillingTest(t, userID))
	assert.Equal(t, 80, tokenRemainForBillingTest(t, tokenID))
	assert.Equal(t, reserved, tokenUsedForBillingTest(t, tokenID))
}

func TestBillingSessionSubscriptionZeroDeltaPersistsSubscriptionIdentity(t *testing.T) {
	truncate(t)
	const userID, tokenID, subscriptionID = 9212, 9212, 9212
	const reserved int64 = 20
	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "billing-session-zero-delta-sub-token", 100)
	seedSubscription(t, subscriptionID, userID, 1_000, 0)
	info := &relaycommon.RelayInfo{
		UserId:          userID,
		TokenId:         tokenID,
		TokenKey:        "billing-session-zero-delta-sub-token",
		RequestId:       "billing-session-zero-delta-sub",
		ForcePreConsume: true,
		UserSetting:     relaydto.UserSetting{BillingPreference: "subscription_only"},
	}
	session, apiErr := NewBillingSession(newBillingSessionTestContext(), info, int(reserved))
	require.Nil(t, apiErr)
	require.NoError(t, session.Settle(int(reserved)))

	var marker model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", info.RequestId, "settle").First(&marker).Error)
	assert.Equal(t, model.BillingOperationApplied, marker.Status)
	assert.Equal(t, subscriptionID, marker.SubscriptionId)
	assert.Zero(t, marker.SubscriptionDelta)
	assert.EqualValues(t, reserved, getSubscriptionUsed(t, subscriptionID))
	assert.Equal(t, 80, tokenRemainForBillingTest(t, tokenID))

	// The durable marker must be replayable using the normal subscription
	// settlement snapshot; a missing identity would otherwise cause a spec
	// conflict here.
	replayed, apiErr := NewBillingSession(newBillingSessionTestContext(), &relaycommon.RelayInfo{
		UserId:          userID,
		TokenId:         tokenID,
		TokenKey:        info.TokenKey,
		RequestId:       info.RequestId,
		ForcePreConsume: true,
		UserSetting:     relaydto.UserSetting{BillingPreference: "subscription_only"},
	}, int(reserved))
	require.Nil(t, apiErr)
	require.NoError(t, replayed.Settle(int(reserved)))
	assert.EqualValues(t, reserved, getSubscriptionUsed(t, subscriptionID))
	assert.Equal(t, 80, tokenRemainForBillingTest(t, tokenID))
}

func TestBillingSessionWalletRefundIsAtomicAndReplaySafe(t *testing.T) {
	truncate(t)
	const userID, tokenID = 9202, 9202
	seedUser(t, userID, 100)
	seedToken(t, tokenID, userID, "billing-session-refund-token", 100)
	info := &relaycommon.RelayInfo{
		UserId:          userID,
		TokenId:         tokenID,
		TokenKey:        "billing-session-refund-token",
		RequestId:       "billing-session-wallet-refund",
		ForcePreConsume: true,
	}
	session, apiErr := NewBillingSession(newBillingSessionTestContext(), info, 20)
	require.Nil(t, apiErr)
	session.Refund(newBillingSessionTestContext())

	assert.Equal(t, 100, userQuotaForBillingTest(t, userID))
	assert.Equal(t, 100, tokenRemainForBillingTest(t, tokenID))
	assert.Equal(t, 0, tokenUsedForBillingTest(t, tokenID))
	assert.False(t, session.NeedsRefund())

	// A fresh process may reconstruct the same request and invoke refund again;
	// both the pre-consume and refund markers make this a no-op.
	replayed, apiErr := NewBillingSession(newBillingSessionTestContext(), &relaycommon.RelayInfo{
		UserId:          userID,
		TokenId:         tokenID,
		TokenKey:        "billing-session-refund-token",
		RequestId:       info.RequestId,
		ForcePreConsume: true,
	}, 20)
	require.Nil(t, apiErr)
	replayed.Refund(newBillingSessionTestContext())
	assert.Equal(t, 100, userQuotaForBillingTest(t, userID))
	assert.Equal(t, 100, tokenRemainForBillingTest(t, tokenID))
}

func TestBillingSessionWalletRefundDoesNotFallbackAfterDurableFailure(t *testing.T) {
	truncate(t)
	const userID, tokenID = 9208, 9208
	const charged = 20
	seedUser(t, userID, 100)
	seedToken(t, tokenID, userID, "billing-session-wallet-refund-failure-token", 100)
	info := &relaycommon.RelayInfo{
		UserId:          userID,
		TokenId:         tokenID,
		TokenKey:        "billing-session-wallet-refund-failure-token",
		RequestId:       "billing-session-wallet-refund-failure",
		ForcePreConsume: true,
	}
	session, apiErr := NewBillingSession(newBillingSessionTestContext(), info, charged)
	require.Nil(t, apiErr)

	// Persist the refund intent before applying any ledger deltas. If the
	// side-effecting transaction fails, the intent must remain pending so a
	// fresh process (or a later retry) can safely finish it.
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_billing_session_wallet_refund_operation
		BEFORE UPDATE OF status ON billing_operations
		WHEN NEW.status = 'applied'
		BEGIN
			SELECT RAISE(ABORT, 'forced wallet refund operation failure');
		END;
	`).Error)
	session.Refund(newBillingSessionTestContext())

	assert.True(t, session.NeedsRefund())
	assert.Equal(t, 100-charged, userQuotaForBillingTest(t, userID))
	assert.Equal(t, 100-charged, tokenRemainForBillingTest(t, tokenID))
	assert.Equal(t, charged, tokenUsedForBillingTest(t, tokenID))
	var operation model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", info.RequestId, "refund").First(&operation).Error)
	assert.Equal(t, model.BillingOperationPending, operation.Status)

	require.NoError(t, model.DB.Exec("DROP TRIGGER fail_billing_session_wallet_refund_operation").Error)
	session.Refund(newBillingSessionTestContext())
	assert.False(t, session.NeedsRefund())
	assert.Equal(t, 100, userQuotaForBillingTest(t, userID))
	assert.Equal(t, 100, tokenRemainForBillingTest(t, tokenID))
	assert.Zero(t, tokenUsedForBillingTest(t, tokenID))
}

// Subscription refunds must not leave the token ledger behind when the
// process exits while returning the subscription reservation. The durable
// refund operation applies both ledgers and marks the reservation record in
// one transaction; replaying the same request is a no-op.
func TestBillingSessionSubscriptionRefundIsAtomicAndReplaySafe(t *testing.T) {
	truncate(t)
	const userID, tokenID, subID = 9203, 9203, 9203
	const charged = 40
	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "billing-session-sub-refund-token", 100)
	seedSubscription(t, subID, userID, 1_000, 0)
	info := &relaycommon.RelayInfo{
		UserId:          userID,
		TokenId:         tokenID,
		TokenKey:        "billing-session-sub-refund-token",
		RequestId:       "billing-session-sub-refund",
		ForcePreConsume: true,
		UserSetting:     relaydto.UserSetting{BillingPreference: "subscription_only"},
	}
	session, apiErr := NewBillingSession(newBillingSessionTestContext(), info, charged)
	require.Nil(t, apiErr)
	session.Refund(newBillingSessionTestContext())

	assert.EqualValues(t, 0, getSubscriptionUsed(t, subID))
	assert.Equal(t, 100, tokenRemainForBillingTest(t, tokenID))
	assert.Zero(t, tokenUsedForBillingTest(t, tokenID))
	var marker model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Where("request_id = ?", info.RequestId).First(&marker).Error)
	assert.Equal(t, "refunded", marker.Status)
	var operation model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", info.RequestId, "refund").First(&operation).Error)
	assert.Equal(t, model.BillingOperationApplied, operation.Status)

	// A reconstructed session/retry cannot charge or refund either ledger a
	// second time, even though the first process's in-memory state is gone.
	replayed, apiErr := NewBillingSession(newBillingSessionTestContext(), &relaycommon.RelayInfo{
		UserId:          userID,
		TokenId:         tokenID,
		TokenKey:        info.TokenKey,
		RequestId:       info.RequestId,
		ForcePreConsume: true,
		UserSetting:     relaydto.UserSetting{BillingPreference: "subscription_only"},
	}, charged)
	if apiErr == nil {
		replayed.Refund(newBillingSessionTestContext())
	}
	assert.EqualValues(t, 0, getSubscriptionUsed(t, subID))
	assert.Equal(t, 100, tokenRemainForBillingTest(t, tokenID))
}

func TestBillingSessionSubscriptionReserveRefundRecoversAcrossSessionInstances(t *testing.T) {
	truncate(t)
	const userID, tokenID, subID = 9209, 9209, 9209
	const initialReserve, finalReserve = 40, 70
	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "billing-session-sub-reserve-recovery-token", 100)
	seedSubscription(t, subID, userID, 1_000, 0)
	newInfo := func() *relaycommon.RelayInfo {
		return &relaycommon.RelayInfo{
			UserId:          userID,
			TokenId:         tokenID,
			TokenKey:        "billing-session-sub-reserve-recovery-token",
			RequestId:       "billing-session-sub-reserve-recovery",
			OriginModelName: "test-model",
			ForcePreConsume: true,
			UserSetting:     relaydto.UserSetting{BillingPreference: "subscription_only"},
		}
	}

	first, apiErr := NewBillingSession(newBillingSessionTestContext(), newInfo(), initialReserve)
	require.Nil(t, apiErr)
	require.NoError(t, first.Reserve(finalReserve))
	assert.EqualValues(t, finalReserve, getSubscriptionUsed(t, subID))
	assert.Equal(t, 100-finalReserve, tokenRemainForBillingTest(t, tokenID))

	// Simulate a process exit after Reserve. The replacement process only knows
	// the stable request identity and the original pre-consume amount; the
	// reserve increments must be recovered from the durable operation journal.
	replayed, apiErr := NewBillingSession(newBillingSessionTestContext(), newInfo(), initialReserve)
	require.Nil(t, apiErr)
	assert.Equal(t, finalReserve, replayed.GetPreConsumedQuota())
	replayed.Refund(newBillingSessionTestContext())

	assert.EqualValues(t, 0, getSubscriptionUsed(t, subID))
	assert.Equal(t, 100, tokenRemainForBillingTest(t, tokenID))
	assert.Zero(t, tokenUsedForBillingTest(t, tokenID))
	var marker model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Where("request_id = ?", newInfo().RequestId).First(&marker).Error)
	assert.Equal(t, "refunded", marker.Status)
	var refund model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", newInfo().RequestId, "refund").First(&refund).Error)
	assert.EqualValues(t, -finalReserve, refund.SubscriptionDelta)
	assert.EqualValues(t, -finalReserve, refund.TokenDelta)

	// The original process can finish late without returning either ledger a
	// second time.
	first.Refund(newBillingSessionTestContext())
	assert.EqualValues(t, 0, getSubscriptionUsed(t, subID))
	assert.Equal(t, 100, tokenRemainForBillingTest(t, tokenID))
}

func TestBillingSessionSubscriptionReserveRejectsClosedReservation(t *testing.T) {
	truncate(t)
	const userID, tokenID, subID = 9210, 9210, 9210
	const initialReserve, finalReserve = 40, 70
	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "billing-session-sub-closed-reserve-token", 100)
	seedSubscription(t, subID, userID, 1_000, 0)
	newInfo := func() *relaycommon.RelayInfo {
		return &relaycommon.RelayInfo{
			UserId:          userID,
			TokenId:         tokenID,
			TokenKey:        "billing-session-sub-closed-reserve-token",
			RequestId:       "billing-session-sub-closed-reserve",
			OriginModelName: "test-model",
			ForcePreConsume: true,
			UserSetting:     relaydto.UserSetting{BillingPreference: "subscription_only"},
		}
	}

	refunder, apiErr := NewBillingSession(newBillingSessionTestContext(), newInfo(), initialReserve)
	require.Nil(t, apiErr)
	stale, apiErr := NewBillingSession(newBillingSessionTestContext(), newInfo(), initialReserve)
	require.Nil(t, apiErr)
	refunder.Refund(newBillingSessionTestContext())

	require.Error(t, stale.Reserve(finalReserve))
	assert.EqualValues(t, 0, getSubscriptionUsed(t, subID))
	assert.Equal(t, 100, tokenRemainForBillingTest(t, tokenID))
	assert.Zero(t, tokenUsedForBillingTest(t, tokenID))
	var reserveCount int64
	require.NoError(t, model.DB.Model(&model.BillingOperation{}).
		Where("request_id = ? AND component = ?", newInfo().RequestId, "reserve:70").Count(&reserveCount).Error)
	assert.Zero(t, reserveCount)
}

func TestBillingSessionSubscriptionRefundRollsBackWhenMarkerWriteFails(t *testing.T) {
	truncate(t)
	const userID, tokenID, subID = 9207, 9207, 9207
	const charged, finalReserve = 40, 70
	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "billing-session-sub-refund-marker-failure-token", 100)
	seedSubscription(t, subID, userID, 1_000, 0)
	info := &relaycommon.RelayInfo{
		UserId:          userID,
		TokenId:         tokenID,
		TokenKey:        "billing-session-sub-refund-marker-failure-token",
		RequestId:       "billing-session-sub-refund-marker-failure",
		ForcePreConsume: true,
		UserSetting:     relaydto.UserSetting{BillingPreference: "subscription_only"},
	}
	session, apiErr := NewBillingSession(newBillingSessionTestContext(), info, charged)
	require.Nil(t, apiErr)
	require.NoError(t, session.Reserve(finalReserve))

	// The refund intent and reservation fence are durable before the
	// subscription/token transaction. A failure must leave all three ledgers
	// retryable and unchanged while keeping the reservation frozen.
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_billing_session_subscription_refund_marker
		BEFORE UPDATE OF status ON subscription_pre_consume_records
		WHEN NEW.status = 'refunded'
		BEGIN
			SELECT RAISE(ABORT, 'forced subscription refund marker failure');
		END;
	`).Error)
	session.Refund(newBillingSessionTestContext())

	assert.True(t, session.NeedsRefund())
	assert.EqualValues(t, finalReserve, getSubscriptionUsed(t, subID))
	assert.Equal(t, 100-finalReserve, tokenRemainForBillingTest(t, tokenID))
	assert.Equal(t, finalReserve, tokenUsedForBillingTest(t, tokenID))
	var operation model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", info.RequestId, "refund").First(&operation).Error)
	assert.Equal(t, model.BillingOperationPending, operation.Status)
	var marker model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Where("request_id = ?", info.RequestId).First(&marker).Error)
	assert.Equal(t, "refund_pending", marker.Status)

	require.NoError(t, model.DB.Exec("DROP TRIGGER fail_billing_session_subscription_refund_marker").Error)
	session.Refund(newBillingSessionTestContext())
	assert.False(t, session.NeedsRefund())
	assert.EqualValues(t, 0, getSubscriptionUsed(t, subID))
	assert.Equal(t, 100, tokenRemainForBillingTest(t, tokenID))
	assert.Zero(t, tokenUsedForBillingTest(t, tokenID))
}

func TestSubscriptionAndTokenPreConsumeCommitAsOneTransaction(t *testing.T) {
	truncate(t)
	const userID, tokenID, subID = 9206, 9206, 9206
	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "combined-subscription-token", 100)
	seedSubscription(t, subID, userID, 1_000, 0)
	requestID := "combined-subscription-preconsume"

	// A token write failure must roll back both the subscription usage and its
	// consumed marker. This reproduces the former process-crash window at a
	// deterministic transaction seam.
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_combined_preconsume_token
		BEFORE UPDATE ON tokens
		WHEN OLD.id = 9206
		BEGIN
			SELECT RAISE(ABORT, 'forced combined token failure');
		END;
	`).Error)
	_, err := model.PreConsumeUserSubscriptionAndToken(
		requestID, userID, "test-model", 0, 40, tokenID, "combined-subscription-token", false, false,
	)
	require.Error(t, err)
	assert.Zero(t, getSubscriptionUsed(t, subID))
	assert.Equal(t, 100, tokenRemainForBillingTest(t, tokenID))
	var markerCount int64
	require.NoError(t, model.DB.Model(&model.SubscriptionPreConsumeRecord{}).
		Where("request_id = ?", requestID).Count(&markerCount).Error)
	assert.Zero(t, markerCount)

	require.NoError(t, model.DB.Exec("DROP TRIGGER IF EXISTS fail_combined_preconsume_token").Error)
	result, err := model.PreConsumeUserSubscriptionAndToken(
		requestID, userID, "test-model", 0, 40, tokenID, "combined-subscription-token", false, false,
	)
	require.NoError(t, err)
	assert.EqualValues(t, 40, result.PreConsumed)
	assert.EqualValues(t, 40, getSubscriptionUsed(t, subID))
	assert.Equal(t, 60, tokenRemainForBillingTest(t, tokenID))

	// A retry after the process has lost its in-memory session is a no-op on
	// both ledgers and returns the original reservation snapshot.
	replay, err := model.PreConsumeUserSubscriptionAndToken(
		requestID, userID, "test-model", 0, 40, tokenID, "combined-subscription-token", false, false,
	)
	require.NoError(t, err)
	assert.EqualValues(t, 40, replay.PreConsumed)
	assert.EqualValues(t, 40, getSubscriptionUsed(t, subID))
	assert.Equal(t, 60, tokenRemainForBillingTest(t, tokenID))
}

// A provider can accept an async request while the local task INSERT is
// unavailable. The billing markers do not carry the provider task identity, so
// they must remain pending until a pollable Task row owns the request fence.
func TestAsyncTaskBillingMarkersWaitForTaskRow(t *testing.T) {
	truncate(t)
	const userID, tokenID = 9204, 9204
	seedUser(t, userID, 100)
	seedToken(t, tokenID, userID, "billing-marker-recovery-token", 100)
	info := &relaycommon.RelayInfo{
		UserId:          userID,
		TokenId:         tokenID,
		TokenKey:        "billing-marker-recovery-token",
		RequestId:       "billing-marker-recovery",
		ForcePreConsume: true,
		// Explicitly represent the intentionally absent channel ledger. A nil
		// ChannelMeta is rejected by async marker creation because its promoted
		// ChannelId cannot be reconciled safely with a later task row.
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 0},
	}
	session, apiErr := NewBillingSession(newBillingSessionTestContext(), info, 20)
	require.Nil(t, apiErr)
	info.Billing = session
	require.NoError(t, EnsureAsyncTaskBillingOperations(info, 30))

	var pending []model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND status = ?", info.RequestId, model.BillingOperationPending).Find(&pending).Error)
	assert.Len(t, pending, 2)

	// Simulate process exit before Settle/Task INSERT. Charging now would leave
	// the user with an unqueryable and unrefundable provider task.
	candidates, completed, retryPending := ReconcilePendingTaskBillingOperations(nil, 100)
	assert.Equal(t, 2, candidates)
	assert.Zero(t, completed)
	assert.Equal(t, 2, retryPending)
	assert.Equal(t, 80, userQuotaForBillingTest(t, userID))
	assert.Equal(t, 80, tokenRemainForBillingTest(t, tokenID))
	assert.Equal(t, 20, tokenUsedForBillingTest(t, tokenID))
	used, requests := getUserUsageAccounting(t, userID)
	assert.Zero(t, used)
	assert.Zero(t, requests)

	var stillPending []model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component IN ?", info.RequestId, []string{
		taskBillingSettlementComponent,
		taskBillingSettlementUsageComponent,
	}).Find(&stillPending).Error)
	for _, operation := range stillPending {
		assert.Equal(t, model.BillingOperationPending, operation.Status)
	}

	// Restoring the exact accepted task makes the intent actionable. The marker
	// keys remain unchanged, so every later retry is still idempotent.
	task := &model.Task{
		TaskID:                  "billing-marker-recovery-task",
		UserId:                  userID,
		Quota:                   30,
		BillingRequestId:        info.RequestId,
		BillingPreConsumedQuota: 20,
		BillingSettlementQuota:  30,
		BillingSettlementState:  model.TaskBillingSettlementPending,
		PrivateData: model.TaskPrivateData{
			TokenId: tokenID,
		},
	}
	require.NoError(t, task.InsertWithBillingFence())
	candidates, completed, retryPending = ReconcilePendingTaskBillingOperations(nil, 100)
	assert.Equal(t, 2, candidates)
	assert.Equal(t, 2, completed)
	assert.Zero(t, retryPending)
	assert.Equal(t, 70, userQuotaForBillingTest(t, userID))
	assert.Equal(t, 70, tokenRemainForBillingTest(t, tokenID))
	assert.Equal(t, 30, tokenUsedForBillingTest(t, tokenID))
	used, requests = getUserUsageAccounting(t, userID)
	assert.Equal(t, 30, used)
	assert.Equal(t, 1, requests)

	// The original session may be discarded after the crash; a replay through
	// the normal path observes the same operation keys and cannot double-charge.
	require.NoError(t, session.Settle(30))
	assert.Equal(t, 70, userQuotaForBillingTest(t, userID))
	assert.Equal(t, 70, tokenRemainForBillingTest(t, tokenID))
}

func TestFreeAsyncTaskBillingMarkersRecoverWithoutFinancialSession(t *testing.T) {
	truncate(t)
	const userID, channelID, quota = 9205, 9205, 0
	seedUser(t, userID, 100)
	seedChannel(t, channelID)
	info := &relaycommon.RelayInfo{
		UserId:      userID,
		RequestId:   "free-billing-marker-recovery",
		PriceData:   hosttypes.PriceData{FreeModel: true},
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID},
	}
	require.NoError(t, EnsureAsyncTaskBillingOperations(info, quota))

	var markers []model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ?", info.RequestId).Find(&markers).Error)
	require.Len(t, markers, 2, "free tasks need a zero-delta settlement companion and usage marker")
	// Free requests still need a durable, pollable task owner before the request
	// counter is recorded. A zero financial delta does not make an orphan task
	// safe to claim as delivered.
	candidates, completed, pending := ReconcilePendingTaskBillingOperations(nil, 100)
	assert.Equal(t, 2, candidates)
	assert.Zero(t, completed)
	assert.Equal(t, 2, pending)
	used, requests := getUserUsageAccounting(t, userID)
	assert.Zero(t, used)
	assert.Zero(t, requests)

	task := &model.Task{
		TaskID:                 "free-billing-marker-recovery-task",
		UserId:                 userID,
		ChannelId:              channelID,
		BillingRequestId:       info.RequestId,
		BillingSettlementState: model.TaskBillingSettlementComplete,
	}
	require.NoError(t, task.InsertWithBillingFence())
	candidates, completed, pending = ReconcilePendingTaskBillingOperations(nil, 100)
	assert.Equal(t, 2, candidates)
	assert.Equal(t, 2, completed)
	assert.Zero(t, pending)
	used, requests = getUserUsageAccounting(t, userID)
	assert.Zero(t, used)
	assert.Equal(t, 1, requests)
	assert.Zero(t, getChannelUsedQuota(t, channelID))

	var applied []model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ?", info.RequestId).Find(&applied).Error)
	for _, marker := range applied {
		assert.Equal(t, model.BillingOperationApplied, marker.Status)
	}
	// Reconciliation is replay-safe and must not increment informational usage
	// a second time.
	_, completed, pending = ReconcilePendingTaskBillingOperations(nil, 100)
	assert.Zero(t, completed)
	assert.Zero(t, pending)
	used, requests = getUserUsageAccounting(t, userID)
	assert.Zero(t, used)
	assert.Equal(t, 1, requests)
}

func userQuotaForBillingTest(t *testing.T, id int) int {
	t.Helper()
	user, err := model.GetUserById(id, true)
	require.NoError(t, err)
	return user.Quota
}

func tokenRemainForBillingTest(t *testing.T, id int) int {
	t.Helper()
	var token model.Token
	require.NoError(t, model.DB.First(&token, id).Error)
	return token.RemainQuota
}

func tokenUsedForBillingTest(t *testing.T, id int) int {
	t.Helper()
	var token model.Token
	require.NoError(t, model.DB.First(&token, id).Error)
	return token.UsedQuota
}

// Keep common imported in this integration fixture even when a build tag
// elides the seed helpers' status initialization.
var _ = common.UserStatusEnabled

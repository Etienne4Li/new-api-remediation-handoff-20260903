package service

import (
	"context"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReconcilePendingBillingRefundsReplaysLegacyTaskSubscriptionWithoutReservation(t *testing.T) {
	truncate(t)
	const userID, tokenID, subscriptionID = 9410, 9410, 9410
	const quota int64 = 35
	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "legacy-task-sub-refund-token", int(100-quota))
	seedSubscription(t, subscriptionID, userID, 1_000, quota)
	// Legacy task rows predate SubscriptionPreConsumeRecord. Model the charged
	// token ledger directly and persist only the dedicated task refund intent.
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Update("used_quota", quota).Error)
	requestID := "legacy-task-subscription-replay"
	require.NoError(t, model.EnsureBillingOperation(model.BillingOperationSpec{
		RequestID:         requestID,
		Component:         LegacyTaskRefundComponent,
		UserID:            userID,
		TokenID:           tokenID,
		SubscriptionID:    subscriptionID,
		TokenDelta:        -quota,
		SubscriptionDelta: -quota,
		TokenUnlimited:    false,
	}))

	candidates, completed, pending := ReconcilePendingBillingRefunds(context.Background(), 100)
	assert.Equal(t, 1, candidates)
	assert.Equal(t, 1, completed)
	assert.Zero(t, pending)
	assert.Zero(t, getSubscriptionUsed(t, subscriptionID))
	assert.Equal(t, 100, tokenRemainForBillingTest(t, tokenID))
	assert.Zero(t, tokenUsedForBillingTest(t, tokenID))
}

func TestReconcilePendingBillingRefundsLegacyGenericMarkerUsesPlainOperation(t *testing.T) {
	truncate(t)
	const userID, tokenID, subscriptionID = 9411, 9411, 9411
	const quota int64 = 25
	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "legacy-generic-refund-token", int(100-quota))
	seedSubscription(t, subscriptionID, userID, 1_000, quota)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Update("used_quota", quota).Error)
	// Compatibility fixture for markers written by the previous deployment,
	// which used component `refund` and the deterministic legacy-refund-* key.
	requestID := "legacy-refund-compat-subscription"
	require.NoError(t, model.EnsureBillingOperation(model.BillingOperationSpec{
		RequestID:         requestID,
		Component:         model.BillingOperationRefundComponent,
		UserID:            userID,
		TokenID:           tokenID,
		SubscriptionID:    subscriptionID,
		TokenDelta:        -quota,
		SubscriptionDelta: -quota,
		TokenUnlimited:    false,
	}))

	candidates, completed, pending := ReconcilePendingBillingRefunds(context.Background(), 100)
	assert.Equal(t, 1, candidates)
	assert.Equal(t, 1, completed)
	assert.Zero(t, pending)
	assert.Zero(t, getSubscriptionUsed(t, subscriptionID))
	assert.Equal(t, 100, tokenRemainForBillingTest(t, tokenID))
}

func TestReconcilePendingBillingRefundsReplaysWalletAndTokenIntent(t *testing.T) {
	truncate(t)
	const userID, tokenID = 9401, 9401
	seedUser(t, userID, 100)
	seedToken(t, tokenID, userID, "pending-refund-wallet-token", 100)

	// Model the state immediately after a 20-unit pre-consume. The intent is
	// persisted separately, while the refund transaction is intentionally left
	// for a different process to replay.
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userID).Update("quota", 80).Error)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Updates(map[string]any{
		"remain_quota": 80,
		"used_quota":   20,
	}).Error)
	requestID := "pending-wallet-refund-replay"
	require.NoError(t, model.EnsureBillingRefundOperation(model.BillingOperationSpec{
		RequestID:      requestID,
		Component:      model.BillingOperationRefundComponent,
		UserID:         userID,
		TokenID:        tokenID,
		WalletDelta:    20,
		TokenDelta:     -20,
		TokenUnlimited: false,
	}))

	candidates, completed, pending := ReconcilePendingBillingRefunds(context.Background(), 100)
	assert.Equal(t, 1, candidates)
	assert.Equal(t, 1, completed)
	assert.Zero(t, pending)
	assert.Equal(t, 100, userQuotaForBillingTest(t, userID))
	assert.Equal(t, 100, tokenRemainForBillingTest(t, tokenID))
	assert.Zero(t, tokenUsedForBillingTest(t, tokenID))

	var operation model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", requestID, model.BillingOperationRefundComponent).First(&operation).Error)
	assert.Equal(t, model.BillingOperationApplied, operation.Status)

	// A second worker sees no pending row and cannot credit either ledger again.
	candidates, completed, pending = ReconcilePendingBillingRefunds(context.Background(), 100)
	assert.Zero(t, candidates)
	assert.Zero(t, completed)
	assert.Zero(t, pending)
	assert.Equal(t, 100, userQuotaForBillingTest(t, userID))
	assert.Equal(t, 100, tokenRemainForBillingTest(t, tokenID))
}

func TestReconcilePendingBillingRefundsClosesSubscriptionReservation(t *testing.T) {
	truncate(t)
	const userID, tokenID, subscriptionID = 9402, 9402, 9402
	const reserved int64 = 35
	seedUser(t, userID, 0)
	seedToken(t, tokenID, userID, "pending-refund-subscription-token", 100)
	seedSubscription(t, subscriptionID, userID, 1_000, 0)
	// Prefix intentionally resembles the historical legacy-task key. The
	// persisted reservation marker must take precedence so this is still
	// replayed through the atomic reservation-closing path.
	requestID := "legacy-refund-synchronous-with-marker"
	result, err := model.PreConsumeUserSubscriptionAndToken(
		requestID, userID, "test-model", 0, reserved, tokenID,
		"pending-refund-subscription-token", false, false,
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	reservation, err := model.GetSubscriptionBillingReservation(requestID)
	require.NoError(t, err)
	require.NoError(t, model.EnsureSubscriptionRefundOperation(model.BillingOperationSpec{
		RequestID:         requestID,
		Component:         model.BillingOperationRefundComponent,
		UserID:            userID,
		TokenID:           tokenID,
		SubscriptionID:    subscriptionID,
		TokenDelta:        -reservation.TokenQuota,
		SubscriptionDelta: -reservation.SubscriptionQuota,
		TokenUnlimited:    reservation.TokenUnlimited,
	}, requestID))

	var marker model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Where("request_id = ?", requestID).First(&marker).Error)
	assert.Equal(t, "refund_pending", marker.Status)

	candidates, completed, pending := ReconcilePendingBillingRefunds(context.Background(), 100)
	assert.Equal(t, 1, candidates)
	assert.Equal(t, 1, completed)
	assert.Zero(t, pending)
	assert.Zero(t, getSubscriptionUsed(t, subscriptionID))
	assert.Equal(t, 100, tokenRemainForBillingTest(t, tokenID))
	assert.Zero(t, tokenUsedForBillingTest(t, tokenID))
	require.NoError(t, model.DB.Where("request_id = ?", requestID).First(&marker).Error)
	assert.Equal(t, "refunded", marker.Status)
}

func TestRunTaskPollingOnceReconcilesRefundWithoutAdaptor(t *testing.T) {
	truncate(t)
	const userID, tokenID = 9403, 9403
	seedUser(t, userID, 100)
	seedToken(t, tokenID, userID, "polling-refund-token", 100)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userID).Update("quota", 90).Error)
	require.NoError(t, model.DB.Model(&model.Token{}).Where("id = ?", tokenID).Updates(map[string]any{
		"remain_quota": 90,
		"used_quota":   10,
	}).Error)
	require.NoError(t, model.EnsureBillingRefundOperation(model.BillingOperationSpec{
		RequestID:   "polling-refund-without-adaptor",
		Component:   model.BillingOperationRefundComponent,
		UserID:      userID,
		TokenID:     tokenID,
		WalletDelta: 10,
		TokenDelta:  -10,
	}))

	previousFactory := GetTaskAdaptorFunc
	GetTaskAdaptorFunc = nil
	t.Cleanup(func() { GetTaskAdaptorFunc = previousFactory })
	summary, err := RunTaskPollingOnce(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.BillingRefundCandidates)
	assert.Equal(t, 1, summary.BillingRefundCompleted)
	assert.Zero(t, summary.BillingRefundPending)
	assert.Equal(t, 100, userQuotaForBillingTest(t, userID))
	assert.Equal(t, 100, tokenRemainForBillingTest(t, tokenID))
}

func TestReconcilePendingBillingRefundsRotatesBlockedRows(t *testing.T) {
	truncate(t)
	const blockedUserID, validUserID = 9404, 9405
	seedUser(t, blockedUserID, common.MaxWalletQuota)
	seedUser(t, validUserID, 10)

	for _, requestID := range []string{"blocked-refund-1", "blocked-refund-2"} {
		require.NoError(t, model.EnsureBillingRefundOperation(model.BillingOperationSpec{
			RequestID:   requestID,
			Component:   model.BillingOperationTaskRefundComponent,
			UserID:      blockedUserID,
			WalletDelta: 10,
		}))
	}
	validRequestID := "valid-refund-after-blocked-rows"
	require.NoError(t, model.EnsureBillingRefundOperation(model.BillingOperationSpec{
		RequestID:   validRequestID,
		Component:   model.BillingOperationTaskRefundComponent,
		UserID:      validUserID,
		WalletDelta: 10,
	}))

	// Both rows in the first bounded page fail the wallet-overflow guard. They
	// remain pending, but must move behind untouched work.
	candidates, completed, pending := ReconcilePendingBillingRefunds(context.Background(), 2)
	assert.Equal(t, 2, candidates)
	assert.Zero(t, completed)
	assert.Equal(t, 2, pending)
	assert.Equal(t, common.MaxWalletQuota, getUserQuota(t, blockedUserID))
	assert.Equal(t, 10, getUserQuota(t, validUserID))

	// The next page reaches the valid refund instead of retrying the same two
	// blocked rows forever.
	candidates, completed, pending = ReconcilePendingBillingRefunds(context.Background(), 2)
	assert.Equal(t, 2, candidates)
	assert.Equal(t, 1, completed)
	assert.Equal(t, 1, pending)
	assert.Equal(t, 20, getUserQuota(t, validUserID))
	var operation model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", validRequestID, model.BillingOperationTaskRefundComponent).First(&operation).Error)
	assert.Equal(t, model.BillingOperationApplied, operation.Status)
}

package model

import (
	"errors"
	"fmt"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func billingOperationTestUser(t *testing.T, id, quota int) *User {
	t.Helper()
	user := &User{
		Id:       id,
		Username: "billing-operation-user-" + common.GetRandomString(8),
		AffCode:  fmt.Sprintf("billing-operation-aff-%d-%s", id, common.GetRandomString(6)),
		Password: "unused-password-hash",
		Status:   common.UserStatusEnabled,
		Quota:    quota,
	}
	require.NoError(t, DB.Create(user).Error)
	return user
}

func billingOperationTestToken(t *testing.T, id, userID, remain int) *Token {
	t.Helper()
	token := &Token{
		Id:          id,
		UserId:      userID,
		Key:         "billing-operation-token-" + common.GetRandomString(8),
		Name:        "billing-operation-test",
		Status:      common.TokenStatusEnabled,
		ExpiredTime: -1,
		RemainQuota: remain,
	}
	require.NoError(t, DB.Create(token).Error)
	return token
}

func TestApplyBillingOperationIsIdempotentAcrossRetries(t *testing.T) {
	truncateTables(t)
	user := billingOperationTestUser(t, 9101, 100)
	token := billingOperationTestToken(t, 9101, user.Id, 100)
	spec := BillingOperationSpec{
		RequestID:   "billing-operation-settle-retry",
		Component:   "settle",
		UserID:      user.Id,
		TokenID:     token.Id,
		TokenKey:    token.Key,
		WalletDelta: -30,
		TokenDelta:  30,
	}

	require.NoError(t, ApplyBillingOperation(spec))
	require.NoError(t, ApplyBillingOperation(spec), "a replay must not apply either ledger twice")

	var gotUser User
	require.NoError(t, DB.First(&gotUser, user.Id).Error)
	assert.Equal(t, 70, gotUser.Quota)
	var gotToken Token
	require.NoError(t, DB.First(&gotToken, token.Id).Error)
	assert.Equal(t, 70, gotToken.RemainQuota)
	assert.Equal(t, 30, gotToken.UsedQuota)
	var operations []BillingOperation
	require.NoError(t, DB.Where("request_id = ?", spec.RequestID).Find(&operations).Error)
	assert.Len(t, operations, 1)
	assert.Equal(t, BillingOperationApplied, operations[0].Status)
}

func TestApplyBillingOperationRollsBackLedgersWhenMarkerCommitFails(t *testing.T) {
	truncateTables(t)
	user := billingOperationTestUser(t, 9102, 100)
	token := billingOperationTestToken(t, 9102, user.Id, 100)
	spec := BillingOperationSpec{
		RequestID:   "billing-operation-marker-failure",
		Component:   "settle",
		UserID:      user.Id,
		TokenID:     token.Id,
		WalletDelta: -30,
		TokenDelta:  30,
	}
	require.NoError(t, DB.Exec(`
CREATE TRIGGER fail_billing_operation_marker
BEFORE UPDATE OF status ON billing_operations
WHEN NEW.status = 'applied'
BEGIN
  SELECT RAISE(ABORT, 'forced billing operation marker failure');
END`).Error)

	err := ApplyBillingOperation(spec)
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrBillingOperationConflict))

	var gotUser User
	require.NoError(t, DB.First(&gotUser, user.Id).Error)
	assert.Equal(t, 100, gotUser.Quota, "wallet update must roll back with marker failure")
	var gotToken Token
	require.NoError(t, DB.First(&gotToken, token.Id).Error)
	assert.Equal(t, 100, gotToken.RemainQuota, "token update must roll back with marker failure")
	assert.Zero(t, gotToken.UsedQuota)
	var count int64
	require.NoError(t, DB.Model(&BillingOperation{}).Where("operation_key = ?", BillingOperationKey(spec.RequestID, spec.Component)).Count(&count).Error)
	assert.Zero(t, count, "the marker insert must roll back as part of the transaction")

	require.NoError(t, DB.Exec("DROP TRIGGER fail_billing_operation_marker").Error)
	require.NoError(t, ApplyBillingOperation(spec))
}

func TestApplyBillingOperationRejectsKeyReuseWithDifferentLedger(t *testing.T) {
	truncateTables(t)
	user := billingOperationTestUser(t, 9103, 100)
	require.NoError(t, ApplyBillingOperation(BillingOperationSpec{
		RequestID:   "billing-operation-conflict",
		Component:   "settle",
		UserID:      user.Id,
		WalletDelta: -10,
	}))
	err := ApplyBillingOperation(BillingOperationSpec{
		RequestID:   "billing-operation-conflict",
		Component:   "settle",
		UserID:      user.Id,
		WalletDelta: -11,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBillingOperationConflict)
	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, 90, got.Quota)
}

func TestApplyBillingOperationRequiresAvailableBalancesAtomically(t *testing.T) {
	truncateTables(t)
	user := billingOperationTestUser(t, 9104, 10)
	token := billingOperationTestToken(t, 9104, user.Id, 10)
	err := ApplyBillingOperation(BillingOperationSpec{
		RequestID:            "billing-operation-insufficient",
		Component:            "preconsume",
		UserID:               user.Id,
		TokenID:              token.Id,
		WalletDelta:          -11,
		TokenDelta:           11,
		RequireWalletBalance: true,
		RequireTokenBalance:  true,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBillingOperationInsufficient)
	var gotUser User
	require.NoError(t, DB.First(&gotUser, user.Id).Error)
	assert.Equal(t, 10, gotUser.Quota)
	var gotToken Token
	require.NoError(t, DB.First(&gotToken, token.Id).Error)
	assert.Equal(t, 10, gotToken.RemainQuota)
}

func TestApplyBillingOperationDoesNotDropWalletDeltaForMissingUser(t *testing.T) {
	truncateTables(t)
	const missingUserID = 9199
	spec := BillingOperationSpec{
		RequestID:   "billing-operation-missing-wallet-user",
		Component:   "refund",
		UserID:      missingUserID,
		WalletDelta: 25,
	}

	err := ApplyBillingOperation(spec)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// The marker must remain absent (the whole transaction rolls back), so a
	// later reconciliation can retry after restoring/repairing the user row.
	var count int64
	require.NoError(t, DB.Model(&BillingOperation{}).
		Where("operation_key = ?", BillingOperationKey(spec.RequestID, spec.Component)).
		Count(&count).Error)
	assert.Zero(t, count)
}

func TestApplyBillingOperationRollsBackOnUserUsageUnderflow(t *testing.T) {
	truncateTables(t)
	user := billingOperationTestUser(t, 9190, 100)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", user.Id).Updates(map[string]interface{}{
		"used_quota": 5,
	}).Error)
	spec := BillingOperationSpec{
		RequestID:          "billing-operation-user-usage-underflow",
		Component:          "task_refund",
		UserID:             user.Id,
		WalletDelta:        25,
		UserUsedQuotaDelta: -10,
	}
	// Keep the intent durable before applying it, mirroring the refund worker's
	// crash-recovery boundary. A failed counter reversal must leave this marker
	// pending so an operator/reconciler can repair the missing usage history.
	require.NoError(t, EnsureBillingOperation(spec))
	err := ApplyBillingOperation(spec)
	require.ErrorIs(t, err, ErrBillingUsageUnderflow)

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, 100, got.Quota, "wallet mutation must roll back with usage underflow")
	assert.Equal(t, 5, got.UsedQuota, "underflow must not clamp or erase the existing counter")
	var operation BillingOperation
	require.NoError(t, DB.Where("request_id = ? AND component = ?", spec.RequestID, spec.Component).First(&operation).Error)
	assert.Equal(t, BillingOperationPending, operation.Status, "failed usage reversal remains retryable")
}

func TestApplyBillingOperationRollsBackOnChannelUsageUnderflow(t *testing.T) {
	truncateTables(t)
	user := billingOperationTestUser(t, 9191, 100)
	channel := &Channel{Id: 9191, Name: "billing-usage-underflow-channel", UsedQuota: 5}
	require.NoError(t, DB.Create(channel).Error)
	spec := BillingOperationSpec{
		RequestID:             "billing-operation-channel-usage-underflow",
		Component:             "task_refund",
		UserID:                user.Id,
		WalletDelta:           25,
		ChannelID:             channel.Id,
		ChannelUsedQuotaDelta: -10,
	}
	require.NoError(t, EnsureBillingOperation(spec))
	err := ApplyBillingOperation(spec)
	require.ErrorIs(t, err, ErrBillingUsageUnderflow)

	var gotUser User
	require.NoError(t, DB.First(&gotUser, user.Id).Error)
	assert.Equal(t, 100, gotUser.Quota, "wallet mutation must roll back with channel underflow")
	var gotChannel Channel
	require.NoError(t, DB.First(&gotChannel, channel.Id).Error)
	assert.EqualValues(t, 5, gotChannel.UsedQuota, "channel counter must remain unchanged on underflow")
	var operation BillingOperation
	require.NoError(t, DB.Where("request_id = ? AND component = ?", spec.RequestID, spec.Component).First(&operation).Error)
	assert.Equal(t, BillingOperationPending, operation.Status, "failed usage reversal remains retryable")
}

func TestBillingOperationMarkerCreationIsMutuallyExclusiveWithRefundIntent(t *testing.T) {
	truncateTables(t)
	user := billingOperationTestUser(t, 9208, 100)
	requestID := "billing-operation-marker-lifecycle-fence"
	settle := BillingOperationSpec{
		RequestID:   requestID,
		Component:   "settle",
		UserID:      user.Id,
		WalletDelta: -10,
	}
	refund := BillingOperationSpec{
		RequestID:   requestID,
		Component:   BillingOperationRefundComponent,
		UserID:      user.Id,
		WalletDelta: 10,
	}

	// A refund intent wins first; marker-only settlement creation must reject it
	// immediately rather than leaving a pending pair that can never be applied.
	require.NoError(t, EnsureBillingRefundOperation(refund))
	err := EnsureBillingOperation(settle)
	require.ErrorIs(t, err, ErrBillingOperationConflict)
	var settleCount int64
	require.NoError(t, DB.Model(&BillingOperation{}).
		Where("request_id = ? AND component = ?", requestID, "settle").Count(&settleCount).Error)
	assert.Zero(t, settleCount)

	// The inverse order is fenced as well: an existing settlement intent blocks
	// creation of a refund intent, preserving settlement-before-refund ordering.
	// Clear only this test's operation rows; truncateTables is cleanup-scoped and
	// would leave the first fixture in place until the test returns.
	require.NoError(t, DB.Where("request_id = ?", requestID).Delete(&BillingOperation{}).Error)
	require.NoError(t, EnsureBillingOperation(settle))
	err = EnsureBillingRefundOperation(refund)
	require.ErrorIs(t, err, ErrBillingOperationConflict)
	var refundCount int64
	require.NoError(t, DB.Model(&BillingOperation{}).
		Where("request_id = ? AND component = ?", requestID, BillingOperationRefundComponent).
		Count(&refundCount).Error)
	assert.Zero(t, refundCount)
}

func TestLegacyTaskRefundMarkerFencesSettlementAndReserve(t *testing.T) {
	truncateTables(t)
	user := billingOperationTestUser(t, 9210, 0)
	sub := &UserSubscription{Id: 9210, UserId: user.Id, AmountTotal: 100, AmountUsed: 10, Status: "active"}
	require.NoError(t, DB.Create(sub).Error)
	requestID := "legacy-task-refund-lifecycle-fence"
	require.NoError(t, DB.Create(&SubscriptionPreConsumeRecord{
		RequestId: requestID, UserId: user.Id, UserSubscriptionId: sub.Id,
		PreConsumed: 10, Status: "consumed",
	}).Error)
	refund := BillingOperationSpec{
		RequestID:         requestID,
		Component:         BillingOperationLegacyTaskRefundComponent,
		UserID:            user.Id,
		SubscriptionID:    sub.Id,
		SubscriptionDelta: -10,
	}
	require.NoError(t, EnsureBillingOperation(refund))

	settle := BillingOperationSpec{
		RequestID:         requestID,
		Component:         "settle",
		UserID:            user.Id,
		SubscriptionID:    sub.Id,
		SubscriptionDelta: 10,
	}
	assert.ErrorIs(t, EnsureBillingOperation(settle), ErrBillingOperationConflict)

	reserve := BillingOperationSpec{
		RequestID:         requestID,
		Component:         "reserve:20",
		UserID:            user.Id,
		SubscriptionID:    sub.Id,
		SubscriptionDelta: 10,
	}
	assert.ErrorIs(t, EnsureBillingOperation(reserve), ErrBillingOperationConflict)
}

func TestApplyBillingOperationRefundsFundingWhenTokenWasDeleted(t *testing.T) {
	truncateTables(t)
	user := billingOperationTestUser(t, 9107, 70)
	spec := BillingOperationSpec{
		RequestID:   "billing-operation-deleted-token-refund",
		Component:   "refund",
		UserID:      user.Id,
		TokenID:     9107,
		WalletDelta: 30,
		TokenDelta:  -30,
	}

	require.NoError(t, ApplyBillingOperation(spec))
	require.NoError(t, ApplyBillingOperation(spec), "deleted-token refunds must remain idempotent")

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, 100, got.Quota)
	var operation BillingOperation
	require.NoError(t, DB.Where("operation_key = ?", BillingOperationKey(spec.RequestID, spec.Component)).First(&operation).Error)
	assert.Equal(t, BillingOperationApplied, operation.Status)
}

func TestApplyBillingOperationSubscriptionAndTokenShareTransaction(t *testing.T) {
	truncateTables(t)
	user := billingOperationTestUser(t, 9105, 0)
	sub := &UserSubscription{Id: 9105, UserId: user.Id, AmountTotal: 100, AmountUsed: 20, Status: "active"}
	require.NoError(t, DB.Create(sub).Error)
	token := billingOperationTestToken(t, 9105, user.Id, 100)
	spec := BillingOperationSpec{
		RequestID:         "billing-operation-subscription",
		Component:         "settle",
		UserID:            user.Id,
		TokenID:           token.Id,
		SubscriptionID:    sub.Id,
		TokenDelta:        15,
		SubscriptionDelta: 15,
	}
	require.NoError(t, ApplyBillingOperation(spec))
	require.NoError(t, ApplyBillingOperation(spec))
	var gotSub UserSubscription
	require.NoError(t, DB.First(&gotSub, sub.Id).Error)
	assert.EqualValues(t, 35, gotSub.AmountUsed)
	var gotToken Token
	require.NoError(t, DB.First(&gotToken, token.Id).Error)
	assert.Equal(t, 85, gotToken.RemainQuota)
}

func TestApplyBillingOperationAndTaskQuotaIsAtomicAndIdempotent(t *testing.T) {
	truncateTables(t)
	user := billingOperationTestUser(t, 9106, 100)
	task := &Task{
		UserId: user.Id,
		TaskID: "billing-operation-task-quota",
		Status: TaskStatusSuccess,
		Quota:  30,
	}
	require.NoError(t, DB.Create(task).Error)
	spec := BillingOperationSpec{
		RequestID:   "billing-operation-task-quota",
		Component:   "terminal_refund",
		UserID:      user.Id,
		WalletDelta: 30,
	}

	applied, err := ApplyBillingOperationAndTaskQuota(spec, task.ID, TaskStatusSuccess, 30, 0)
	require.NoError(t, err)
	assert.True(t, applied)

	applied, err = ApplyBillingOperationAndTaskQuota(spec, task.ID, TaskStatusSuccess, 30, 0)
	require.NoError(t, err)
	assert.False(t, applied, "a replay must not refund the user twice")

	var gotUser User
	require.NoError(t, DB.First(&gotUser, user.Id).Error)
	assert.Equal(t, 130, gotUser.Quota)
	var gotTask Task
	require.NoError(t, DB.First(&gotTask, task.ID).Error)
	assert.Zero(t, gotTask.Quota)
}

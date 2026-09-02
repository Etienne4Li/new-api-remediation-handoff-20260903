package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Usage is an informational companion, not an independent charge.  Applying
// it before the corresponding financial marker would make aggregates claim a
// spend that was never committed.  Keep this invariant at the model seam so
// callers outside service/task_settlement cannot bypass it.
func TestApplyBillingOperationRequiresSubmitSettlementBeforeUsage(t *testing.T) {
	truncateTables(t)
	const userID = 9310
	user := billingOperationTestUser(t, userID, 100)
	requestID := "usage-requires-submit-settlement"
	usage := BillingOperationSpec{
		RequestID:             requestID,
		Component:             BillingOperationSettleUsageComponent,
		UserID:                user.Id,
		UserUsedQuotaDelta:    10,
		UserRequestCountDelta: 1,
	}
	financial := BillingOperationSpec{
		RequestID: requestID,
		Component: BillingOperationSettleComponent,
		UserID:    user.Id,
	}

	require.NoError(t, EnsureBillingOperation(usage))
	err := ApplyBillingOperation(usage)
	require.ErrorIs(t, err, ErrBillingOperationConflict)
	assert.Equal(t, int64(0), getBillingUserUsedQuota(t, user.Id))

	// A marker-only settlement may be created after the usage intent. Once the
	// financial operation is durably applied, the usage replay is valid.
	require.NoError(t, EnsureBillingOperation(financial))
	require.NoError(t, ApplyBillingOperation(financial))
	require.NoError(t, ApplyBillingOperation(usage))
	var row BillingOperation
	require.NoError(t, DB.Where("request_id = ? AND component = ?", requestID, BillingOperationSettleUsageComponent).First(&row).Error)
	assert.Equal(t, BillingOperationApplied, row.Status)
}

func TestApplyBillingOperationRequiresTerminalSettlementBeforeTerminalUsage(t *testing.T) {
	truncateTables(t)
	const userID = 9311
	user := billingOperationTestUser(t, userID, 100)
	requestID := "usage-requires-terminal-settlement"
	submit := BillingOperationSpec{
		RequestID: requestID,
		Component: BillingOperationSettleComponent,
		UserID:    user.Id,
	}
	terminal := BillingOperationSpec{
		RequestID: requestID,
		Component: BillingOperationTerminalSettleComponent,
		UserID:    user.Id,
	}
	usage := BillingOperationSpec{
		RequestID:             requestID,
		Component:             BillingOperationTerminalSettleUsageComponent,
		UserID:                user.Id,
		UserUsedQuotaDelta:    5,
		UserRequestCountDelta: 1,
	}

	require.NoError(t, EnsureBillingOperation(submit))
	require.NoError(t, ApplyBillingOperation(submit))
	require.NoError(t, EnsureBillingOperation(usage))
	err := ApplyBillingOperation(usage)
	require.ErrorIs(t, err, ErrBillingOperationConflict)

	require.NoError(t, EnsureBillingOperation(terminal))
	require.NoError(t, ApplyBillingOperation(terminal))
	require.NoError(t, ApplyBillingOperation(usage))
	assert.Equal(t, int64(5), getBillingUserUsedQuota(t, user.Id))
}

func TestEnsureBillingOperationsRejectsMixedUserOwners(t *testing.T) {
	truncateTables(t)
	first := billingOperationTestUser(t, 9312, 100)
	second := billingOperationTestUser(t, 9313, 100)
	requestID := "billing-operation-mixed-user-owners"
	err := EnsureBillingOperations(
		BillingOperationSpec{RequestID: requestID, Component: BillingOperationSettleComponent, UserID: first.Id},
		BillingOperationSpec{RequestID: requestID, Component: BillingOperationSettleUsageComponent, UserID: second.Id},
	)
	require.ErrorIs(t, err, ErrBillingOperationConflict)
	var count int64
	require.NoError(t, DB.Model(&BillingOperation{}).Where("request_id = ?", requestID).Count(&count).Error)
	assert.Zero(t, count)
}

// A component name is reused by independent requests.  The lifecycle scan
// must skip only markers that belong to the current batch's request/component
// pair; matching by component alone can hide an already-persisted refund and
// allow a settlement to be installed after it.
func TestEnsureBillingOperationsDoesNotSkipExistingPeerForSameComponentInOtherRequest(t *testing.T) {
	truncateTables(t)
	first := billingOperationTestUser(t, 9314, 100)
	second := billingOperationTestUser(t, 9315, 100)
	requestA := "billing-operation-existing-refund"
	requestB := "billing-operation-other-refund"

	require.NoError(t, EnsureBillingOperation(BillingOperationSpec{
		RequestID:   requestA,
		Component:   BillingOperationRefundComponent,
		UserID:      first.Id,
		WalletDelta: 10,
	}))

	err := EnsureBillingOperations(
		BillingOperationSpec{
			RequestID: requestA,
			Component: BillingOperationSettleComponent,
			UserID:    first.Id,
		},
		BillingOperationSpec{
			RequestID:   requestB,
			Component:   BillingOperationRefundComponent,
			UserID:      second.Id,
			WalletDelta: 20,
		},
	)
	require.ErrorIs(t, err, ErrBillingOperationConflict)

	var count int64
	require.NoError(t, DB.Model(&BillingOperation{}).
		Where("request_id = ? AND component = ?", requestA, BillingOperationSettleComponent).
		Count(&count).Error)
	assert.Zero(t, count, "the rejected settlement must not leave a marker behind")
	require.NoError(t, DB.Model(&BillingOperation{}).
		Where("request_id = ? AND component = ?", requestB, BillingOperationRefundComponent).
		Count(&count).Error)
	assert.Zero(t, count, "the rolled-back cross-request marker must not remain")
}

// Lifecycle fences are request-scoped.  A pending refund for one request must
// not block a settlement for another request in the same atomic batch, even
// when both operations belong to the same user and reuse common component
// names.
func TestEnsureBillingOperationsAllowsIndependentRequestsInOneBatch(t *testing.T) {
	truncateTables(t)
	const userID = 9316
	user := billingOperationTestUser(t, userID, 100)
	requestA := "billing-operation-independent-refund"
	requestB := "billing-operation-independent-settle"

	require.NoError(t, EnsureBillingOperations(
		BillingOperationSpec{
			RequestID:   requestA,
			Component:   BillingOperationRefundComponent,
			UserID:      user.Id,
			WalletDelta: 10,
		},
		BillingOperationSpec{
			RequestID: requestB,
			Component: BillingOperationSettleComponent,
			UserID:    user.Id,
		},
	))

	assertBillingOperationMarkerCount(t, requestA, 1)
	assertBillingOperationMarkerCount(t, requestB, 1)
}

func getBillingUserUsedQuota(t *testing.T, userID int) int64 {
	t.Helper()
	var row struct {
		UsedQuota int64 `gorm:"column:used_quota"`
	}
	require.NoError(t, DB.Model(&User{}).Select("used_quota").Where("id = ?", userID).Scan(&row).Error)
	return row.UsedQuota
}

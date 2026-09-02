package service

import (
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertRawTaskBillingOperation intentionally bypasses EnsureBillingOperation
// so the service guard can be exercised against rows produced by a corrupted
// or pre-fence deployment. Normal production writes reject these combinations
// before INSERT.
func insertRawTaskBillingOperation(t *testing.T, requestID, component string, userID int, status model.BillingOperationStatus, walletDelta int64) {
	t.Helper()
	require.NoError(t, model.DB.Create(&model.BillingOperation{
		OperationKey: model.BillingOperationKey(requestID, component),
		RequestId:    requestID,
		Component:    component,
		UserId:       userID,
		WalletDelta:  walletDelta,
		Status:       status,
	}).Error)
}

func TestRecordTaskBillingUsageOnceRejectsMultipleFinancialCompanions(t *testing.T) {
	truncate(t)
	const userID, channelID, quota = 9370, 9370, 50
	seedUser(t, userID, 1000)
	seedChannel(t, channelID)
	requestID := "usage-multiple-financial-companions"
	insertRawTaskBillingOperation(t, requestID, taskBillingSettlementComponent, userID, model.BillingOperationPending, -quota)
	insertRawTaskBillingOperation(t, requestID, model.BillingOperationWalletSettleComponent, userID, model.BillingOperationPending, -quota)

	err := RecordTaskBillingUsageOnce(requestID, userID, channelID, quota)
	require.ErrorIs(t, err, model.ErrBillingOperationConflict)
	assert.Equal(t, 1000, getUserQuota(t, userID), "ambiguous companions must not charge either financial row")
	used, requests := getUserUsageAccounting(t, userID)
	assert.Zero(t, used)
	assert.Zero(t, requests)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	assert.Zero(t, countBillingOperations(t, taskBillingSettlementUsageComponent))

	var rows []model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ?", requestID).Find(&rows).Error)
	for _, row := range rows {
		assert.Equal(t, model.BillingOperationPending, row.Status)
	}
}

func TestRecordTaskBillingUsageOnceRejectsCrossUserCompanion(t *testing.T) {
	truncate(t)
	const usageUserID, companionUserID, channelID, quota = 9371, 9372, 9371, 50
	seedUser(t, usageUserID, 1000)
	// users.aff_code is unique and the shared seedUser helper intentionally
	// leaves it empty. Give the second owner an explicit value so this fixture
	// can model a cross-user corruption row without tripping that unrelated
	// schema constraint.
	require.NoError(t, model.DB.Create(&model.User{
		Id:       companionUserID,
		Username: "test_user_cross_owner",
		AffCode:  "cross-owner-aff-code",
		Quota:    1000,
		Status:   common.UserStatusEnabled,
	}).Error)
	seedChannel(t, channelID)
	requestID := "usage-cross-user-companion"
	insertRawTaskBillingOperation(t, requestID, taskBillingSettlementComponent, companionUserID, model.BillingOperationPending, -quota)

	err := RecordTaskBillingUsageOnce(requestID, usageUserID, channelID, quota)
	require.ErrorIs(t, err, model.ErrBillingOperationConflict)
	assert.Equal(t, 1000, getUserQuota(t, usageUserID))
	assert.Equal(t, 1000, getUserQuota(t, companionUserID))
	used, requests := getUserUsageAccounting(t, usageUserID)
	assert.Zero(t, used)
	assert.Zero(t, requests)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	assert.Zero(t, countBillingOperations(t, taskBillingSettlementUsageComponent))

	var companion model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", requestID, taskBillingSettlementComponent).First(&companion).Error)
	assert.Equal(t, model.BillingOperationPending, companion.Status)
}

func TestRecordTaskBillingUsageOnceConcurrentReplayIsExactlyOnce(t *testing.T) {
	truncate(t)
	const userID, channelID, quota = 9373, 9373, 75
	seedUser(t, userID, 1000)
	seedChannel(t, channelID)
	requestID := "usage-concurrent-replay"
	insertRawTaskBillingOperation(t, requestID, taskBillingSettlementComponent, userID, model.BillingOperationPending, -quota)

	const workers = 4
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- RecordTaskBillingUsageOnce(requestID, userID, channelID, quota)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	assert.Equal(t, 1000-quota, getUserQuota(t, userID))
	used, requests := getUserUsageAccounting(t, userID)
	assert.Equal(t, quota, used)
	assert.Equal(t, 1, requests)
	assert.Equal(t, int64(quota), getChannelUsedQuota(t, channelID))
	assert.Equal(t, int64(1), countBillingOperations(t, taskBillingSettlementComponent))
	assert.Equal(t, int64(1), countBillingOperations(t, taskBillingSettlementUsageComponent))

	var financial, usage model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", requestID, taskBillingSettlementComponent).First(&financial).Error)
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", requestID, taskBillingSettlementUsageComponent).First(&usage).Error)
	assert.Equal(t, model.BillingOperationApplied, financial.Status)
	assert.Equal(t, model.BillingOperationApplied, usage.Status)
}

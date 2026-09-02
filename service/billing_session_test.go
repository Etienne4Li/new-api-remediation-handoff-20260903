package service

import (
	"errors"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// billingSessionTestFunding is deliberately small: it lets the lifecycle
// tests fail at a chosen funding step without touching the model database.
type billingSessionTestFunding struct {
	settleErrors []error
	refundErrors []error
	settleCalls  int
	refundCalls  int
}

func (f *billingSessionTestFunding) Source() string { return BillingSourceWallet }

func (f *billingSessionTestFunding) PreConsume(int) error { return nil }

func (f *billingSessionTestFunding) Settle(int) error {
	f.settleCalls++
	if len(f.settleErrors) == 0 {
		return nil
	}
	err := f.settleErrors[0]
	f.settleErrors = f.settleErrors[1:]
	return err
}

func (f *billingSessionTestFunding) Refund() error {
	f.refundCalls++
	if len(f.refundErrors) == 0 {
		return nil
	}
	err := f.refundErrors[0]
	f.refundErrors = f.refundErrors[1:]
	return err
}

func newBillingSessionTestContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	return ctx
}

func TestBillingSessionSettleRetriesTokenWithoutRepeatingFunding(t *testing.T) {
	funding := &billingSessionTestFunding{}
	tokenCalls := 0
	session := &BillingSession{
		relayInfo:        &relaycommon.RelayInfo{TokenId: 7, TokenKey: "test-token"},
		funding:          funding,
		preConsumedQuota: 100,
		adjustTokenFn: func(delta int) error {
			tokenCalls++
			assert.Equal(t, 50, delta)
			if tokenCalls == 1 {
				return errors.New("token store temporarily unavailable")
			}
			return nil
		},
	}

	err := session.Settle(150)
	require.Error(t, err)
	assert.Equal(t, 1, funding.settleCalls)
	assert.Equal(t, 1, tokenCalls)
	assert.True(t, session.fundingSettled)
	assert.False(t, session.tokenSettled)
	assert.False(t, session.settled)
	// Funding is already committed, so an error in the token ledger must not
	// make the caller refund the funding side a second time.
	assert.False(t, session.NeedsRefund())

	require.NoError(t, session.Settle(150))
	assert.Equal(t, 1, funding.settleCalls)
	assert.Equal(t, 2, tokenCalls)
	assert.True(t, session.tokenSettled)
	assert.True(t, session.settled)

	// A completed settlement is idempotent.
	require.NoError(t, session.Settle(150))
	assert.Equal(t, 1, funding.settleCalls)
	assert.Equal(t, 2, tokenCalls)
}

func TestBillingSessionSettleFundingFailureDoesNotTouchToken(t *testing.T) {
	funding := &billingSessionTestFunding{
		settleErrors: []error{errors.New("funding store temporarily unavailable")},
	}
	tokenCalls := 0
	session := &BillingSession{
		relayInfo:        &relaycommon.RelayInfo{TokenId: 8, TokenKey: "test-token"},
		funding:          funding,
		preConsumedQuota: 100,
		adjustTokenFn: func(int) error {
			tokenCalls++
			return nil
		},
	}

	require.Error(t, session.Settle(150))
	assert.Equal(t, 1, funding.settleCalls)
	assert.Zero(t, tokenCalls)
	assert.False(t, session.fundingSettled)
	assert.False(t, session.settlementDeltaSet)

	require.NoError(t, session.Settle(150))
	assert.Equal(t, 2, funding.settleCalls)
	assert.Equal(t, 1, tokenCalls)
	assert.True(t, session.settled)
}

func TestSettleBillingAndRecordUsageDoesNotRecordOnSettlementFailure(t *testing.T) {
	truncate(t)
	const userID, channelID = 901, 902
	seedUser(t, userID, 10_000)
	seedChannel(t, channelID)

	funding := &billingSessionTestFunding{
		settleErrors: []error{errors.New("funding settlement unavailable")},
	}
	info := &relaycommon.RelayInfo{
		BillingSource: BillingSourceWallet,
		ChannelMeta:   &relaycommon.ChannelMeta{ChannelId: channelID},
	}
	info.UserId = userID
	info.Billing = &BillingSession{
		relayInfo:        info,
		funding:          funding,
		preConsumedQuota: 100,
		adjustTokenFn:    func(int) error { return nil },
	}

	err := SettleBillingAndRecordUsage(newBillingSessionTestContext(), info, 150, true)
	require.Error(t, err)
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Equal(t, 0, usedQuota)
	assert.Equal(t, 0, requestCount)
	assert.Equal(t, int64(0), getChannelUsedQuota(t, channelID))

	// A later retry commits the settlement first; only then are aggregate
	// counters advanced.
	require.NoError(t, SettleBillingAndRecordUsage(newBillingSessionTestContext(), info, 150, true))
	usedQuota, requestCount = getUserUsageAccounting(t, userID)
	assert.Equal(t, 150, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Equal(t, int64(150), getChannelUsedQuota(t, channelID))
}

func TestLegacySettleAndUsageAreDurableAndReplaySafe(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID = 903, 904, 905
	seedUser(t, userID, 100)
	seedToken(t, tokenID, userID, "legacy-settle-token", 100)
	seedChannel(t, channelID)
	info := &relaycommon.RelayInfo{
		UserId:      userID,
		TokenId:     tokenID,
		TokenKey:    "legacy-settle-token",
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID},
		RequestId:   "legacy-settle-replay",
	}

	// This is the compatibility path used by realtime/older callers that do
	// not carry a BillingSession. Both the financial delta and the aggregate
	// usage marker must be safe to replay after a process restart.
	require.NoError(t, SettleBillingAndRecordUsage(newBillingSessionTestContext(), info, 20, true))
	require.NoError(t, SettleBillingAndRecordUsage(newBillingSessionTestContext(), info, 20, true))

	assert.Equal(t, 80, getUserQuota(t, userID))
	assert.Equal(t, 80, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, 20, getTokenUsedQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Equal(t, 20, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Equal(t, int64(20), getChannelUsedQuota(t, channelID))
	assert.Equal(t, int64(1), countBillingOperations(t, "legacy_settle"))
	assert.Equal(t, int64(1), countBillingOperations(t, syncBillingUsageComponent))
}

func TestZeroQuotaSuccessfulRequestStillRecordsUsageAndIsReplaySafe(t *testing.T) {
	truncate(t)
	const userID, channelID = 906, 907
	seedUser(t, userID, 100)
	seedChannel(t, channelID)
	info := &relaycommon.RelayInfo{
		UserId:                userID,
		RequestId:             "zero-quota-success",
		FinalPreConsumedQuota: 0,
		ChannelMeta:           &relaycommon.ChannelMeta{ChannelId: channelID},
	}

	// A successful request can legitimately have a zero monetary charge (for
	// example a free model or a zero multiplier), but it is still one request.
	require.NoError(t, SettleBillingAndRecordUsage(newBillingSessionTestContext(), info, 0, true))
	require.NoError(t, SettleBillingAndRecordUsage(newBillingSessionTestContext(), info, 0, true))

	used, requests := getUserUsageAccounting(t, userID)
	assert.Zero(t, used)
	assert.Equal(t, 1, requests)
	assert.Zero(t, getChannelUsedQuota(t, channelID))
	assert.Equal(t, int64(1), countBillingOperations(t, legacyBillingSettlementComponent))
	assert.Equal(t, int64(1), countBillingOperations(t, syncBillingUsageComponent))
	var financial, usage model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", info.RequestId, legacyBillingSettlementComponent).First(&financial).Error)
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", info.RequestId, syncBillingUsageComponent).First(&usage).Error)
	assert.Equal(t, model.BillingOperationApplied, financial.Status)
	assert.Equal(t, model.BillingOperationApplied, usage.Status)
}

func TestBillingSessionSettleRejectsChangedAmountAfterFundingCommit(t *testing.T) {
	funding := &billingSessionTestFunding{}
	tokenCalls := 0
	session := &BillingSession{
		relayInfo:        &relaycommon.RelayInfo{TokenId: 9, TokenKey: "test-token"},
		funding:          funding,
		preConsumedQuota: 100,
		adjustTokenFn: func(int) error {
			tokenCalls++
			return errors.New("token store unavailable")
		},
	}

	require.Error(t, session.Settle(150))
	require.Error(t, session.Settle(160), "a retry with a different amount must fail closed")
	assert.Equal(t, 1, funding.settleCalls)
	assert.Equal(t, 1, tokenCalls)
	assert.False(t, session.settled)
}

func TestBillingSessionRefundRetriesOnlyFailedComponents(t *testing.T) {
	funding := &billingSessionTestFunding{
		refundErrors: []error{errors.New("funding store temporarily unavailable")},
	}
	tokenCalls := 0
	session := &BillingSession{
		relayInfo:        &relaycommon.RelayInfo{TokenId: 10, TokenKey: "test-token"},
		funding:          funding,
		preConsumedQuota: 100,
		tokenConsumed:    100,
		adjustTokenFn: func(delta int) error {
			assert.Equal(t, -100, delta)
			tokenCalls++
			if tokenCalls == 1 {
				return errors.New("token refund temporarily unavailable")
			}
			return nil
		},
	}
	ctx := newBillingSessionTestContext()

	// Refund is synchronous: both failures are observable before this call
	// returns, and neither component is marked complete prematurely.
	session.Refund(ctx)
	assert.Equal(t, 1, funding.refundCalls)
	assert.Equal(t, 1, tokenCalls)
	assert.True(t, session.NeedsRefund())
	assert.False(t, session.refunded)

	session.Refund(ctx)
	assert.Equal(t, 2, funding.refundCalls)
	assert.Equal(t, 2, tokenCalls)
	assert.False(t, session.NeedsRefund())
	assert.True(t, session.refunded)

	// Once all components succeeded, duplicate calls are no-ops.
	session.Refund(ctx)
	assert.Equal(t, 2, funding.refundCalls)
	assert.Equal(t, 2, tokenCalls)
}

func TestBillingSessionReserveKeepsFailedFundingRollbackRetryable(t *testing.T) {
	funding := &billingSessionTestFunding{}
	reserveFundingCalls := 0
	rollbackCalls := 0
	reserveTokenCalls := 0
	session := &BillingSession{
		relayInfo:        &relaycommon.RelayInfo{TokenId: 11, TokenKey: "test-token"},
		funding:          funding,
		preConsumedQuota: 100,
		reserveFundingFn: func(delta int) error {
			reserveFundingCalls++
			assert.Equal(t, 50, delta)
			return nil
		},
		rollbackFundingFn: func(delta int) error {
			rollbackCalls++
			assert.Equal(t, 50, delta)
			if rollbackCalls == 1 {
				return errors.New("funding rollback temporarily unavailable")
			}
			return nil
		},
		reserveTokenFn: func(delta int) error {
			reserveTokenCalls++
			assert.Equal(t, 50, delta)
			if reserveTokenCalls == 1 {
				return errors.New("token reserve failed")
			}
			return nil
		},
	}

	require.Error(t, session.Reserve(150))
	assert.Equal(t, 1, reserveFundingCalls)
	assert.Equal(t, 1, reserveTokenCalls)
	assert.Equal(t, 1, rollbackCalls)
	assert.Equal(t, 50, session.pendingFundingReserve)
	assert.Equal(t, 100, session.preConsumedQuota)
	assert.True(t, session.NeedsRefund())

	// The next Reserve first retries the unresolved rollback, then retries the
	// reservation. No leaked funding marker remains after success.
	require.NoError(t, session.Reserve(150))
	assert.Equal(t, 2, reserveFundingCalls)
	assert.Equal(t, 2, reserveTokenCalls)
	assert.Equal(t, 2, rollbackCalls)
	assert.Zero(t, session.pendingFundingReserve)
	assert.Equal(t, 150, session.preConsumedQuota)
	assert.Equal(t, 50, session.tokenConsumed)
	assert.Equal(t, 50, session.extraReserved)
}

func TestBillingSessionPreConsumedSnapshotIsSafeDuringReserve(t *testing.T) {
	session := &BillingSession{
		relayInfo:        &relaycommon.RelayInfo{TokenId: 12, TokenKey: "test-token"},
		funding:          &billingSessionTestFunding{},
		preConsumedQuota: 100,
		reserveFundingFn: func(int) error { return nil },
		reserveTokenFn:   func(int) error { return nil },
	}

	start := make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		<-start
		for i := 0; i < 100; i++ {
			_ = session.GetPreConsumedQuota()
		}
	}()
	go func() {
		defer readers.Done()
		<-start
		require.NoError(t, session.Reserve(150))
	}()

	close(start)
	readers.Wait()
	assert.Equal(t, 150, session.GetPreConsumedQuota())
}

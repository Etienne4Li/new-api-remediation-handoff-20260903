package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBillingOperationAliasGroupsRejectPairwise keeps the component aliases
// honest as new call sites are added.  These names all describe one logical
// mutation, but they intentionally have different durable operation keys for
// backwards compatibility.  The request lifecycle fence must therefore
// reject every pair while both markers are active.
func TestBillingOperationAliasGroupsRejectPairwise(t *testing.T) {
	groups := []struct {
		name       string
		components []string
	}{
		{
			name: "submit_settlement",
			components: []string{
				BillingOperationSettleComponent,
				BillingOperationLegacySettleComponent,
				BillingOperationWalletSettleComponent,
				BillingOperationSubscriptionSettleComponent,
			},
		},
		{
			name: "terminal_settlement",
			components: []string{
				BillingOperationTerminalSettleComponent,
				BillingOperationLegacyTaskRecalculateComponent,
			},
		},
		{
			name: "terminal_refund",
			components: []string{
				BillingOperationTaskRefundComponent,
				BillingOperationTerminalRefundComponent,
				BillingOperationLegacyTaskRefundComponent,
			},
		},
		{
			name: "submit_usage",
			components: []string{
				BillingOperationSettleUsageComponent,
				BillingOperationSyncSettleUsageComponent,
				BillingOperationLegacySettleUsageComponent,
			},
		},
		{
			name: "base_preconsume",
			components: []string{
				BillingOperationWalletPreConsumeComponent,
				BillingOperationSubscriptionPreConsumeTokenComponent,
				BillingOperationPreConsumeComponent,
				BillingOperationPreConsumeTokenComponent,
			},
		},
		{
			name: "ordinary_refund",
			components: []string{
				BillingOperationRefundComponent,
				BillingOperationWalletRefundComponent,
				BillingOperationSubscriptionRefundComponent,
			},
		},
	}

	for _, group := range groups {
		group := group
		t.Run(group.name, func(t *testing.T) {
			for i := 0; i < len(group.components); i++ {
				for j := i + 1; j < len(group.components); j++ {
					left, right := group.components[i], group.components[j]
					t.Run(left+"_vs_"+right, func(t *testing.T) {
						assert.True(t, billingOperationAliasConflict(
							left, right, BillingOperationPending, BillingOperationPending,
						), "aliases must conflict in the forward direction")
						assert.True(t, billingOperationAliasConflict(
							right, left, BillingOperationApplied, BillingOperationPending,
						), "aliases must conflict in the reverse direction")

						specs := []BillingOperationSpec{
							{RequestID: "alias-pair", Component: left, UserID: 1},
							{RequestID: "alias-pair", Component: right, UserID: 1},
						}
						require.ErrorIs(t, validateBillingOperationBatch(specs), ErrBillingOperationConflict)
					})
				}
			}
		})
	}
}

// TestEnsureBillingOperationsRejectsAliasPairs exercises the database path,
// including the case where one alias already exists and a second worker tries
// to create its compatibility name later.
func TestEnsureBillingOperationsRejectsAliasPairs(t *testing.T) {
	truncateTables(t)
	const userID = 9301
	billingOperationTestUser(t, userID, 100)

	pairs := [][2]string{
		{BillingOperationSettleComponent, BillingOperationLegacySettleComponent},
		{BillingOperationTerminalSettleComponent, BillingOperationLegacyTaskRecalculateComponent},
		{BillingOperationTaskRefundComponent, BillingOperationTerminalRefundComponent},
		{BillingOperationSettleUsageComponent, BillingOperationSyncSettleUsageComponent},
		{BillingOperationWalletPreConsumeComponent, BillingOperationPreConsumeComponent},
		{BillingOperationRefundComponent, BillingOperationWalletRefundComponent},
		{BillingOperationRefundComponent, BillingOperationTokenRefundComponent},
		{BillingOperationRefundComponent, BillingOperationExtraRefundComponent},
	}

	for index, pair := range pairs {
		pair := pair
		requestID := "alias-sequential-" + string(rune('a'+index))
		t.Run(pair[0]+"_then_"+pair[1], func(t *testing.T) {
			first := BillingOperationSpec{RequestID: requestID, Component: pair[0], UserID: userID}
			second := BillingOperationSpec{RequestID: requestID, Component: pair[1], UserID: userID}
			require.NoError(t, EnsureBillingOperation(first))
			require.ErrorIs(t, EnsureBillingOperation(second), ErrBillingOperationConflict)

			var count int64
			require.NoError(t, DB.Model(&BillingOperation{}).
				Where("request_id = ? AND component = ?", requestID, pair[1]).Count(&count).Error)
			assert.Zero(t, count, "the rejected alias must not leave a marker behind")
		})
	}
}

func TestBillingOperationAliasCompatibility(t *testing.T) {
	truncateTables(t)
	const userID = 9302
	billingOperationTestUser(t, userID, 100)

	t.Run("submit_settlement_and_usage", func(t *testing.T) {
		requestID := "alias-compatible-submit"
		require.NoError(t, EnsureBillingOperations(
			BillingOperationSpec{RequestID: requestID, Component: BillingOperationSettleComponent, UserID: userID},
			BillingOperationSpec{RequestID: requestID, Component: BillingOperationSettleUsageComponent, UserID: userID},
		))
		assertBillingOperationMarkerCount(t, requestID, 2)
	})

	t.Run("terminal_settlement_and_usage", func(t *testing.T) {
		requestID := "alias-compatible-terminal"
		require.NoError(t, EnsureBillingOperations(
			BillingOperationSpec{RequestID: requestID, Component: BillingOperationTerminalSettleComponent, UserID: userID},
			BillingOperationSpec{RequestID: requestID, Component: BillingOperationTerminalSettleUsageComponent, UserID: userID},
		))
		assertBillingOperationMarkerCount(t, requestID, 2)
	})

	t.Run("wallet_refund_split_with_token", func(t *testing.T) {
		requestID := "alias-compatible-wallet-token"
		require.NoError(t, EnsureBillingOperations(
			BillingOperationSpec{RequestID: requestID, Component: BillingOperationWalletRefundComponent, UserID: userID, WalletDelta: 10},
			BillingOperationSpec{RequestID: requestID, Component: BillingOperationTokenRefundComponent, UserID: userID, TokenID: 9302, TokenDelta: -10},
		))
		assertBillingOperationMarkerCount(t, requestID, 2)
	})

	t.Run("wallet_refund_split_with_extra", func(t *testing.T) {
		requestID := "alias-compatible-wallet-extra"
		require.NoError(t, EnsureBillingOperations(
			BillingOperationSpec{RequestID: requestID, Component: BillingOperationWalletRefundComponent, UserID: userID, WalletDelta: 10},
			BillingOperationSpec{RequestID: requestID, Component: BillingOperationExtraRefundComponent, UserID: userID, TokenID: 9302, TokenDelta: -10},
		))
		assertBillingOperationMarkerCount(t, requestID, 2)
	})
}

func TestBillingOperationAppliedSubmitSettlementMayBeFollowedByTerminalRefund(t *testing.T) {
	truncateTables(t)
	const userID = 9303
	user := billingOperationTestUser(t, userID, 100)
	requestID := "alias-compatible-applied-settle-terminal-refund"

	require.NoError(t, ApplyBillingOperation(BillingOperationSpec{
		RequestID:   requestID,
		Component:   BillingOperationSettleComponent,
		UserID:      user.Id,
		WalletDelta: -10,
	}))
	require.NoError(t, EnsureBillingOperation(BillingOperationSpec{
		RequestID:   requestID,
		Component:   BillingOperationTerminalRefundComponent,
		UserID:      user.Id,
		WalletDelta: 10,
	}))
	require.NoError(t, ApplyBillingOperation(BillingOperationSpec{
		RequestID:   requestID,
		Component:   BillingOperationTerminalRefundComponent,
		UserID:      user.Id,
		WalletDelta: 10,
	}))

	var got User
	require.NoError(t, DB.First(&got, user.Id).Error)
	assert.Equal(t, 100, got.Quota, "terminal refund should reverse the applied submit settlement")
	assertBillingOperationMarkerCount(t, requestID, 2)
}

func assertBillingOperationMarkerCount(t *testing.T, requestID string, want int) {
	t.Helper()
	var count int64
	require.NoError(t, DB.Model(&BillingOperation{}).Where("request_id = ?", requestID).Count(&count).Error)
	assert.Equal(t, int64(want), count)
}

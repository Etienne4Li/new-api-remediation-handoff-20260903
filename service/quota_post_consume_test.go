package service

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPostConsumeQuotaCompensatesFundingWhenTokenWriteFails exercises the
// legacy two-ledger path directly.  A token failure must leave both ledgers at
// their pre-call balances, and a subsequent retry must charge exactly once.
func TestPostConsumeQuotaCompensatesFundingWhenTokenWriteFails(t *testing.T) {
	truncate(t)

	const userID, tokenID = 61, 61
	const initialUserQuota, initialTokenQuota, charge = 10000, 5000, 3000
	const tokenKey = "sk-post-consume-compensate"
	seedUser(t, userID, initialUserQuota)
	seedToken(t, tokenID, userID, tokenKey, initialTokenQuota)

	relayInfo := &relaycommon.RelayInfo{UserId: userID, TokenId: tokenID, TokenKey: tokenKey}
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_post_consume_token
		BEFORE UPDATE ON tokens
		WHEN OLD.id = 61
		BEGIN
			SELECT RAISE(ABORT, 'forced post-consume token failure');
		END;
	`).Error)
	t.Cleanup(func() { model.DB.Exec("DROP TRIGGER IF EXISTS fail_post_consume_token") })

	result, err := postConsumeQuotaWithResult(relayInfo, charge, 0, false)
	require.Error(t, err)
	assert.False(t, result.FundingApplied)
	assert.False(t, result.TokenApplied)
	assert.True(t, result.FundingCompensated)
	assert.Equal(t, initialUserQuota, getUserQuota(t, userID), "funding must be restored")
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID), "failed token write must not change token")

	// The failed call was fully compensated.  Retrying the same operation after
	// the transient token outage therefore performs one, and only one, charge.
	require.NoError(t, model.DB.Exec("DROP TRIGGER IF EXISTS fail_post_consume_token").Error)
	result, err = postConsumeQuotaWithResult(relayInfo, charge, 0, false)
	require.NoError(t, err)
	assert.True(t, result.FundingApplied)
	assert.True(t, result.TokenApplied)
	assert.False(t, result.FundingCompensated)
	assert.Equal(t, initialUserQuota-charge, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota-charge, getTokenRemainQuota(t, tokenID))
}

func TestPostConsumeQuotaCompensatesSubscriptionWhenTokenWriteFails(t *testing.T) {
	truncate(t)

	const userID, subscriptionID, tokenID = 63, 63, 63
	const initialUsed, initialTokenQuota, charge = int64(100), 5000, 3000
	const tokenKey = "sk-post-consume-subscription-compensate"
	seedUser(t, userID, 0)
	seedSubscription(t, subscriptionID, userID, 10000, initialUsed)
	seedToken(t, tokenID, userID, tokenKey, initialTokenQuota)

	relayInfo := &relaycommon.RelayInfo{
		UserId:         userID,
		TokenId:        tokenID,
		TokenKey:       tokenKey,
		BillingSource:  BillingSourceSubscription,
		SubscriptionId: subscriptionID,
	}
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_post_consume_subscription_token
		BEFORE UPDATE ON tokens
		WHEN OLD.id = 63
		BEGIN
			SELECT RAISE(ABORT, 'forced subscription token failure');
		END;
	`).Error)
	t.Cleanup(func() { model.DB.Exec("DROP TRIGGER IF EXISTS fail_post_consume_subscription_token") })

	result, err := postConsumeQuotaWithResult(relayInfo, charge, 0, false)
	require.Error(t, err)
	assert.False(t, result.FundingApplied)
	assert.True(t, result.FundingCompensated)
	assert.Zero(t, relayInfo.SubscriptionPostDelta)
	assert.Equal(t, initialUsed, getSubscriptionUsed(t, subscriptionID))
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))

	require.NoError(t, model.DB.Exec("DROP TRIGGER IF EXISTS fail_post_consume_subscription_token").Error)
	result, err = postConsumeQuotaWithResult(relayInfo, charge, 0, false)
	require.NoError(t, err)
	assert.True(t, result.FundingApplied)
	assert.True(t, result.TokenApplied)
	assert.Equal(t, int64(charge), relayInfo.SubscriptionPostDelta)
	assert.Equal(t, initialUsed+int64(charge), getSubscriptionUsed(t, subscriptionID))
	assert.Equal(t, initialTokenQuota-charge, getTokenRemainQuota(t, tokenID))
}

// TestPostConsumeQuotaKeepsFundingMarkerWhenCompensationFails verifies the
// fail-closed branch.  If the inverse funding write also fails, callers must
// retain their durable billing marker and reconcile it explicitly; replaying
// the full operation would otherwise double-charge funding.
func TestPostConsumeQuotaKeepsFundingMarkerWhenCompensationFails(t *testing.T) {
	truncate(t)

	const userID, tokenID = 62, 62
	const initialUserQuota, initialTokenQuota, charge = 10000, 5000, 3000
	const tokenKey = "sk-post-consume-compensation-failure"
	seedUser(t, userID, initialUserQuota)
	seedToken(t, tokenID, userID, tokenKey, initialTokenQuota)

	relayInfo := &relaycommon.RelayInfo{UserId: userID, TokenId: tokenID, TokenKey: tokenKey}
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_post_consume_token_compensation
		BEFORE UPDATE ON tokens
		WHEN OLD.id = 62
		BEGIN
			SELECT RAISE(ABORT, 'forced post-consume token failure');
		END;
	`).Error)
	// The initial funding update is 10000 -> 7000.  Only the inverse update
	// (7000 -> 10000) is rejected, so this trigger isolates compensation.
	require.NoError(t, model.DB.Exec(`
		CREATE TRIGGER fail_post_consume_funding_compensation
		BEFORE UPDATE ON users
		WHEN OLD.id = 62 AND OLD.quota = 7000
		BEGIN
			SELECT RAISE(ABORT, 'forced funding compensation failure');
		END;
	`).Error)
	t.Cleanup(func() {
		model.DB.Exec("DROP TRIGGER IF EXISTS fail_post_consume_token_compensation")
		model.DB.Exec("DROP TRIGGER IF EXISTS fail_post_consume_funding_compensation")
	})

	result, err := postConsumeQuotaWithResult(relayInfo, charge, 0, false)
	require.Error(t, err)
	assert.True(t, result.FundingApplied, "partial funding must remain marked for reconciliation")
	assert.False(t, result.TokenApplied)
	assert.False(t, result.FundingCompensated)
	assert.Equal(t, initialUserQuota-charge, getUserQuota(t, userID))
	assert.Equal(t, initialTokenQuota, getTokenRemainQuota(t, tokenID))
	assert.Error(t, err)
	// The joined error must retain both causes for retry/reconciliation logs.
	assert.NotEqual(t, "", err.Error())
	assert.ErrorIs(t, err, ErrPostConsumeFundingCompensation)

	// Once the funding store recovers, an explicit inverse operation can clear
	// the marker.  The full charge operation must not be replayed while the
	// marker is set.
	require.NoError(t, model.DB.Exec("DROP TRIGGER IF EXISTS fail_post_consume_funding_compensation").Error)
	require.NoError(t, applyPostConsumeFunding(relayInfo, -charge))
	assert.Equal(t, initialUserQuota, getUserQuota(t, userID))
}

func TestApplyPostConsumeFundingRejectsNilRelayInfo(t *testing.T) {
	err := applyPostConsumeFunding(nil, 1)
	assert.EqualError(t, err, "relay info is nil")
}

func TestInvertQuotaDeltaRejectsMinimumInteger(t *testing.T) {
	_, err := invertQuotaDelta(-int(^uint(0)>>1) - 1)
	assert.EqualError(t, err, "quota delta cannot be inverted")
}

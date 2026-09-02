package service

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The violation fee is a post-adjustment, but its usage aggregates still need
// the same idempotency fence as the wallet/token charge. Replaying the exact
// request/component must therefore leave every counter unchanged after the
// first commit.
func TestViolationFeeDurableOperationIncludesUsageDeltas(t *testing.T) {
	truncate(t)

	const userID, tokenID, channelID, feeQuota = 611, 611, 611, 1200
	seedUser(t, userID, 10000)
	seedToken(t, tokenID, userID, "sk-violation-fee", 5000)
	seedChannel(t, channelID)

	info := &relaycommon.RelayInfo{
		RequestId:     "violation-request-611",
		UserId:        userID,
		TokenId:       tokenID,
		TokenKey:      "sk-violation-fee",
		ChannelMeta:   &relaycommon.ChannelMeta{ChannelId: channelID},
		BillingSource: BillingSourceWallet,
	}
	usage := &postConsumeQuotaUsage{
		UserUsedQuotaDelta:    feeQuota,
		UserRequestCountDelta: 1,
		ChannelID:             channelID,
		ChannelUsedQuotaDelta: feeQuota,
	}

	result, err := postConsumeQuotaWithComponentUsageResult(info, feeQuota, 0, false, "violation_fee", usage)
	require.NoError(t, err)
	assert.True(t, result.Durable)
	assert.Equal(t, 8800, getUserQuota(t, userID))
	assert.Equal(t, 3800, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, feeQuota, getTokenUsedQuota(t, tokenID))
	usedQuota, requestCount := getUserUsageAccounting(t, userID)
	assert.Equal(t, feeQuota, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Equal(t, int64(feeQuota), getChannelUsedQuota(t, channelID))

	// A retry (the normal path after a response write/error race) must not
	// increment wallet, token, or informational counters again.
	result, err = postConsumeQuotaWithComponentUsageResult(info, feeQuota, 0, false, "violation_fee", usage)
	require.NoError(t, err)
	assert.True(t, result.Durable)
	assert.Equal(t, 8800, getUserQuota(t, userID))
	assert.Equal(t, 3800, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, feeQuota, getTokenUsedQuota(t, tokenID))
	usedQuota, requestCount = getUserUsageAccounting(t, userID)
	assert.Equal(t, feeQuota, usedQuota)
	assert.Equal(t, 1, requestCount)
	assert.Equal(t, int64(feeQuota), getChannelUsedQuota(t, channelID))

	var marker model.BillingOperation
	require.NoError(t, model.DB.Where("request_id = ? AND component = ?", info.RequestId, "violation_fee").First(&marker).Error)
	assert.Equal(t, int64(feeQuota), marker.UserUsedQuotaDelta)
	assert.Equal(t, int64(1), marker.UserRequestCountDelta)
	assert.Equal(t, int64(feeQuota), marker.ChannelUsedQuotaDelta)
}

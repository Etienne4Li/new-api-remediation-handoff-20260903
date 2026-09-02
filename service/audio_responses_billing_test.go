package service

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPostAudioConsumeQuotaIncompleteResponsesDoesNotRefundReservation guards
// the direct OpenAI Responses audio error path.  A client disconnect can leave
// the upstream usage object empty; that is still billable work and must not be
// converted into a zero settlement that refunds the entire reservation.
func TestPostAudioConsumeQuotaIncompleteResponsesDoesNotRefundReservation(t *testing.T) {
	truncate(t)

	const (
		userID       = 971
		tokenID      = 972
		tokenKey     = "audio-responses-incomplete-token"
		initialQuota = 10000
		reservation  = 3000
	)
	seedUser(t, userID, initialQuota)
	seedToken(t, tokenID, userID, tokenKey, initialQuota)
	seedChannel(t, 973)

	ctx := newBillingSessionTestContext()
	const requestID = "audio-responses-incomplete-settlement"
	ctx.Set(common.RequestIdKey, requestID)

	info := &relaycommon.RelayInfo{
		UserId:    userID,
		TokenId:   tokenID,
		TokenKey:  tokenKey,
		RequestId: requestID,
		// Keep the Responses audio prefix while using a synthetic suffix so the
		// test does not depend on mutable production ratio-map state. The
		// hard-coded gpt-4o completion ratio is 4 and the unknown audio suffix
		// falls back to audio ratio 1.
		OriginModelName: "gpt-4o-audio-test",
		RelayMode:       relayconstant.RelayModeResponses,
		IsStream:        true,
		StartTime:       time.Now(),
		PriceData: hosttypes.PriceData{
			ModelRatio:      1,
			CompletionRatio: 1,
			GroupRatioInfo:  hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
		ChannelMeta:  &relaycommon.ChannelMeta{ChannelId: 973},
		StreamStatus: relaycommon.NewStreamStatus(),
	}
	info.SetEstimatePromptTokens(37)
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, nil)
	common.SetContextKey(ctx, constant.ContextKeyResponsesStreamIncomplete, true)
	common.SetContextKey(ctx, constant.ContextKeyResponsesUsageAuthoritative, false)
	common.SetContextKey(ctx, constant.ContextKeyLocalCountTokens, true)

	apiErr := PreConsumeBilling(ctx, reservation, info)
	require.Nil(t, apiErr, "pre-consume failed: %#v", apiErr)
	assert.Equal(t, initialQuota-reservation, getUserQuota(t, userID))
	assert.Equal(t, initialQuota-reservation, getTokenRemainQuota(t, tokenID))

	// No upstream usage was reported.  The incomplete Responses policy should
	// settle the conservative input + completion baseline (37 + 500*4 = 2037),
	// refunding only the unused part of the reservation.
	PostAudioConsumeQuota(ctx, info, &dto.Usage{}, "")

	const expectedActualQuota = 2037
	assert.Equal(t, initialQuota-expectedActualQuota, getUserQuota(t, userID))
	assert.Equal(t, initialQuota-expectedActualQuota, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, expectedActualQuota, getTokenUsedQuota(t, tokenID))
}

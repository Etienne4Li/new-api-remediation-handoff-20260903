package service

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreWssConsumeQuotaRaisesBillingReservationToCumulativeTarget(t *testing.T) {
	billing := &recordingBillingSettler{}
	info := &relaycommon.RelayInfo{
		Billing:         billing,
		OriginModelName: "gpt-4o",
		UsingGroup:      "default",
	}
	usage := &dto.RealtimeUsage{
		InputTokenDetails:  dto.InputTokenDetails{TextTokens: 100},
		OutputTokenDetails: dto.OutputTokenDetails{TextTokens: 50},
	}

	modelRatio, _, _ := ratio_setting.GetModelRatio(info.OriginModelName)
	expected, clamp := calculateAudioQuota(QuotaInfo{
		InputDetails:  TokenDetails{TextTokens: 100},
		OutputDetails: TokenDetails{TextTokens: 50},
		ModelName:     info.OriginModelName,
		ModelRatio:    modelRatio,
		GroupRatio:    ratio_setting.GetGroupRatio(info.UsingGroup),
	})
	require.Nil(t, clamp)
	require.NoError(t, PreWssConsumeQuota(&gin.Context{}, info, usage))
	require.Equal(t, []int{expected}, billing.reserveTargets)
	require.Equal(t, expected, billing.GetPreConsumedQuota())
}

func TestPreWssConsumeQuotaDoesNotLowerOrRepeatReservation(t *testing.T) {
	billing := &recordingBillingSettler{preConsumedQuota: 10_000}
	info := &relaycommon.RelayInfo{
		Billing:         billing,
		OriginModelName: "gpt-4o",
		UsingGroup:      "default",
	}
	usage := &dto.RealtimeUsage{
		InputTokenDetails:  dto.InputTokenDetails{TextTokens: 10},
		OutputTokenDetails: dto.OutputTokenDetails{TextTokens: 5},
	}

	require.NoError(t, PreWssConsumeQuota(&gin.Context{}, info, usage))
	require.Empty(t, billing.reserveTargets)
	require.Equal(t, 10_000, billing.GetPreConsumedQuota())
}

func TestPreWssConsumeQuotaRejectsMissingUsage(t *testing.T) {
	require.Error(t, PreWssConsumeQuota(&gin.Context{}, &relaycommon.RelayInfo{}, nil))
}

func TestPreWssConsumeQuotaNormalizesAggregateOnlyUsage(t *testing.T) {
	billing := &recordingBillingSettler{}
	info := &relaycommon.RelayInfo{
		Billing:         billing,
		OriginModelName: "gpt-4o",
		UsingGroup:      "default",
	}
	usage := &dto.RealtimeUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150}

	modelRatio, _, _ := ratio_setting.GetModelRatio(info.OriginModelName)
	expected, clamp := calculateAudioQuota(QuotaInfo{
		InputDetails:  TokenDetails{TextTokens: 100},
		OutputDetails: TokenDetails{TextTokens: 50},
		ModelName:     info.OriginModelName,
		ModelRatio:    modelRatio,
		GroupRatio:    ratio_setting.GetGroupRatio(info.UsingGroup),
	})
	require.Nil(t, clamp)
	require.NoError(t, PreWssConsumeQuota(&gin.Context{}, info, usage))
	require.Equal(t, []int{expected}, billing.reserveTargets)
}

func TestPreWssConsumeQuotaUsesFrozenModelRatioSnapshot(t *testing.T) {
	const modelName = "realtime-ratio-snapshot-test"
	originalRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(originalRatios))
	})
	// Deliberately change the global ratio between response boundaries. The
	// request-scoped PriceData snapshot must remain authoritative throughout
	// the realtime stream.
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"`+modelName+`":1}`))

	billing := &recordingBillingSettler{}
	info := &relaycommon.RelayInfo{
		Billing:         billing,
		OriginModelName: modelName,
		UsingGroup:      "default",
	}
	info.SetPriceDataSnapshot(hosttypes.PriceData{
		ModelRatio:     9,
		GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1},
	})

	first := &dto.RealtimeUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150}
	firstQuota, clamp := calculateAudioQuota(QuotaInfo{
		InputDetails:  TokenDetails{TextTokens: 100},
		OutputDetails: TokenDetails{TextTokens: 50},
		ModelName:     modelName,
		ModelRatio:    9,
		GroupRatio:    1,
	})
	require.Nil(t, clamp)
	// Nil contexts are accepted for internal callers and must not make the
	// pricing path panic while it reads optional auto-group metadata.
	require.NoError(t, PreWssConsumeQuota(nil, info, first))
	require.Equal(t, []int{firstQuota}, billing.reserveTargets)

	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"`+modelName+`":100}`))
	second := &dto.RealtimeUsage{InputTokens: 200, OutputTokens: 100, TotalTokens: 300}
	secondQuota, clamp := calculateAudioQuota(QuotaInfo{
		InputDetails:  TokenDetails{TextTokens: 200},
		OutputDetails: TokenDetails{TextTokens: 100},
		ModelName:     modelName,
		ModelRatio:    9,
		GroupRatio:    1,
	})
	require.Nil(t, clamp)
	require.NoError(t, PreWssConsumeQuota(nil, info, second))
	require.Equal(t, []int{firstQuota, secondQuota}, billing.reserveTargets)
}

func TestPreWssConsumeQuotaHonorsZeroModelRatioSnapshot(t *testing.T) {
	const modelName = "realtime-free-ratio-snapshot-test"
	originalRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(originalRatios))
	})
	// A legacy/global lookup would return a paid ratio here. The explicit zero
	// in the frozen snapshot represents a free model and must remain zero.
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"`+modelName+`":7}`))

	billing := &recordingBillingSettler{}
	info := &relaycommon.RelayInfo{
		Billing:         billing,
		OriginModelName: modelName,
		UsingGroup:      "default",
	}
	info.SetPriceDataSnapshot(hosttypes.PriceData{
		FreeModel:      true,
		ModelRatio:     0,
		GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1},
	})

	require.NoError(t, PreWssConsumeQuota(nil, info, &dto.RealtimeUsage{
		InputTokens: 100, OutputTokens: 50, TotalTokens: 150,
	}))
	require.Empty(t, billing.reserveTargets)
}

func TestCalculateAudioQuotaIncludesRealtimeCacheAndImageDimensions(t *testing.T) {
	quota, clamp := calculateAudioQuota(QuotaInfo{
		InputDetails: TokenDetails{
			TextTokens:   100,
			CachedTokens: 40,
			ImageTokens:  10,
		},
		OutputDetails: TokenDetails{TextTokens: 50},
		ModelName:     "gpt-4o",
		ModelRatio:    1.25,
		GroupRatio:    1,
		CacheRatio:    0.5,
		ImageRatio:    2,
	})
	require.Nil(t, clamp)
	// (100-40 + 40*0.5 + 10*2 + 50*4) * 1.25 = 375 quota units.
	require.Equal(t, 375, quota)
}

func TestRealtimeCumulativeReservationSettlesExactlyOnce(t *testing.T) {
	truncate(t)
	const userID, tokenID, channelID = 9301, 9301, 9301
	const initialQuota = 100_000
	seedUser(t, userID, initialQuota)
	seedToken(t, tokenID, userID, "realtime-cumulative-token", initialQuota)
	seedChannel(t, channelID)

	info := &relaycommon.RelayInfo{
		UserId:          userID,
		TokenId:         tokenID,
		TokenKey:        "realtime-cumulative-token",
		RequestId:       "realtime-cumulative-request",
		OriginModelName: "realtime-test-model",
		UsingGroup:      "default",
		ForcePreConsume: true,
		PriceData: hosttypes.PriceData{
			ModelRatio:     37.5,
			CacheRatio:     0.5,
			ImageRatio:     1,
			GroupRatioInfo: hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
		ChannelMeta: &relaycommon.ChannelMeta{ChannelId: channelID},
	}
	info.PriceDataSnapshotReady = true
	ctx := newBillingSessionTestContext()
	session, apiErr := NewBillingSession(ctx, info, 100)
	require.Nil(t, apiErr)
	info.Billing = session

	first := &dto.RealtimeUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150}
	second := &dto.RealtimeUsage{InputTokens: 200, OutputTokens: 100, TotalTokens: 300}
	firstQuota, clamp := calculateAudioQuota(QuotaInfo{
		InputDetails:  TokenDetails{TextTokens: 100},
		OutputDetails: TokenDetails{TextTokens: 50},
		ModelName:     info.OriginModelName,
		ModelRatio:    info.PriceData.ModelRatio,
		GroupRatio:    1,
	})
	require.Nil(t, clamp)
	secondQuota, clamp := calculateAudioQuota(QuotaInfo{
		InputDetails:  TokenDetails{TextTokens: 200},
		OutputDetails: TokenDetails{TextTokens: 100},
		ModelName:     info.OriginModelName,
		ModelRatio:    info.PriceData.ModelRatio,
		GroupRatio:    1,
	})
	require.Nil(t, clamp)
	require.Greater(t, secondQuota, firstQuota)
	require.NoError(t, PreWssConsumeQuota(ctx, info, first))
	require.NoError(t, PreWssConsumeQuota(ctx, info, second))
	require.Equal(t, secondQuota, session.GetPreConsumedQuota())

	PostWssConsumeQuota(ctx, info, info.OriginModelName, second, "")
	// Post settlement consumes the cumulative target once. Replaying the
	// terminal usage frame is fenced by the BillingOperation request marker.
	PostWssConsumeQuota(ctx, info, info.OriginModelName, second, "")
	assert.Equal(t, initialQuota-secondQuota, getUserQuota(t, userID))
	assert.Equal(t, initialQuota-secondQuota, getTokenRemainQuota(t, tokenID))
	assert.Equal(t, secondQuota, getTokenUsedQuota(t, tokenID))
	assert.Equal(t, int64(1), countBillingOperations(t, "settle"))
}

package service

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newIncompleteResponsesBillingContext(t *testing.T, authoritative bool) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	common.SetContextKey(ctx, constant.ContextKeyLocalCountTokens, !authoritative)
	common.SetContextKey(ctx, constant.ContextKeyResponsesUsageAuthoritative, authoritative)
	return ctx
}

func newIncompleteResponsesRelayInfo() *relaycommon.RelayInfo {
	info := &relaycommon.RelayInfo{
		IsStream:  true,
		RelayMode: relayconstant.RelayModeResponses,
		PriceData: hosttypes.PriceData{
			ModelRatio:        1,
			CompletionRatio:   1,
			GroupRatioInfo:    hosttypes.GroupRatioInfo{GroupRatio: 1},
			QuotaToPreConsume: 50_000_000,
		},
		StreamStatus: relaycommon.NewStreamStatus(),
		StartTime:    time.Now(),
	}
	info.SetEstimatePromptTokens(37)
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, nil)
	return info
}

func TestNormalizeIncompleteResponsesStreamUsageUsesInputEstimateAndBaseline(t *testing.T) {
	ctx := newIncompleteResponsesBillingContext(t, false)
	info := newIncompleteResponsesRelayInfo()

	usage, normalized := normalizeIncompleteResponsesStreamUsage(ctx, info, &dto.Usage{})

	require.True(t, normalized)
	require.NotNil(t, usage)
	assert.Equal(t, 37, usage.PromptTokens)
	assert.Equal(t, 500, usage.CompletionTokens)
	assert.Equal(t, 537, usage.TotalTokens)

	summary := calculateTextQuotaSummary(ctx, info, usage)
	assert.Equal(t, 537, summary.Quota)
	assert.Less(t, summary.Quota, info.PriceData.QuotaToPreConsume)
}

func TestNormalizeIncompleteResponsesStreamUsageHonorsPositiveBaselineBelowDefault(t *testing.T) {
	ctx := newIncompleteResponsesBillingContext(t, false)
	info := newIncompleteResponsesRelayInfo()
	old := common.PreConsumedQuota
	common.PreConsumedQuota = 100
	t.Cleanup(func() { common.PreConsumedQuota = old })

	usage, normalized := normalizeIncompleteResponsesStreamUsage(ctx, info, &dto.Usage{})
	require.True(t, normalized)
	assert.Equal(t, 100, usage.CompletionTokens,
		"a positive operator setting below the default must remain effective")
}

func TestNormalizeIncompleteResponsesStreamUsageKeepsObservedCompletionAboveBaseline(t *testing.T) {
	ctx := newIncompleteResponsesBillingContext(t, false)
	info := newIncompleteResponsesRelayInfo()
	original := &dto.Usage{PromptTokens: 12, CompletionTokens: 900, TotalTokens: 912}

	usage, normalized := normalizeIncompleteResponsesStreamUsage(ctx, info, original)

	require.True(t, normalized)
	assert.Equal(t, 37, usage.PromptTokens)
	assert.Equal(t, 900, usage.CompletionTokens)
	assert.Equal(t, 937, usage.TotalTokens)
	assert.Equal(t, 12, original.PromptTokens)
	assert.Equal(t, 900, original.CompletionTokens)
	assert.Equal(t, 912, original.TotalTokens)
}

func TestNormalizeIncompleteResponsesStreamUsageDoesNotReplaceAuthoritativeUsage(t *testing.T) {
	ctx := newIncompleteResponsesBillingContext(t, true)
	info := newIncompleteResponsesRelayInfo()
	original := &dto.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}

	usage, normalized := normalizeIncompleteResponsesStreamUsage(ctx, info, original)

	assert.False(t, normalized)
	assert.Same(t, original, usage)
	assert.Equal(t, 11, usage.PromptTokens)
	assert.Equal(t, 7, usage.CompletionTokens)
	assert.Equal(t, 18, usage.TotalTokens)
}

func TestNormalizeIncompleteResponsesStreamUsageDoesNotChargeFreeGroup(t *testing.T) {
	ctx := newIncompleteResponsesBillingContext(t, false)
	info := newIncompleteResponsesRelayInfo()
	info.PriceData.FreeModel = true
	info.PriceData.GroupRatioInfo.GroupRatio = 0

	usage, normalized := normalizeIncompleteResponsesStreamUsage(ctx, info, &dto.Usage{})

	require.True(t, normalized)
	summary := calculateTextQuotaSummary(ctx, info, usage)
	assert.Zero(t, summary.Quota)
	assert.Zero(t, usage.PromptTokens)
	assert.Zero(t, usage.CompletionTokens)
}

func TestNeedsIncompleteResponsesStreamBillingRequiresAbnormalNonAuthoritativeStream(t *testing.T) {
	ctx := newIncompleteResponsesBillingContext(t, false)
	info := newIncompleteResponsesRelayInfo()

	assert.True(t, needsIncompleteResponsesStreamBilling(ctx, info, &dto.Usage{}))
	assert.True(t, needsIncompleteResponsesStreamBillingFloor(ctx, info, &dto.Usage{}),
		"legacy helper must follow the new usage-normalisation decision")

	authoritativeCtx := newIncompleteResponsesBillingContext(t, true)
	assert.False(t, needsIncompleteResponsesStreamBilling(authoritativeCtx, info, &dto.Usage{}))

	normalInfo := *info
	normalInfo.StreamStatus = relaycommon.NewStreamStatus()
	normalInfo.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
	assert.False(t, needsIncompleteResponsesStreamBilling(ctx, &normalInfo, &dto.Usage{}))
}

func TestNeedsIncompleteResponsesStreamBillingTreatsEOFWithoutTerminalAsAbnormal(t *testing.T) {
	ctx := newIncompleteResponsesBillingContext(t, false)
	info := newIncompleteResponsesRelayInfo()
	info.StreamStatus = relaycommon.NewStreamStatus()
	info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
	common.SetContextKey(ctx, constant.ContextKeyResponsesStreamTerminalSeen, false)

	assert.True(t, needsIncompleteResponsesStreamBilling(ctx, info, &dto.Usage{PromptTokens: 37}))
	usage, normalized := normalizeIncompleteResponsesStreamUsage(ctx, info, &dto.Usage{PromptTokens: 37})
	require.True(t, normalized)
	assert.Equal(t, 500, usage.CompletionTokens)
}

func TestNormalizeIncompleteResponsesStreamUsageClonesNestedInputDetails(t *testing.T) {
	ctx := newIncompleteResponsesBillingContext(t, false)
	info := newIncompleteResponsesRelayInfo()
	original := &dto.Usage{
		PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 10},
		InputTokensDetails:  &dto.InputTokenDetails{CachedTokens: 20},
	}

	usage, normalized := normalizeIncompleteResponsesStreamUsage(ctx, info, original)

	require.True(t, normalized)
	require.NotNil(t, usage.InputTokensDetails)
	usage.InputTokensDetails.CachedTokens = 99
	assert.Equal(t, 20, original.InputTokensDetails.CachedTokens)
	assert.Nil(t, usage.BillingUsage)
}

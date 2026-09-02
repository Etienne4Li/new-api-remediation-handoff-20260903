package service

import (
	"math"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAIBillingUsageTotalSaturatesTokenSum(t *testing.T) {
	converted := usageFromOpenAIBillingUsage(&dto.BillingUsage{
		OpenAIUsage: &dto.Usage{
			PromptTokens:     math.MaxInt,
			CompletionTokens: 1,
		},
	})
	require.NotNil(t, converted)
	assert.Equal(t, math.MaxInt, converted.TotalTokens)
}

func TestClaudeBillingUsageSaturatesTokenSums(t *testing.T) {
	converted := usageFromClaudeBillingUsage(&dto.BillingUsage{
		ClaudeUsage: &dto.ClaudeUsage{
			InputTokens:              math.MaxInt,
			CacheReadInputTokens:     1,
			CacheCreationInputTokens: 1,
			OutputTokens:             1,
		},
	})
	require.NotNil(t, converted)
	assert.Equal(t, math.MaxInt, converted.TotalTokens)
	assert.Equal(t, math.MaxInt, converted.InputTokens)
}

func TestGeminiBillingUsageSaturatesTokenAndDetailSums(t *testing.T) {
	converted := usageFromGeminiBillingUsage(&dto.BillingUsage{
		GeminiUsageMetadata: &dto.GeminiUsageMetadata{
			PromptTokenCount:        math.MaxInt,
			ToolUsePromptTokenCount: 1,
			CandidatesTokenCount:    math.MaxInt,
			ThoughtsTokenCount:      1,
			PromptTokensDetails: []dto.GeminiPromptTokensDetails{
				{Modality: "TEXT", TokenCount: math.MaxInt},
				{Modality: "TEXT", TokenCount: 1},
			},
		},
	})
	require.NotNil(t, converted)
	assert.Equal(t, math.MaxInt, converted.PromptTokens)
	assert.Equal(t, math.MaxInt, converted.CompletionTokens)
	assert.Equal(t, math.MaxInt, converted.PromptTokensDetails.TextTokens)
}

func TestValidUsageRejectsNegativeOnlyCounters(t *testing.T) {
	assert.False(t, ValidUsage(&dto.Usage{PromptTokens: -1, CompletionTokens: 0}))
	assert.False(t, ValidUsage(&dto.Usage{PromptTokens: 0, CompletionTokens: -1}))
	assert.True(t, ValidUsage(&dto.Usage{PromptTokens: 1}))
}

func TestCalculateTextQuotaSummaryNormalizesNegativeUsageCounters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	relayInfo := &relaycommon.RelayInfo{
		OriginModelName: "boundary-model",
		PriceData: hosttypes.PriceData{
			ModelRatio:      1,
			CompletionRatio: 1,
			GroupRatioInfo:  hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, &dto.Usage{
		PromptTokens:     -10,
		CompletionTokens: -20,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens:         -1,
			CachedCreationTokens: -2,
			ImageTokens:          -3,
			AudioTokens:          -4,
		},
		ClaudeCacheCreation5mTokens: -5,
		ClaudeCacheCreation1hTokens: -6,
	})

	assert.Zero(t, summary.PromptTokens)
	assert.Zero(t, summary.CompletionTokens)
	assert.Zero(t, summary.TotalTokens)
	assert.Zero(t, summary.CacheTokens)
	assert.Zero(t, summary.CacheCreationTokens)
	assert.Zero(t, summary.ImageTokens)
	assert.Zero(t, summary.AudioTokens)
}

func TestCalculateTextQuotaSummaryClampsOpenRouterCacheSubtraction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	relayInfo := &relaycommon.RelayInfo{
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelType: constant.ChannelTypeOpenRouter},
		OriginModelName: "claude-boundary-model",
		PriceData: hosttypes.PriceData{
			ModelRatio:         1,
			CompletionRatio:    1,
			CacheRatio:         1,
			CacheCreationRatio: 1,
			GroupRatioInfo:     hosttypes.GroupRatioInfo{GroupRatio: 1},
		},
		StartTime: time.Now(),
	}

	summary := calculateTextQuotaSummary(ctx, relayInfo, &dto.Usage{
		PromptTokens:     10,
		CompletionTokens: 1,
		UsageSemantic:    dto.BillingUsageSemanticAnthropic,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: 100,
		},
	})

	assert.Zero(t, summary.PromptTokens)
}

package xai

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/require"
)

func TestNormalizeXAIUsageUsesConservativeNonNegativeTotals(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     -4,
		CompletionTokens: 3,
		TotalTokens:      1,
		PromptTokensDetails: dto.InputTokenDetails{
			CachedTokens: -1,
		},
		CompletionTokenDetails: dto.OutputTokenDetails{
			ReasoningTokens: 9,
			TextTokens:      math.MaxInt,
		},
	}

	normalizeXAIUsage(usage)

	// The reported total is inconsistent with the dimensions. Keep the
	// non-negative dimensions and raise total to their conservative sum.
	require.Equal(t, 0, usage.PromptTokens)
	require.Equal(t, 3, usage.CompletionTokens)
	require.Equal(t, 3, usage.TotalTokens)
	require.Equal(t, 0, usage.PromptTokensDetails.CachedTokens)
	// Reasoning cannot exceed completion, so derived text is zero.
	require.Equal(t, 0, usage.CompletionTokenDetails.TextTokens)
}

func TestNormalizeXAIUsageSaturatesLargeCounters(t *testing.T) {
	usage := &dto.Usage{
		PromptTokens:     math.MaxInt,
		CompletionTokens: 1,
		TotalTokens:      math.MaxInt,
	}

	normalizeXAIUsage(usage)

	require.Equal(t, math.MaxInt, usage.PromptTokens)
	require.Equal(t, 1, usage.CompletionTokens)
	require.Equal(t, math.MaxInt, usage.TotalTokens)
}

package dto

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTokenMetadataSaturatesOversizedUintMaxTokens(t *testing.T) {
	oversized := ^uint(0)
	maxInt := int(^uint(0) >> 1)

	openAI := (&GeneralOpenAIRequest{MaxTokens: &oversized}).GetTokenCountMeta()
	require.NotNil(t, openAI)
	assert.Equal(t, maxInt, openAI.MaxTokens)

	openAICompletion := (&GeneralOpenAIRequest{MaxCompletionTokens: &oversized}).GetTokenCountMeta()
	require.NotNil(t, openAICompletion)
	assert.Equal(t, maxInt, openAICompletion.MaxTokens)

	responses := (&OpenAIResponsesRequest{MaxOutputTokens: &oversized}).GetTokenCountMeta()
	require.NotNil(t, responses)
	assert.Equal(t, maxInt, responses.MaxTokens)

	gemini := (&GeminiChatRequest{GenerationConfig: GeminiChatGenerationConfig{
		MaxOutputTokens: &oversized,
	}}).GetTokenCountMeta()
	require.NotNil(t, gemini)
	assert.Equal(t, maxInt, gemini.MaxTokens)

	claude := (&ClaudeRequest{MaxTokens: &oversized}).GetTokenCountMeta()
	require.NotNil(t, claude)
	assert.Equal(t, maxInt, claude.MaxTokens)
}

func TestImageTokenMetadataCapsUnvalidatedCount(t *testing.T) {
	oversized := ^uint(0)
	meta := (&ImageRequest{N: &oversized}).GetTokenCountMeta()
	require.NotNil(t, meta)
	require.NotNil(t, meta.BillingRatios)
	assert.Equal(t, float64(MaxImageN), meta.BillingRatios["n"])
}

func TestBoundedImageNRejectsOversizedDirectAdaptorInput(t *testing.T) {
	oversized := uint(MaxImageN + 1)
	got, ok := BoundedImageN(&oversized)
	assert.False(t, ok)
	assert.Zero(t, got)

	defaultN, ok := BoundedImageN(nil)
	require.True(t, ok)
	require.Equal(t, uint(1), defaultN)
}

func TestSaturatingUintToIntBoundaryMatchesMetadataType(t *testing.T) {
	// This assertion documents the intended behavior on the host architecture
	// without relying on a particular 32/64-bit CI runner.
	assert.Equal(t, int(^uint(0)>>1), types.SaturatingUintToInt(uint(math.MaxUint)))
}

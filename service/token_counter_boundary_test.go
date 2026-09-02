package service

import (
	"image"
	"math"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateImageDimensionsRejectsMalformedHeaderSizes(t *testing.T) {
	assert.NoError(t, validateImageDimensions(image.Config{Width: 1, Height: 1}))
	assert.NoError(t, validateImageDimensions(image.Config{}))
	assert.Error(t, validateImageDimensions(image.Config{Width: -1, Height: 1}))
	assert.Error(t, validateImageDimensions(image.Config{Width: maxImageDimension + 1, Height: 1}))
	assert.Error(t, validateImageDimensions(image.Config{Width: 1, Height: maxImageDimension + 1}))
}

func TestGetImageTokenRejectsOversizedCachedDimensions(t *testing.T) {
	previousGetMediaToken := constant.GetMediaToken
	previousGetMediaTokenNotStream := constant.GetMediaTokenNotStream
	constant.GetMediaToken = true
	constant.GetMediaTokenNotStream = true
	t.Cleanup(func() {
		constant.GetMediaToken = previousGetMediaToken
		constant.GetMediaTokenNotStream = previousGetMediaTokenNotStream
	})

	source := types.NewBase64FileSource("", "image/png")
	config := image.Config{Width: maxImageDimension + 1, Height: 1}
	cached := types.NewMemoryCachedData("", "image/png", 0)
	cached.ImageConfig = &config
	source.SetCache(cached)

	_, err := getImageToken(nil, &types.FileMeta{FileType: types.FileTypeImage, Source: source}, "gpt-4.1-mini", true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dimensions")
}

func TestSafeTokenProductSaturatesInsteadOfWrapping(t *testing.T) {
	assert.Equal(t, 42, safeTokenProduct(6, 7))
	assert.Equal(t, 0, safeTokenProduct(-1, 7))
	assert.Equal(t, math.MaxInt, safeTokenProduct(math.MaxInt, 2))
	assert.Equal(t, math.MaxInt, safeTokenProduct(math.MaxInt/2+1, 2))
}

func TestPatchImageTokenCalculationUsesSafeAreaArithmetic(t *testing.T) {
	previousGetMediaToken := constant.GetMediaToken
	previousGetMediaTokenNotStream := constant.GetMediaTokenNotStream
	constant.GetMediaToken = true
	constant.GetMediaTokenNotStream = true
	t.Cleanup(func() {
		constant.GetMediaToken = previousGetMediaToken
		constant.GetMediaTokenNotStream = previousGetMediaTokenNotStream
	})

	source := types.NewBase64FileSource("", "image/png")
	config := image.Config{Width: maxImageDimension, Height: maxImageDimension}
	cached := types.NewMemoryCachedData("", "image/png", 0)
	cached.ImageConfig = &config
	source.SetCache(cached)

	tokens, err := getImageToken(nil, &types.FileMeta{FileType: types.FileTypeImage, Source: source}, "gpt-4.1-mini", true)
	require.NoError(t, err)
	// Patch-based models cap the raw patch count at 1536 before applying the
	// model multiplier; a huge advertised area must therefore stay finite.
	assert.Greater(t, tokens, 0)
	assert.LessOrEqual(t, tokens, 1536*2) // multiplier is 1.62, rounded
}

func TestCountTokenRealtimeSaturatesAccumulation(t *testing.T) {
	// The helper itself is the invariant used by each realtime accumulation
	// branch; exercise its boundary directly so this test remains independent
	// of provider-specific event construction.
	assert.Equal(t, math.MaxInt, safeTokenTotal(math.MaxInt-1, 8))
}

package mokaai

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/require"
)

func TestConvertEmbeddingRequestNormalizesScalarInput(t *testing.T) {
	converted, err := (&Adaptor{}).ConvertEmbeddingRequest(nil, nil, dto.EmbeddingRequest{
		Model: "m3e-large",
		Input: "hello",
	})
	require.NoError(t, err)
	require.Equal(t, &dto.EmbeddingRequest{
		Model: "m3e-large",
		Input: []string{"hello"},
	}, converted)
}

func TestConvertEmbeddingRequestDropsNonStringArrayItems(t *testing.T) {
	converted, err := (&Adaptor{}).ConvertEmbeddingRequest(nil, nil, dto.EmbeddingRequest{
		Model: "m3e-base",
		Input: []any{"first", 42, "second"},
	})
	require.NoError(t, err)
	require.Equal(t, &dto.EmbeddingRequest{
		Model: "m3e-base",
		Input: []string{"first", "second"},
	}, converted)
}

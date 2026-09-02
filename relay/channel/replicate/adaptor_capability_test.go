package replicate

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

func TestUnsupportedConvertersReturnCapabilityErrors(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		call     func() (any, error)
	}{
		{name: "openai", endpoint: string(types.RelayFormatOpenAI), call: func() (any, error) {
			return (&Adaptor{}).ConvertOpenAIRequest(nil, nil, &dto.GeneralOpenAIRequest{})
		}},
		{name: "rerank", endpoint: string(types.RelayFormatRerank), call: func() (any, error) {
			return (&Adaptor{}).ConvertRerankRequest(nil, 0, dto.RerankRequest{})
		}},
		{name: "embedding", endpoint: string(types.RelayFormatEmbedding), call: func() (any, error) {
			return (&Adaptor{}).ConvertEmbeddingRequest(nil, nil, dto.EmbeddingRequest{})
		}},
		{name: "audio", endpoint: string(types.RelayFormatOpenAIAudio), call: func() (any, error) {
			return (&Adaptor{}).ConvertAudioRequest(nil, nil, dto.AudioRequest{})
		}},
		{name: "gemini", endpoint: string(types.RelayFormatGemini), call: func() (any, error) {
			return (&Adaptor{}).ConvertGeminiRequest(nil, nil, (*dto.GeminiChatRequest)(nil))
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			converted, err := tt.call()
			require.Nil(t, converted)
			var capabilityErr *types.UnsupportedEndpointError
			require.ErrorAs(t, err, &capabilityErr)
			require.Equal(t, ChannelName, capabilityErr.ChannelName)
			require.Equal(t, tt.endpoint, capabilityErr.Endpoint)
		})
	}
}

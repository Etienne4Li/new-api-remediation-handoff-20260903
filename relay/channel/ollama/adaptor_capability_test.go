package ollama

import (
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

func TestOllamaRerankConverterReturnsCapabilityError(t *testing.T) {
	var (
		converted any
		err       error
	)

	require.NotPanics(t, func() {
		converted, err = (&Adaptor{}).ConvertRerankRequest(nil, 0, dto.RerankRequest{})
	})
	require.Nil(t, converted)
	var capabilityErr *types.UnsupportedEndpointError
	require.ErrorAs(t, err, &capabilityErr)
	require.Equal(t, ChannelName, capabilityErr.ChannelName)
	require.Equal(t, string(types.RelayFormatRerank), capabilityErr.Endpoint)
}

func TestOllamaClaudeCompatibilityUsesNativeMessagesEndpoint(t *testing.T) {
	url, err := (&Adaptor{}).GetRequestURL(&relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatClaude,
		ChannelMeta: &relaycommon.ChannelMeta{ChannelBaseUrl: "http://ollama"},
	})
	require.NoError(t, err)
	require.Equal(t, "http://ollama/v1/messages", url)
}

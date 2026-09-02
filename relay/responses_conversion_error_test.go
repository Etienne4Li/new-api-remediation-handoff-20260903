package relay

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

func TestResponsesRequestConversionErrorRetriesUnsupportedEndpoint(t *testing.T) {
	err := responsesRequestConversionError(types.NewUnsupportedEndpointError("test", string(types.RelayFormatOpenAIResponses)))
	require.False(t, types.IsSkipRetryError(err))
	require.Equal(t, types.ErrorCodeConvertRequestFailed, err.GetErrorCode())
	require.ErrorContains(t, err, "does not support endpoint")
}

func TestResponsesRequestConversionErrorSkipsRetryForOrdinaryFailure(t *testing.T) {
	err := responsesRequestConversionError(errors.New("invalid Responses request"))
	require.True(t, types.IsSkipRetryError(err))
	require.Equal(t, types.ErrorCodeConvertRequestFailed, err.GetErrorCode())
}

package relay

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

func TestClaudeRequestConversionErrorRetriesUnsupportedEndpoint(t *testing.T) {
	err := claudeRequestConversionError(types.NewUnsupportedEndpointError("test", string(types.RelayFormatClaude)))
	require.False(t, types.IsSkipRetryError(err))
	require.Equal(t, types.ErrorCodeConvertRequestFailed, err.GetErrorCode())
	require.ErrorContains(t, err, "does not support endpoint")
}

func TestClaudeRequestConversionErrorSkipsRetryForInvalidRequest(t *testing.T) {
	err := claudeRequestConversionError(errors.New("invalid Claude request"))
	require.True(t, types.IsSkipRetryError(err))
	require.Equal(t, types.ErrorCodeConvertRequestFailed, err.GetErrorCode())
}

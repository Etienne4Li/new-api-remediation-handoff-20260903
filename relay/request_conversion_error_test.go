package relay

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

func TestRequestConversionErrorRetriesUnsupportedEndpoint(t *testing.T) {
	err := requestConversionError(types.NewUnsupportedEndpointError("ollama", string(types.RelayFormatRerank)))

	require.NotNil(t, err)
	require.False(t, types.IsSkipRetryError(err))
	require.Equal(t, types.ErrorCodeConvertRequestFailed, err.GetErrorCode())
	require.ErrorContains(t, err, "does not support endpoint")
}

func TestRequestConversionErrorSkipsRetryForOrdinaryFailure(t *testing.T) {
	err := requestConversionError(errors.New("invalid converted request"))

	require.NotNil(t, err)
	require.True(t, types.IsSkipRetryError(err))
	require.Equal(t, types.ErrorCodeConvertRequestFailed, err.GetErrorCode())
}

func TestRequestConversionErrorPreservesWrappedCapabilityError(t *testing.T) {
	capabilityErr := types.NewUnsupportedEndpointError("ollama", string(types.RelayFormatRerank))
	err := requestConversionError(errors.Join(errors.New("conversion wrapper"), capabilityErr))

	require.NotNil(t, err)
	require.False(t, types.IsSkipRetryError(err))
	var got *types.UnsupportedEndpointError
	require.ErrorAs(t, err, &got)
	require.Same(t, capabilityErr, got)
}

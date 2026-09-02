package relay

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequireHTTPResponseRejectsUnexpectedAdaptorValues(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{name: "nil", value: nil},
		{name: "typed nil", value: (*http.Response)(nil)},
		{name: "wrong type", value: "upstream response"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, apiErr := requireHTTPResponse(tt.value)
			require.Nil(t, response)
			require.NotNil(t, apiErr)
			assert.Equal(t, types.ErrorCodeBadResponse, apiErr.GetErrorCode())
			assert.Equal(t, http.StatusInternalServerError, apiErr.StatusCode)
			assert.True(t, types.IsSkipRetryError(apiErr))
		})
	}
}

func TestRequireHTTPResponseAcceptsResponse(t *testing.T) {
	want := &http.Response{StatusCode: http.StatusOK}
	got, apiErr := requireHTTPResponse(want)
	require.Nil(t, apiErr)
	assert.Same(t, want, got)
}

func TestRequireUsageRejectsUnexpectedAdaptorValues(t *testing.T) {
	for _, value := range []any{nil, (*dto.Usage)(nil), dto.Usage{}, "usage"} {
		usage, apiErr := requireUsage(value)
		require.Nil(t, usage)
		require.NotNil(t, apiErr)
		assert.Equal(t, types.ErrorCodeBadResponseBody, apiErr.GetErrorCode())
		assert.True(t, types.IsSkipRetryError(apiErr))
	}
}

func TestRequireUsageAcceptsUsage(t *testing.T) {
	want := &dto.Usage{TotalTokens: 3}
	got, apiErr := requireUsage(want)
	require.Nil(t, apiErr)
	assert.Same(t, want, got)
}

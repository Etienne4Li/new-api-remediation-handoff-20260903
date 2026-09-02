package controller

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCoerceTestHTTPResponseRejectsInvalidValues(t *testing.T) {
	for _, value := range []any{nil, (*http.Response)(nil), "not an HTTP response"} {
		response, err := coerceTestHTTPResponse(value)
		require.Nil(t, response)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid channel test HTTP response type")
	}
}

func TestCoerceTestHTTPResponseAcceptsHTTPResponse(t *testing.T) {
	want := &http.Response{StatusCode: http.StatusOK}
	got, err := coerceTestHTTPResponse(want)
	require.NoError(t, err)
	assert.Same(t, want, got)
}

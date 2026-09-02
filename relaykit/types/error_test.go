package types

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewOpenAIErrorWithNilCauseIsSafe(t *testing.T) {
	err := NewOpenAIError(nil, ErrorCodeBadResponse, http.StatusInternalServerError)
	require.NotNil(t, err)
	assert.Equal(t, ErrorCodeBadResponse, err.GetErrorCode())
	assert.Equal(t, http.StatusInternalServerError, err.StatusCode)
	assert.Equal(t, string(ErrorCodeBadResponse), err.Error())
	assert.Equal(t, string(ErrorCodeBadResponse), err.ToOpenAIError().Message)
}

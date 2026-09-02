package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestResetStatusCode(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name             string
		statusCode       int
		statusCodeConfig string
		expectedCode     int
	}{
		{
			name:             "map string value",
			statusCode:       429,
			statusCodeConfig: `{"429":"503"}`,
			expectedCode:     503,
		},
		{
			name:             "map int value",
			statusCode:       429,
			statusCodeConfig: `{"429":503}`,
			expectedCode:     503,
		},
		{
			name:             "skip invalid string value",
			statusCode:       429,
			statusCodeConfig: `{"429":"bad-code"}`,
			expectedCode:     429,
		},
		{
			name:             "skip status code 200",
			statusCode:       200,
			statusCodeConfig: `{"200":503}`,
			expectedCode:     200,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			newAPIError := &types.NewAPIError{
				StatusCode: tc.statusCode,
			}
			ResetStatusCode(newAPIError, tc.statusCodeConfig)
			require.Equal(t, tc.expectedCode, newAPIError.StatusCode)
		})
	}
}

func TestRelayErrorHandlerUsesBodyMetadataInLog(t *testing.T) {
	withDebugEnabled(t, false)

	body := strings.Repeat("b", common.LocalLogContentLimit+256)
	var logBuffer bytes.Buffer

	common.LogWriterMu.Lock()
	oldWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logBuffer
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = oldWriter
		common.LogWriterMu.Unlock()
	})

	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.Equal(t, "bad response status code 500", newAPIError.Error())
	require.Contains(t, logBuffer.String(), fmt.Sprintf("len=%d", len(body)))
	require.Contains(t, logBuffer.String(), "hash=")
	require.NotContains(t, logBuffer.String(), body)
}

func TestRelayErrorHandlerKeepsStructuredErrorMessage(t *testing.T) {
	message := strings.Repeat("c", common.LocalLogContentLimit+256)
	body := `{"message":"` + message + `"}`
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.Equal(t, message, newAPIError.Error())
}

func TestRelayErrorHandlerKeepsOpenAIErrorMessage(t *testing.T) {
	message := strings.Repeat("d", common.LocalLogContentLimit+256)
	body := `{"error":{"message":"` + message + `","type":"server_error","code":"server_error"}}`
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.Equal(t, message, newAPIError.Error())
}

func TestRelayErrorHandlerShowBodyUsesMessageMetadata(t *testing.T) {
	secret := "provider-secret-message"
	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"` + secret + `","type":"server_error"}}`)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, true)
	require.NotNil(t, newAPIError)
	require.NotContains(t, newAPIError.Error(), secret)
	require.Contains(t, newAPIError.Error(), "message_meta:")
	require.Contains(t, newAPIError.Error(), "body_meta:")
}

func TestRelayErrorHandlerPreservesSessionPolicyErrorCode(t *testing.T) {
	body := `{"error":{"message":"session blocked","type":"permission_error","code":"session_blocked_by_cyber_policy"}}`
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.Equal(t, types.ErrorCodeSessionBlockedByCyberPolicy, newAPIError.GetErrorCode())
	require.Equal(t, http.StatusForbidden, newAPIError.StatusCode)
}

func TestRelayErrorHandlerNeverLeaksInvalidJSONBodyInDebugLog(t *testing.T) {
	withDebugEnabled(t, true)

	body := strings.Repeat("e", common.LocalLogContentLimit+256)
	var logBuffer bytes.Buffer

	common.LogWriterMu.Lock()
	oldWriter := gin.DefaultErrorWriter
	gin.DefaultErrorWriter = &logBuffer
	common.LogWriterMu.Unlock()
	t.Cleanup(func() {
		common.LogWriterMu.Lock()
		gin.DefaultErrorWriter = oldWriter
		common.LogWriterMu.Unlock()
	})

	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	newAPIError := RelayErrorHandler(context.Background(), resp, false)

	require.NotNil(t, newAPIError)
	require.NotContains(t, logBuffer.String(), "[truncated")
	require.Contains(t, logBuffer.String(), fmt.Sprintf("len=%d", len(body)))
	require.Contains(t, logBuffer.String(), "hash=")
	require.NotContains(t, logBuffer.String(), body)
}

func TestTaskErrorWrapperSanitizesProviderMessage(t *testing.T) {
	secret := "provider-secret-token"
	taskErr := TaskErrorWrapper(errors.New(`upstream failed Authorization: Bearer `+secret+` body={"api_key":"`+secret+`"}`), "upstream_failed", http.StatusBadGateway)
	require.NotContains(t, taskErr.Message, secret)
	require.Contains(t, taskErr.Message, "Bearer ***")
	require.Equal(t, http.StatusBadGateway, taskErr.StatusCode)
}

func TestTaskErrorFromAPIErrorSanitizesMessage(t *testing.T) {
	secret := "provider-secret-token"
	apiErr := types.NewOpenAIError(errors.New(`Authorization: Bearer `+secret), types.ErrorCodeDoRequestFailed, http.StatusBadGateway)
	taskErr := TaskErrorFromAPIError(apiErr)
	require.NotNil(t, taskErr)
	require.NotContains(t, taskErr.Message, secret)
	require.Contains(t, taskErr.Message, "Bearer ***")
}

func withDebugEnabled(t *testing.T, enabled bool) {
	t.Helper()

	oldDebug := common.DebugEnabled
	common.DebugEnabled = enabled
	t.Cleanup(func() {
		common.DebugEnabled = oldDebug
	})
}

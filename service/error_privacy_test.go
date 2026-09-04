package service

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/require"
)

func TestSanitizeUpstreamErrorForClient_Disabled(t *testing.T) {
	original := operation_setting.GetGeneralSetting().HideUpstreamErrorDetails
	operation_setting.GetGeneralSetting().HideUpstreamErrorDetails = false
	defer func() { operation_setting.GetGeneralSetting().HideUpstreamErrorDetails = original }()

	err := types.NewErrorWithStatusCode(errors.New("provider says 429"), types.ErrorCode("upstream_error"), 429)
	got := SanitizeUpstreamErrorForClient(err)
	require.Same(t, err, got)
	require.Contains(t, got.Error(), "provider says 429")
}

func TestSanitizeUpstreamErrorForClient_LocalErrorPassedThrough(t *testing.T) {
	original := operation_setting.GetGeneralSetting().HideUpstreamErrorDetails
	operation_setting.GetGeneralSetting().HideUpstreamErrorDetails = true
	defer func() { operation_setting.GetGeneralSetting().HideUpstreamErrorDetails = original }()

	err := types.NewErrorWithStatusCode(errors.New("insufficient quota"), types.ErrorCodeInsufficientUserQuota, 403)
	require.Equal(t, types.ErrorTypeNewAPIError, err.GetErrorType())
	got := SanitizeUpstreamErrorForClient(err)
	require.Same(t, err, got)
	require.Contains(t, got.Error(), "insufficient quota")
}

func TestSanitizeUpstreamErrorForClient_Upstream429Masked(t *testing.T) {
	original := operation_setting.GetGeneralSetting().HideUpstreamErrorDetails
	operation_setting.GetGeneralSetting().HideUpstreamErrorDetails = true
	defer func() { operation_setting.GetGeneralSetting().HideUpstreamErrorDetails = original }()

	err := types.NewOpenAIError(errors.New("provider endpoint https://secret.example says 429"), types.ErrorCode("rate_limit_exceeded"), 429)
	got := SanitizeUpstreamErrorForClient(err)
	require.NotSame(t, err, got)
	require.Contains(t, got.Error(), "upstream rate limited, please retry later")
	require.NotContains(t, got.Error(), "secret.example")
	require.Equal(t, 429, got.StatusCode)
}

func TestSanitizeUpstreamErrorForClient_Upstream400PassedThrough(t *testing.T) {
	original := operation_setting.GetGeneralSetting().HideUpstreamErrorDetails
	operation_setting.GetGeneralSetting().HideUpstreamErrorDetails = true
	defer func() { operation_setting.GetGeneralSetting().HideUpstreamErrorDetails = original }()

	err := types.NewOpenAIError(errors.New("invalid parameter value"), types.ErrorCode("invalid_request_error"), 400)
	got := SanitizeUpstreamErrorForClient(err)
	require.Same(t, err, got)
	require.Contains(t, got.Error(), "invalid parameter value")
}

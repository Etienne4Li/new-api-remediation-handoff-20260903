package service

import (
	"errors"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// SanitizeUpstreamErrorForClient replaces upstream-facing error text with a
// generic message before it is returned to an API caller, when the
// hide_upstream_error_details general setting is enabled.
//
// By the time this runs the original error has already been written to the
// error log (which is visible to root users) by processChannelError. This
// function only changes what is returned to the client; the stored log rows and
// any root-visible diagnostics are left untouched.
//
// Local errors (ErrorTypeNewAPIError, e.g. insufficient quota, invalid token,
// self-hosted rate limit) and 4xx parameter-class errors (400, 404, 413, 422,
// ...) are passed through unchanged so the caller still sees the actionable
// text. Only upstream authentication (401/403), upstream account (402),
// upstream rate-limit (429) and upstream server (>=500) errors are masked.
func SanitizeUpstreamErrorForClient(err *types.NewAPIError) *types.NewAPIError {
	if err == nil {
		return err
	}

	if !operation_setting.GetGeneralSetting().HideUpstreamErrorDetails {
		return err
	}

	switch err.GetErrorType() {
	case types.ErrorTypeOpenAIError,
		types.ErrorTypeClaudeError,
		types.ErrorTypeGeminiError,
		types.ErrorTypeRerankError,
		types.ErrorTypeUpstreamError,
		types.ErrorTypeMidjourneyError:
		// Upstream-sourced errors: fall through and potentially mask.
	default:
		// ErrorTypeNewAPIError and anything else are local errors; keep original.
		return err
	}

	var msg string
	switch err.StatusCode {
	case 401, 403:
		msg = "upstream authentication failed, please contact the administrator"
	case 402:
		msg = "upstream account error, please contact the administrator"
	case 429:
		msg = "upstream rate limited, please retry later"
	default:
		if err.StatusCode >= 500 {
			msg = "upstream service error, please retry later"
		} else {
			// Other 4xx parameter-class errors: keep original text.
			return err
		}
	}

	return types.NewErrorWithStatusCode(errors.New(msg), types.ErrorCode("upstream_error"), err.StatusCode, types.ErrOptionWithSkipRetry())
}

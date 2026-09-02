package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func setAutomaticRetryStatusCodeRangesForTest(t *testing.T, ranges []operation_setting.StatusCodeRange) {
	t.Helper()
	original := operation_setting.GetOperationRuntimeConfig().AutomaticRetryStatusCodeRanges
	operation_setting.UpdateOperationRuntimeConfig(func(config *operation_setting.OperationRuntimeConfig) {
		config.AutomaticRetryStatusCodeRanges = ranges
	})
	t.Cleanup(func() {
		operation_setting.UpdateOperationRuntimeConfig(func(config *operation_setting.OperationRuntimeConfig) {
			config.AutomaticRetryStatusCodeRanges = original
		})
	})
}

func TestShouldRetryNeverRetriesSessionPolicyBlock(t *testing.T) {
	setAutomaticRetryStatusCodeRangesForTest(t, []operation_setting.StatusCodeRange{{Start: 403, End: 403}})

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	err := types.WithOpenAIError(types.OpenAIError{
		Message: "session blocked",
		Type:    "permission_error",
		Code:    "session_blocked_by_cyber_policy",
	}, http.StatusForbidden)

	assert.False(t, shouldRetry(c, err, 1))
}

func TestShouldRetryWithAffinityOnlyAllowsTransientFailures(t *testing.T) {
	setAutomaticRetryStatusCodeRangesForTest(t, []operation_setting.StatusCodeRange{
		{Start: 400, End: 403},
		{Start: 408, End: 408},
		{Start: 429, End: 429},
		{Start: 500, End: 599},
	})

	tests := []struct {
		name       string
		errorCode  types.ErrorCode
		statusCode int
		want       bool
	}{
		{name: "transport failure", errorCode: types.ErrorCodeDoRequestFailed, statusCode: http.StatusInternalServerError, want: true},
		{name: "channel response timeout", errorCode: types.ErrorCodeChannelResponseTimeExceeded, statusCode: http.StatusRequestTimeout, want: true},
		{name: "upstream request timeout", errorCode: "upstream_timeout", statusCode: http.StatusRequestTimeout, want: true},
		{name: "rate limit", errorCode: "rate_limit_exceeded", statusCode: http.StatusTooManyRequests, want: true},
		{name: "upstream server error", errorCode: "upstream_error", statusCode: http.StatusServiceUnavailable, want: true},
		{name: "bad request", errorCode: "invalid_request", statusCode: http.StatusBadRequest, want: false},
		{name: "unauthorized", errorCode: "invalid_api_key", statusCode: http.StatusUnauthorized, want: false},
		{name: "ordinary forbidden", errorCode: "forbidden", statusCode: http.StatusForbidden, want: false},
		{name: "session policy block", errorCode: types.ErrorCodeSessionBlockedByCyberPolicy, statusCode: http.StatusForbidden, want: false},
		{name: "channel configuration error", errorCode: types.ErrorCodeChannelParamOverrideInvalid, statusCode: http.StatusInternalServerError, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set("channel_affinity_skip_retry_on_failure", true)
			err := types.WithOpenAIError(types.OpenAIError{
				Message: test.name,
				Type:    "upstream_error",
				Code:    string(test.errorCode),
			}, test.statusCode)

			assert.Equal(t, test.want, shouldRetry(c, err, 1))
		})
	}
}

func TestShouldRetryAllowsConfiguredCloudflareTimeout(t *testing.T) {
	setAutomaticRetryStatusCodeRangesForTest(t, []operation_setting.StatusCodeRange{
		{Start: 524, End: 524},
	})

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("channel_affinity_skip_retry_on_failure", true)
	err := types.WithOpenAIError(types.OpenAIError{
		Message: "cloudflare upstream timeout",
		Type:    "upstream_error",
		Code:    "upstream_timeout",
	}, 524)

	assert.True(t, shouldRetry(c, err, 1))
}

func TestShouldRetryTaskRelaySkipsCloudflareTimeout(t *testing.T) {
	setAutomaticRetryStatusCodeRangesForTest(t, []operation_setting.StatusCodeRange{
		{Start: 500, End: 599},
	})

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	assert.False(t, shouldRetryTaskRelay(c, 19, &taskdto.TaskError{StatusCode: 504}, 1))
	assert.False(t, shouldRetryTaskRelay(c, 19, &taskdto.TaskError{StatusCode: 524}, 1))
	assert.True(t, shouldRetryTaskRelay(c, 19, &taskdto.TaskError{StatusCode: 503}, 1))
}

func TestShouldRetryStopsAfterDownstreamResponseStarted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	_, writeErr := c.Writer.Write([]byte("partial stream"))
	assert.NoError(t, writeErr)

	err := types.WithOpenAIError(types.OpenAIError{
		Message: "upstream failed after stream started",
		Type:    "upstream_error",
		Code:    "upstream_error",
	}, http.StatusServiceUnavailable)

	assert.False(t, shouldRetry(c, err, 1))
}

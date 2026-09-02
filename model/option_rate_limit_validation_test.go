package model

import (
	"testing"

	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateOptionValueRejectsUnsafeModelRateLimitScalars(t *testing.T) {
	valid := map[string]string{
		"ModelRequestRateLimitCount":           "0",
		"ModelRequestRateLimitDurationMinutes": "1440",
		"ModelRequestRateLimitSuccessCount":    "100000000",
	}
	for key, value := range valid {
		require.NoError(t, validateOptionValue(key, value), key)
	}

	invalid := map[string]string{
		"ModelRequestRateLimitCount":           "-1",
		"ModelRequestRateLimitDurationMinutes": "0",
		"ModelRequestRateLimitSuccessCount":    "0",
	}
	for key, value := range invalid {
		assert.Error(t, validateOptionValue(key, value), key)
	}
	assert.Error(t, validateOptionValue("ModelRequestRateLimitCount", "100000001"))
	assert.Error(t, validateOptionValue("ModelRequestRateLimitDurationMinutes", "9223372036854775807"))
	assert.Error(t, validateOptionValue("ModelRequestRateLimitSuccessCount", "not-an-int"))

	// Keep the test tied to the public contract used by the validator.
	assert.Equal(t, 100000000, setting.MaxModelRequestRateLimitCount)
}

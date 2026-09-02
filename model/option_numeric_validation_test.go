package model

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateOptionValueRejectsUnsafeNumericRanges(t *testing.T) {
	valid := map[string]string{
		"SMTPPort":                "587",
		"MinTopUp":                "0",
		"StripeMinTopUp":          "1",
		"WaffoMinTopUp":           "10",
		"WaffoPancakeMinTopUp":    "10",
		"QuotaForNewUser":         "0",
		"QuotaForInviter":         "1000",
		"QuotaForInvitee":         "1000",
		"QuotaRemindThreshold":    "1000",
		"PreConsumedQuota":        "500",
		"RetryTimes":              "3",
		"DataExportInterval":      "60",
		"StreamCacheQueueLength":  "0",
		"QuotaPerUnit":            "500000",
		"ChannelDisableThreshold": "5",
	}
	for key, value := range valid {
		require.NoError(t, validateOptionValue(key, value), key)
	}

	invalid := map[string]string{
		"SMTPPort":                "0",
		"MinTopUp":                "-1",
		"StripeMinTopUp":          "-1",
		"WaffoMinTopUp":           "-1",
		"WaffoPancakeMinTopUp":    "-1",
		"QuotaForNewUser":         "-1",
		"QuotaForInviter":         "-1",
		"QuotaForInvitee":         "-1",
		"QuotaRemindThreshold":    "-1",
		"PreConsumedQuota":        "-1",
		"RetryTimes":              "-1",
		"StreamCacheQueueLength":  "-1",
		"QuotaPerUnit":            "0",
		"ChannelDisableThreshold": "-1",
	}
	for key, value := range invalid {
		assert.Error(t, validateOptionValue(key, value), key)
	}

	assert.Error(t, validateOptionValue("SMTPPort", "65536"))
	assert.Error(t, validateOptionValue("RetryTimes", "1001"))
	assert.Error(t, validateOptionValue("StreamCacheQueueLength", "1000001"))
	assert.Error(t, validateOptionValue("PreConsumedQuota", "2147483648"))
	assert.Error(t, validateOptionValue("QuotaPerUnit", "NaN"))
	assert.Error(t, validateOptionValue("QuotaPerUnit", "Inf"))
	assert.Error(t, validateOptionValue("QuotaPerUnit", "-1"))
	assert.Error(t, validateOptionValue("ChannelDisableThreshold", "604800.1"))
	assert.Error(t, validateOptionValue("Price", "0"))
	assert.Error(t, validateOptionValue("USDExchangeRate", "-1"))
	assert.Error(t, validateOptionValue("StripeUnitPrice", "0"))

	// Keep the test meaningful if the platform's integer width changes: the
	// validator must still reject values that cannot fit the shared quota type.
	assert.Error(t, validateOptionValue("QuotaForNewUser", "9223372036854775807"))
	assert.False(t, math.IsNaN(common.GetQuotaConfig().QuotaPerUnit))
}

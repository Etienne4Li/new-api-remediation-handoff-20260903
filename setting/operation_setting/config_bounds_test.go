package operation_setting

import (
	"testing"

	"github.com/QuantumNous/new-api/setting/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeneralSettingRejectsUnsafeNumericAndEnumValues(t *testing.T) {
	registered := config.GlobalConfig.Get("general_setting")
	require.NotNil(t, registered)
	original := GetGeneralSettingSnapshot()
	t.Cleanup(func() {
		UpdateGeneralSetting(func(setting *GeneralSetting) { *setting = original })
	})

	for key, value := range map[string]string{
		"ping_interval_seconds":         "0",
		"custom_currency_exchange_rate": "NaN",
		"quota_display_type":            "unsupported",
	} {
		require.Error(t, config.UpdateConfigFromMap(registered, map[string]string{key: value}), key)
		assert.Equal(t, original, GetGeneralSettingSnapshot())
	}

	require.NoError(t, config.UpdateConfigFromMap(registered, map[string]string{
		"ping_interval_seconds":         "300",
		"custom_currency_exchange_rate": "2.5",
		"quota_display_type":            QuotaDisplayTypeCustom,
	}))
	updated := GetGeneralSettingSnapshot()
	assert.Equal(t, 300, updated.PingIntervalSeconds)
	assert.Equal(t, 2.5, updated.CustomCurrencyExchangeRate)
}

func TestMonitorSettingRejectsUnsafeScheduleValues(t *testing.T) {
	t.Setenv("CHANNEL_TEST_ENABLED", "")
	t.Setenv("CHANNEL_TEST_FREQUENCY", "")
	registered := config.GlobalConfig.Get("monitor_setting")
	require.NotNil(t, registered)
	original := GetMonitorSettingSnapshot()
	t.Cleanup(func() {
		UpdateMonitorSetting(func(setting *MonitorSetting) { *setting = original })
	})

	for key, value := range map[string]string{
		"auto_test_channel_minutes": "NaN",
		"channel_test_mode":         "invalid",
		"channel_test_concurrency":  "0",
	} {
		require.Error(t, config.UpdateConfigFromMap(registered, map[string]string{key: value}), key)
		assert.Equal(t, original, GetMonitorSettingSnapshot())
	}

	require.NoError(t, config.UpdateConfigFromMap(registered, map[string]string{
		"auto_test_channel_minutes": "15",
		"channel_test_mode":         ChannelTestModePassiveRecovery,
		"channel_test_concurrency":  "4",
	}))
	updated := GetMonitorSettingSnapshot()
	assert.Equal(t, 15.0, updated.AutoTestChannelMinutes)
	assert.Equal(t, ChannelTestModePassiveRecovery, updated.ChannelTestMode)
	assert.Equal(t, 4, updated.ChannelTestConcurrency)
}

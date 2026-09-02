package perf_metrics_setting

import (
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/setting/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPerfMetricsSettingRejectsUnsafeIntervalsAndRetention(t *testing.T) {
	registered := config.GlobalConfig.Get("perf_metrics_setting")
	require.NotNil(t, registered)
	original := GetSetting()
	t.Cleanup(func() {
		require.NoError(t, config.UpdateConfigFromMap(registered, map[string]string{
			"enabled":        strconv.FormatBool(original.Enabled),
			"flush_interval": strconv.Itoa(original.FlushInterval),
			"bucket_time":    original.BucketTime,
			"retention_days": strconv.Itoa(original.RetentionDays),
		}))
	})

	for key, value := range map[string]string{
		"flush_interval": "0",
		"retention_days": "-1",
		"bucket_time":    "day",
	} {
		require.Error(t, config.UpdateConfigFromMap(registered, map[string]string{key: value}), key)
		assert.Equal(t, original, GetSetting())
	}

	require.NoError(t, config.UpdateConfigFromMap(registered, map[string]string{
		"flush_interval": "30",
		"bucket_time":    "5min",
		"retention_days": "365",
	}))
	updated := GetSetting()
	assert.Equal(t, 30, updated.FlushInterval)
	assert.Equal(t, "5min", updated.BucketTime)
	assert.Equal(t, 365, updated.RetentionDays)
}

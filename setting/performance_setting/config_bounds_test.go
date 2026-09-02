package performance_setting

import (
	"strconv"
	"testing"

	"github.com/QuantumNous/new-api/setting/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPerformanceSettingRejectsUnsafeCacheAndMonitorValues(t *testing.T) {
	registered := config.GlobalConfig.Get("performance_setting")
	require.NotNil(t, registered)
	original := *GetPerformanceSetting()
	t.Cleanup(func() {
		require.NoError(t, config.UpdateConfigFromMap(registered, map[string]string{
			"disk_cache_enabled":       strconv.FormatBool(original.DiskCacheEnabled),
			"disk_cache_threshold_mb":  strconv.Itoa(original.DiskCacheThresholdMB),
			"disk_cache_max_size_mb":   strconv.Itoa(original.DiskCacheMaxSizeMB),
			"disk_cache_path":          original.DiskCachePath,
			"monitor_enabled":          strconv.FormatBool(original.MonitorEnabled),
			"monitor_cpu_threshold":    strconv.Itoa(original.MonitorCPUThreshold),
			"monitor_memory_threshold": strconv.Itoa(original.MonitorMemoryThreshold),
			"monitor_disk_threshold":   strconv.Itoa(original.MonitorDiskThreshold),
		}))
	})

	for key, value := range map[string]string{
		"disk_cache_threshold_mb":  "1048577",
		"disk_cache_max_size_mb":   "-1",
		"monitor_cpu_threshold":    "101",
		"monitor_memory_threshold": "-1",
		"monitor_disk_threshold":   "9223372036854775807",
	} {
		require.Error(t, config.UpdateConfigFromMap(registered, map[string]string{key: value}), key)
		assert.Equal(t, original, *GetPerformanceSetting())
	}

	// A threshold larger than the configured cache capacity is rejected even
	// though both individual values are otherwise in range.
	require.Error(t, config.UpdateConfigFromMap(registered, map[string]string{
		"disk_cache_threshold_mb": "20",
		"disk_cache_max_size_mb":  "10",
	}))
	assert.Equal(t, original, *GetPerformanceSetting())
}

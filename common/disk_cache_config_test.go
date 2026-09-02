package common

import (
	"math"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiskCacheConfigRejectsInvalidMiBValues(t *testing.T) {
	previous := GetDiskCacheConfig()
	t.Cleanup(func() { SetDiskCacheConfig(previous) })

	SetDiskCacheConfig(DiskCacheConfig{Enabled: true, ThresholdMB: -1, MaxSizeMB: -1})
	require.Zero(t, GetDiskCacheThresholdBytes())
	require.Zero(t, GetDiskCacheMaxSizeBytes())
	require.False(t, IsDiskCacheAvailable(1))

	// An int-sized value that would overflow when shifted must be clamped to a
	// positive finite limit rather than wrapping negative.
	SetDiskCacheConfig(DiskCacheConfig{
		Enabled:     true,
		ThresholdMB: int(^uint(0) >> 1),
		MaxSizeMB:   int(^uint(0) >> 1),
	})
	require.Equal(t, int64(math.MaxInt64), GetDiskCacheThresholdBytes())
	require.Equal(t, int64(math.MaxInt64), GetDiskCacheMaxSizeBytes())
}

func TestIsDiskCacheAvailableAvoidsUsageAdditionOverflow(t *testing.T) {
	previous := GetDiskCacheConfig()
	previousUsage := atomic.LoadInt64(&diskCacheStats.CurrentDiskUsageBytes)
	t.Cleanup(func() {
		SetDiskCacheConfig(previous)
		atomic.StoreInt64(&diskCacheStats.CurrentDiskUsageBytes, previousUsage)
	})

	SetDiskCacheConfig(DiskCacheConfig{Enabled: true, MaxSizeMB: 1})
	atomic.StoreInt64(&diskCacheStats.CurrentDiskUsageBytes, math.MaxInt64)
	require.False(t, IsDiskCacheAvailable(1), "corrupt over-limit usage must fail closed")

	atomic.StoreInt64(&diskCacheStats.CurrentDiskUsageBytes, 1<<20-1)
	require.True(t, IsDiskCacheAvailable(1))
	require.False(t, IsDiskCacheAvailable(2))
	atomic.StoreInt64(&diskCacheStats.CurrentDiskUsageBytes, -1)
	require.False(t, IsDiskCacheAvailable(1), "negative usage must fail closed")
	require.False(t, IsDiskCacheAvailable(-1), "negative request size must fail closed")
}

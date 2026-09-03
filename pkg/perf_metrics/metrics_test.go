/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.
*/
package perfmetrics

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildQueryResultIncludesGroupAndBucketCounts(t *testing.T) {
	result := buildQueryResult("model-a", map[bucketKey]counters{
		{model: "model-a", group: "fast", bucketTs: 200}: {
			requestCount:    2,
			successCount:    2,
			totalLatencyMs:  200,
			inputTokens:     400,
			cacheReadTokens: 300,
			cacheRequests:   2,
		},
		{model: "model-a", group: "fast", bucketTs: 100}: {
			requestCount:    3,
			successCount:    1,
			totalLatencyMs:  300,
			inputTokens:     600,
			cacheReadTokens: 200,
			cacheRequests:   3,
		},
		{model: "model-a", group: "slow", bucketTs: 100}: {
			requestCount:   1,
			successCount:   0,
			totalLatencyMs: 150,
		},
	})

	require.Equal(t, "model-a", result.ModelName)
	require.Len(t, result.Groups, 2)
	require.Equal(t, "fast", result.Groups[0].Group)
	require.Equal(t, int64(5), result.Groups[0].RequestCount)
	require.Equal(t, int64(3), result.Groups[0].SuccessCount)
	require.InDelta(t, 60, result.Groups[0].SuccessRate, 0.001)
	require.Equal(t, int64(1000), result.Groups[0].InputTokens)
	require.Equal(t, int64(500), result.Groups[0].CacheReadTokens)
	require.Equal(t, int64(5), result.Groups[0].CacheRequests)
	require.NotNil(t, result.Groups[0].CacheHitRate)
	require.InDelta(t, 50, *result.Groups[0].CacheHitRate, 0.001)
	require.Len(t, result.Groups[0].Series, 2)
	require.Equal(t, int64(100), result.Groups[0].Series[0].Ts)
	require.Equal(t, int64(3), result.Groups[0].Series[0].RequestCount)
	require.Equal(t, int64(1), result.Groups[0].Series[0].SuccessCount)
	require.NotNil(t, result.Groups[0].Series[0].CacheHitRate)
	require.InDelta(t, 100.0/3.0, *result.Groups[0].Series[0].CacheHitRate, 0.01)
	require.Equal(t, int64(200), result.Groups[0].Series[1].Ts)
	require.Equal(t, int64(2), result.Groups[0].Series[1].RequestCount)
	require.Equal(t, int64(2), result.Groups[0].Series[1].SuccessCount)
	require.NotNil(t, result.Groups[0].Series[1].CacheHitRate)
	require.InDelta(t, 75, *result.Groups[0].Series[1].CacheHitRate, 0.001)

	require.Equal(t, "slow", result.Groups[1].Group)
	require.Equal(t, int64(1), result.Groups[1].RequestCount)
	require.Equal(t, int64(0), result.Groups[1].SuccessCount)
	require.Zero(t, result.Groups[1].CacheRequests)
	require.Nil(t, result.Groups[1].CacheHitRate)
}

func TestAtomicBucketTracksCacheTokensOnlyForObservedUsage(t *testing.T) {
	bucket := &atomicBucket{}
	bucket.add(Sample{
		Success:         true,
		InputTokens:     100,
		CacheReadTokens: 40,
		CacheObserved:   true,
	})
	bucket.add(Sample{
		Success:         true,
		InputTokens:     500,
		CacheReadTokens: 500,
		CacheObserved:   false,
	})
	bucket.add(Sample{
		Success:         false,
		InputTokens:     100,
		CacheReadTokens: 100,
		CacheObserved:   true,
	})

	snapshot := bucket.snapshot()
	assert.Equal(t, int64(3), snapshot.requestCount)
	assert.Equal(t, int64(2), snapshot.successCount)
	assert.Equal(t, int64(100), snapshot.inputTokens)
	assert.Equal(t, int64(40), snapshot.cacheReadTokens)
	assert.Equal(t, int64(1), snapshot.cacheRequests)
	assert.InDelta(t, 40, cacheHitRate(snapshot), 0.001)
}

func TestModelSummaryExposesCacheMetricsWithoutFabricatingLegacyData(t *testing.T) {
	totals := map[string]counters{
		"observed": {
			requestCount:    2,
			successCount:    2,
			ttftSumMs:       120,
			ttftCount:       2,
			inputTokens:     200,
			cacheReadTokens: 50,
			cacheRequests:   2,
		},
		"legacy": {
			requestCount: 3,
			successCount: 3,
		},
	}

	observed := modelSummary("observed", totals["observed"], nil)
	legacy := modelSummary("legacy", totals["legacy"], nil)

	assert.Equal(t, int64(200), observed.InputTokens)
	assert.Equal(t, int64(60), observed.AvgTtftMs)
	assert.Equal(t, int64(50), observed.CacheReadTokens)
	assert.Equal(t, int64(2), observed.CacheRequests)
	require.NotNil(t, observed.CacheHitRate)
	assert.InDelta(t, 25, *observed.CacheHitRate, 0.001)
	assert.Zero(t, legacy.CacheRequests)
	assert.Nil(t, legacy.CacheHitRate)

	encoded, err := common.Marshal(legacy)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "cache_hit_rate")
	assert.NotContains(t, string(encoded), "cache_observed_requests")
	assert.NotContains(t, string(encoded), "input_tokens")
	assert.NotContains(t, string(encoded), "cache_read_tokens")
}

func TestRedisCountersReadsCacheFieldsAndKeepsOldHashesCompatible(t *testing.T) {
	old := redisCounters(map[string]string{
		"req": "2",
		"ok":  "2",
	})
	assert.Equal(t, int64(2), old.requestCount)
	assert.Zero(t, old.cacheRequests)
	assert.Zero(t, old.inputTokens)
	assert.Zero(t, old.cacheReadTokens)

	current := redisCounters(map[string]string{
		"req":        "3",
		"ok":         "3",
		"cache_req":  "2",
		"in":         "1000",
		"cache_read": "750",
	})
	assert.Equal(t, int64(2), current.cacheRequests)
	assert.Equal(t, int64(1000), current.inputTokens)
	assert.Equal(t, int64(750), current.cacheReadTokens)
	assert.InDelta(t, 75, cacheHitRate(current), 0.001)
}

func TestBucketPointCarriesRawSums(t *testing.T) {
	value := counters{
		requestCount:   4,
		successCount:   3,
		totalLatencyMs: 9000,
		ttftSumMs:      300,
		ttftCount:      2,
		outputTokens:   500,
		generationMs:   2500,
	}
	pt := bucketPoint(0, value)
	assert.Equal(t, int64(9000), pt.TotalLatencyMs)
	assert.Equal(t, int64(300), pt.TtftSumMs)
	assert.Equal(t, int64(2), pt.TtftCount)
	assert.Equal(t, int64(500), pt.OutputTokens)
	assert.Equal(t, int64(2500), pt.GenerationMs)
	assert.Equal(t, int64(150), pt.AvgTtftMs)
	assert.Equal(t, int64(2250), pt.AvgLatencyMs)
	assert.InDelta(t, 200, pt.AvgTps, 0.001)
}

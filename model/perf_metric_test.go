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
package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPerfMetricUpsertAndSummaryPreserveCacheObservation(t *testing.T) {
	truncateTables(t)
	first := &PerfMetric{
		ModelName:        "model-a",
		Group:            "default",
		BucketTs:         100,
		RequestCount:     2,
		SuccessCount:     2,
		TotalLatencyMs:   200,
		TtftSumMs:        80,
		TtftCount:        2,
		OutputTokens:     50,
		GenerationMs:     1000,
		InputTokens:      400,
		CacheReadTokens:  100,
		CacheWriteTokens: 20,
		CacheRequests:    2,
	}
	second := &PerfMetric{
		ModelName:        "model-a",
		Group:            "default",
		BucketTs:         100,
		RequestCount:     1,
		SuccessCount:     1,
		TotalLatencyMs:   100,
		TtftSumMs:        70,
		TtftCount:        1,
		OutputTokens:     25,
		GenerationMs:     500,
		InputTokens:      200,
		CacheReadTokens:  50,
		CacheWriteTokens: 10,
		CacheRequests:    1,
	}
	require.NoError(t, UpsertPerfMetric(first))
	require.NoError(t, UpsertPerfMetric(second))

	rows, err := GetPerfMetrics("model-a", "default", 0, 200)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, int64(3), rows[0].RequestCount)
	assert.Equal(t, int64(150), rows[0].TtftSumMs)
	assert.Equal(t, int64(3), rows[0].TtftCount)
	assert.Equal(t, int64(600), rows[0].InputTokens)
	assert.Equal(t, int64(150), rows[0].CacheReadTokens)
	assert.Equal(t, int64(30), rows[0].CacheWriteTokens)
	assert.Equal(t, int64(3), rows[0].CacheRequests)

	summaries, err := GetPerfMetricsSummaryAll(0, 200, []string{"default"})
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.Equal(t, int64(600), summaries[0].InputTokens)
	assert.Equal(t, int64(150), summaries[0].TtftSumMs)
	assert.Equal(t, int64(3), summaries[0].TtftCount)
	assert.Equal(t, int64(150), summaries[0].CacheReadTokens)
	assert.Equal(t, int64(30), summaries[0].CacheWriteTokens)
	assert.Equal(t, int64(3), summaries[0].CacheRequests)

	buckets, err := GetPerfMetricsSummaryBucketsAll(0, 200, []string{"default"})
	require.NoError(t, err)
	require.Len(t, buckets, 1)
	assert.Equal(t, int64(600), buckets[0].InputTokens)
	assert.Equal(t, int64(150), buckets[0].TtftSumMs)
	assert.Equal(t, int64(3), buckets[0].TtftCount)
	assert.Equal(t, int64(150), buckets[0].CacheReadTokens)
	assert.Equal(t, int64(30), buckets[0].CacheWriteTokens)
	assert.Equal(t, int64(3), buckets[0].CacheRequests)
}

func TestPerfMetricLegacyRowsRemainUnobserved(t *testing.T) {
	truncateTables(t)
	require.NoError(t, DB.Create(&PerfMetric{
		ModelName:    "legacy-model",
		Group:        "default",
		BucketTs:     100,
		RequestCount: 4,
		SuccessCount: 4,
	}).Error)

	summaries, err := GetPerfMetricsSummaryAll(0, 200, nil)
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.Zero(t, summaries[0].InputTokens)
	assert.Zero(t, summaries[0].CacheReadTokens)
	assert.Zero(t, summaries[0].CacheRequests)
}

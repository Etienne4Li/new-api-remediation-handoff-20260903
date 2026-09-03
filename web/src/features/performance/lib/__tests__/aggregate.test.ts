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
import { describe, expect, test } from 'vitest'

import {
  aggregatePerformanceGroups,
  getOverallSuccessRate,
  getModelGroupRows,
  getPerformanceStatus,
  getRecentStatusSeries,
  mergeConfiguredPerformanceGroups,
  orderPerformanceGroups,
  type AggregatedPerformanceGroup,
  type ModelPerformanceDetail,
} from '../aggregate'

function detail(
  model_name: string,
  groups: ModelPerformanceDetail['groups']
): ModelPerformanceDetail {
  return {
    summary: {
      model_name,
      avg_latency_ms: 120,
      success_rate: 98,
      avg_tps: 16,
    },
    groups,
  }
}

function groupFixture(
  group: string,
  overrides: Partial<AggregatedPerformanceGroup> = {}
): AggregatedPerformanceGroup {
  return {
    group,
    modelNames: ['alpha'],
    requestCount: 0,
    successCount: 0,
    totalLatencyMs: 0,
    ttftSumMs: 0,
    ttftCount: 0,
    outputTokens: 0,
    generationMs: 0,
    avgTtftMs: Number.NaN,
    avgLatencyMs: Number.NaN,
    avgTps: Number.NaN,
    successRate: Number.NaN,
    inputTokens: 0,
    cacheReadTokens: 0,
    cacheWriteTokens: 0,
    cacheHitRate: Number.NaN,
    series: [],
    ...overrides,
  }
}

describe('aggregatePerformanceGroups', () => {
  test('merges same group metrics and averages series by timestamp', () => {
    const groups = aggregatePerformanceGroups([
      detail('alpha', [
        {
          group: 'fast',
          avg_ttft_ms: 100,
          avg_latency_ms: 200,
          success_rate: 100,
          avg_tps: 20,
          series: [
            {
              ts: 10,
              avg_ttft_ms: 100,
              avg_latency_ms: 200,
              success_rate: 100,
              avg_tps: 20,
            },
          ],
        },
      ]),
      detail('beta', [
        {
          group: 'fast',
          avg_ttft_ms: 200,
          avg_latency_ms: 400,
          success_rate: 80,
          avg_tps: 10,
          series: [
            {
              ts: 10,
              avg_ttft_ms: 200,
              avg_latency_ms: 400,
              success_rate: 80,
              avg_tps: 10,
            },
          ],
        },
      ]),
    ])

    expect(groups).toHaveLength(1)
    expect(groups[0]).toMatchObject({
      group: 'fast',
      modelNames: ['alpha', 'beta'],
      avgTtftMs: 150,
      avgLatencyMs: 300,
      avgTps: 15,
      successRate: 90,
      cacheHitRate: Number.NaN,
    })
    expect(groups[0].series[0]).toMatchObject({
      ts: 10,
      successRate: 90,
      cacheHitRate: Number.NaN,
    })
  })

  test('does not turn missing or zero metrics into fake positive values', () => {
    const [group] = aggregatePerformanceGroups([
      detail('alpha', [
        {
          group: 'empty',
          avg_ttft_ms: 0,
          avg_latency_ms: Number.NaN,
          success_rate: 0,
          avg_tps: 0,
          series: [],
        },
      ]),
    ])

    expect(group.avgTtftMs).toBe(Number.NaN)
    expect(group.avgLatencyMs).toBe(Number.NaN)
    expect(group.avgTps).toBe(Number.NaN)
    expect(group.successRate).toBe(0)
  })

  test('weights group metrics by request counts when the API provides them', () => {
    const [group] = aggregatePerformanceGroups([
      detail('busy', [
        {
          group: 'weighted',
          request_count: 90,
          success_count: 90,
          avg_ttft_ms: 100,
          avg_latency_ms: 200,
          success_rate: 100,
          avg_tps: 20,
          series: [],
        },
      ]),
      detail('quiet', [
        {
          group: 'weighted',
          request_count: 10,
          success_count: 0,
          avg_ttft_ms: 500,
          avg_latency_ms: 800,
          success_rate: 0,
          avg_tps: 10,
          series: [],
        },
      ]),
    ])

    expect(group).toMatchObject({
      requestCount: 100,
      successCount: 90,
      avgTtftMs: 140,
      avgLatencyMs: 260,
      avgTps: 19,
      successRate: 90,
    })
  })

  test('derives exact averages from raw sums when the API provides them', () => {
    const [group] = aggregatePerformanceGroups([
      detail('model-a', [
        {
          group: 'g',
          request_count: 1,
          success_count: 1,
          total_latency_ms: 1000,
          ttft_sum_ms: 100,
          ttft_count: 1,
          output_tokens: 100,
          generation_ms: 1000,
          avg_latency_ms: 1000,
          avg_ttft_ms: 100,
          avg_tps: 100,
          success_rate: 100,
          series: [],
        },
      ]),
      detail('model-b', [
        {
          group: 'g',
          request_count: 3,
          success_count: 3,
          total_latency_ms: 9000,
          ttft_sum_ms: 0,
          ttft_count: 0,
          output_tokens: 300,
          generation_ms: 3000,
          avg_latency_ms: 3000,
          avg_ttft_ms: 0,
          avg_tps: 100,
          success_rate: 100,
          series: [],
        },
      ]),
    ])

    expect(group).toMatchObject({
      avgLatencyMs: 2500,
      avgTtftMs: 100,
      avgTps: 100,
      requestCount: 4,
      ttftCount: 1,
    })
  })

  test('weights cache hit rate by observed input tokens and aggregates token totals', () => {
    const [group] = aggregatePerformanceGroups([
      detail('busy', [
        {
          group: 'cached',
          request_count: 2,
          success_count: 2,
          input_tokens: 900,
          cache_read_tokens: 720,
          cache_write_tokens: 50,
          cache_hit_rate: 1,
          avg_ttft_ms: 100,
          avg_latency_ms: 200,
          success_rate: 100,
          avg_tps: 20,
          series: [
            {
              ts: 10,
              request_count: 2,
              success_count: 2,
              input_tokens: 900,
              cache_read_tokens: 720,
              cache_write_tokens: 50,
              cache_hit_rate: 1,
              avg_ttft_ms: 100,
              avg_latency_ms: 200,
              success_rate: 100,
              avg_tps: 20,
            },
          ],
        },
      ]),
      detail('quiet', [
        {
          group: 'cached',
          request_count: 1,
          success_count: 1,
          input_tokens: 100,
          cache_read_tokens: 20,
          cache_write_tokens: 10,
          avg_ttft_ms: 100,
          avg_latency_ms: 200,
          success_rate: 100,
          avg_tps: 20,
          series: [
            {
              ts: 10,
              request_count: 1,
              success_count: 1,
              input_tokens: 100,
              cache_read_tokens: 20,
              cache_write_tokens: 10,
              avg_ttft_ms: 100,
              avg_latency_ms: 200,
              success_rate: 100,
              avg_tps: 20,
            },
          ],
        },
      ]),
    ])

    expect(group).toMatchObject({
      inputTokens: 1000,
      cacheReadTokens: 740,
      cacheWriteTokens: 60,
      cacheHitRate: 74,
    })
    expect(group.series[0]).toMatchObject({
      inputTokens: 1000,
      cacheReadTokens: 740,
      cacheWriteTokens: 60,
      cacheHitRate: 74,
    })
  })

  test('keeps cache hit rate unavailable when no input token observation exists', () => {
    const [group] = aggregatePerformanceGroups([
      detail('legacy', [
        {
          group: 'legacy',
          avg_ttft_ms: 100,
          avg_latency_ms: 200,
          success_rate: 100,
          avg_tps: 20,
          series: [],
        },
      ]),
    ])

    expect(group.inputTokens).toBe(0)
    expect(group.cacheReadTokens).toBe(0)
    expect(group.cacheHitRate).toBe(Number.NaN)
  })

  test('keeps configured groups with no requests in the empty state', () => {
    const observed = aggregatePerformanceGroups([
      detail('alpha', [
        {
          group: 'fast',
          request_count: 10,
          success_count: 10,
          avg_ttft_ms: 100,
          avg_latency_ms: 200,
          success_rate: 100,
          avg_tps: 20,
          series: [],
        },
      ]),
    ])

    const groups = mergeConfiguredPerformanceGroups(observed, [
      { group: 'fast', description: 'Primary route', ratio: 0.5 },
      { group: 'idle', description: 'No traffic yet', ratio: '自动' },
    ])

    expect(groups).toHaveLength(2)
    expect(groups[0]).toMatchObject({
      group: 'fast',
      description: 'Primary route',
      ratio: 0.5,
    })
    expect(groups[1]).toMatchObject({
      group: 'idle',
      description: 'No traffic yet',
      ratio: '自动',
      requestCount: 0,
      modelNames: [],
    })
    expect(
      getPerformanceStatus(
        groups[1].successRate,
        groups[1].modelNames.length > 0
      )
    ).toBe('empty')
  })
})

describe('performance helpers', () => {
  test('maps health thresholds to stable status values', () => {
    expect(getPerformanceStatus(100)).toBe('healthy')
    expect(getPerformanceStatus(95)).toBe('degraded')
    expect(getPerformanceStatus(45)).toBe('critical')
    expect(getPerformanceStatus(Number.NaN)).toBe('empty')
    expect(getPerformanceStatus(0, false)).toBe('empty')
  })

  test('returns the latest status buckets and group-specific rows', () => {
    const source = [
      detail('alpha', [
        {
          group: 'edge',
          avg_ttft_ms: 1,
          avg_latency_ms: 2,
          success_rate: 100,
          avg_tps: 1,
          series: [
            {
              ts: 1,
              avg_ttft_ms: 1,
              avg_latency_ms: 2,
              success_rate: 99,
              avg_tps: 1,
            },
            {
              ts: 2,
              avg_ttft_ms: 1,
              avg_latency_ms: 2,
              success_rate: 100,
              avg_tps: 1,
            },
          ],
        },
      ]),
      detail('beta', [
        {
          group: 'other',
          avg_ttft_ms: 1,
          avg_latency_ms: 2,
          success_rate: 100,
          avg_tps: 1,
          series: [],
        },
      ]),
    ]
    const [edge] = aggregatePerformanceGroups(source)
    expect(getRecentStatusSeries(edge, 1)[0]).toMatchObject({
      ts: 2,
      successRate: 100,
      cacheHitRate: Number.NaN,
    })
    expect(
      getModelGroupRows(source, 'edge').map((row) => row.summary.model_name)
    ).toEqual(['alpha'])
  })

  test('weights the workspace success rate by request count', () => {
    expect(
      getOverallSuccessRate([
        {
          group: 'busy',
          modelNames: ['alpha'],
          requestCount: 90,
          successCount: 90,
          totalLatencyMs: 0,
          ttftSumMs: 0,
          ttftCount: 0,
          outputTokens: 0,
          generationMs: 0,
          avgTtftMs: 1,
          avgLatencyMs: 1,
          avgTps: 1,
          successRate: 100,
          inputTokens: 0,
          cacheReadTokens: 0,
          cacheWriteTokens: 0,
          cacheHitRate: Number.NaN,
          series: [],
        },
        {
          group: 'quiet',
          modelNames: ['beta'],
          requestCount: 10,
          successCount: 0,
          totalLatencyMs: 0,
          ttftSumMs: 0,
          ttftCount: 0,
          outputTokens: 0,
          generationMs: 0,
          avgTtftMs: 1,
          avgLatencyMs: 1,
          avgTps: 1,
          successRate: 0,
          inputTokens: 0,
          cacheReadTokens: 0,
          cacheWriteTokens: 0,
          cacheHitRate: Number.NaN,
          series: [],
        },
      ])
    ).toBe(90)
  })
})

describe('orderPerformanceGroups', () => {
  test('returns the original order for an empty or undefined order list', () => {
    const groups = [
      groupFixture('alpha'),
      groupFixture('beta'),
      groupFixture('gamma'),
    ]

    expect(orderPerformanceGroups(groups, [])).toEqual(groups)
    expect(orderPerformanceGroups(groups, undefined)).toEqual(groups)
    expect(orderPerformanceGroups(groups, null)).toEqual(groups)
  })

  test('places listed groups first and keeps unlisted groups in relative order', () => {
    const groups = [
      groupFixture('alpha'),
      groupFixture('beta'),
      groupFixture('gamma'),
      groupFixture('delta'),
    ]

    const ordered = orderPerformanceGroups(groups, ['gamma', 'alpha'])

    expect(ordered.map((group) => group.group)).toEqual([
      'gamma',
      'alpha',
      'beta',
      'delta',
    ])
  })

  test('ignores unknown group names in the order without throwing', () => {
    const groups = [
      groupFixture('alpha'),
      groupFixture('beta'),
      groupFixture('gamma'),
    ]

    expect(() =>
      orderPerformanceGroups(groups, ['unknown', 'beta', 'missing'])
    ).not.toThrow()

    const ordered = orderPerformanceGroups(groups, ['unknown', 'beta'])

    expect(ordered.map((group) => group.group)).toEqual([
      'beta',
      'alpha',
      'gamma',
    ])
  })
})

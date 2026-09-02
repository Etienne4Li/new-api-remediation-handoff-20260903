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
import type {
  PerformanceGroup,
  PerformanceSeriesPoint,
  PerfModelSummary,
} from '@/features/performance-metrics/types'

export type ModelPerformanceDetail = {
  summary: PerfModelSummary
  groups: PerformanceGroup[]
}

export type AggregatedSeriesPoint = {
  ts: number
  successRate: number
  inputTokens: number
  cacheReadTokens: number
  cacheWriteTokens: number
  cacheHitRate: number
}

export type AggregatedPerformanceGroup = {
  group: string
  modelNames: string[]
  description?: string
  ratio?: number | string
  requestCount: number
  successCount: number
  avgTtftMs: number
  avgLatencyMs: number
  avgTps: number
  successRate: number
  inputTokens: number
  cacheReadTokens: number
  cacheWriteTokens: number
  cacheHitRate: number
  series: AggregatedSeriesPoint[]
}

export type PerformanceGroupConfig = {
  group: string
  description?: string
  ratio?: number | string
}

export type PerformanceStatus = 'healthy' | 'degraded' | 'critical' | 'empty'

type MetricAccumulator = {
  ttft: number[]
  latency: number[]
  tps: number[]
  success: number[]
  ttftWeightedSum: number
  ttftWeightedRequests: number
  latencyWeightedSum: number
  latencyWeightedRequests: number
  tpsWeightedSum: number
  tpsWeightedRequests: number
}

type GroupAccumulator = MetricAccumulator & {
  modelNames: Set<string>
  requestCount: number
  successCount: number
  inputTokens: number
  cacheReadTokens: number
  cacheWriteTokens: number
  cacheRates: number[]
  series: Map<number, number[]>
  seriesCounts: Map<number, { requests: number; successes: number }>
  seriesTokens: Map<
    number,
    {
      inputTokens: number
      cacheReadTokens: number
      cacheWriteTokens: number
      cacheRates: number[]
    }
  >
}

function finiteValues(values: number[], includeZero = false): number[] {
  return values.filter(
    (value) => Number.isFinite(value) && (includeZero || value > 0)
  )
}

function average(values: number[], includeZero = false): number {
  const valid = finiteValues(values, includeZero)
  if (valid.length === 0) return Number.NaN
  return valid.reduce((sum, value) => sum + value, 0) / valid.length
}

function addSeriesPoint(
  series: Map<number, number[]>,
  seriesCounts: Map<number, { requests: number; successes: number }>,
  seriesTokens: GroupAccumulator['seriesTokens'],
  point: PerformanceSeriesPoint
): void {
  const ts = Number(point.ts)
  const successRate = Number(point.success_rate)
  if (!Number.isFinite(ts) || !Number.isFinite(successRate)) return
  const values = series.get(ts) ?? []
  values.push(successRate)
  series.set(ts, values)

  const requests = Number(point.request_count)
  const successes = Number(point.success_count)
  if (Number.isFinite(requests) && requests > 0) {
    const counts = seriesCounts.get(ts) ?? { requests: 0, successes: 0 }
    counts.requests += requests
    if (Number.isFinite(successes) && successes >= 0) {
      counts.successes += successes
    }
    seriesCounts.set(ts, counts)
  }

  const inputTokens = Number(point.input_tokens)
  const cacheReadTokens = Number(point.cache_read_tokens)
  const cacheWriteTokens = Number(point.cache_write_tokens)
  const cacheHitRate = Number(point.cache_hit_rate)
  const tokens = seriesTokens.get(ts) ?? {
    inputTokens: 0,
    cacheReadTokens: 0,
    cacheWriteTokens: 0,
    cacheRates: [],
  }
  if (Number.isFinite(inputTokens) && inputTokens > 0) {
    tokens.inputTokens += inputTokens
    if (Number.isFinite(cacheReadTokens) && cacheReadTokens >= 0) {
      tokens.cacheReadTokens += cacheReadTokens
    }
    if (Number.isFinite(cacheWriteTokens) && cacheWriteTokens >= 0) {
      tokens.cacheWriteTokens += cacheWriteTokens
    }
  } else if (Number.isFinite(cacheHitRate) && cacheHitRate >= 0) {
    tokens.cacheRates.push(cacheHitRate)
  }
  seriesTokens.set(ts, tokens)
}

export function getCacheHitRate(metrics: {
  input_tokens?: number
  cache_read_tokens?: number
  cache_hit_rate?: number
}): number {
  const inputTokens = Number(metrics.input_tokens)
  const cacheReadTokens = Number(metrics.cache_read_tokens)
  if (
    Number.isFinite(inputTokens) &&
    inputTokens > 0 &&
    Number.isFinite(cacheReadTokens) &&
    cacheReadTokens >= 0
  ) {
    return (cacheReadTokens / inputTokens) * 100
  }
  const cacheHitRate = Number(metrics.cache_hit_rate)
  return Number.isFinite(cacheHitRate) && cacheHitRate >= 0
    ? cacheHitRate
    : Number.NaN
}

function weightedOrAverage(
  weightedSum: number,
  weight: number,
  fallback: number[]
): number {
  if (weight > 0) return weightedSum / weight
  return average(fallback)
}

/**
 * Merge the per-model group responses into the group-level cards used by the
 * performance workspace. Newer API responses include counts, so metrics are
 * weighted by requests; older responses still use a deterministic average.
 */
export function aggregatePerformanceGroups(
  details: ModelPerformanceDetail[]
): AggregatedPerformanceGroup[] {
  const groups = new Map<string, GroupAccumulator>()

  for (const detail of details) {
    const modelName = detail.summary.model_name
    for (const group of detail.groups) {
      const name = group.group?.trim() || 'default'
      const accumulator = groups.get(name) ?? {
        modelNames: new Set<string>(),
        requestCount: 0,
        successCount: 0,
        inputTokens: 0,
        cacheReadTokens: 0,
        cacheWriteTokens: 0,
        cacheRates: [],
        ttft: [],
        latency: [],
        tps: [],
        success: [],
        ttftWeightedSum: 0,
        ttftWeightedRequests: 0,
        latencyWeightedSum: 0,
        latencyWeightedRequests: 0,
        tpsWeightedSum: 0,
        tpsWeightedRequests: 0,
        series: new Map<number, number[]>(),
        seriesCounts: new Map<
          number,
          { requests: number; successes: number }
        >(),
        seriesTokens: new Map<
          number,
          {
            inputTokens: number
            cacheReadTokens: number
            cacheWriteTokens: number
            cacheRates: number[]
          }
        >(),
      }

      if (modelName) accumulator.modelNames.add(modelName)
      const requestCount = Number(group.request_count)
      const successCount = Number(group.success_count)
      const hasCounts =
        Number.isFinite(requestCount) &&
        requestCount > 0 &&
        Number.isFinite(successCount) &&
        successCount >= 0
      if (hasCounts) {
        accumulator.requestCount += requestCount
        accumulator.successCount += successCount
        const ttft = Number(group.avg_ttft_ms)
        const latency = Number(group.avg_latency_ms)
        const tps = Number(group.avg_tps)
        if (Number.isFinite(ttft) && ttft > 0) {
          accumulator.ttftWeightedSum += ttft * requestCount
          accumulator.ttftWeightedRequests += requestCount
        }
        if (Number.isFinite(latency) && latency > 0) {
          accumulator.latencyWeightedSum += latency * requestCount
          accumulator.latencyWeightedRequests += requestCount
        }
        if (Number.isFinite(tps) && tps > 0) {
          accumulator.tpsWeightedSum += tps * requestCount
          accumulator.tpsWeightedRequests += requestCount
        }
      }
      accumulator.ttft.push(Number(group.avg_ttft_ms))
      accumulator.latency.push(Number(group.avg_latency_ms))
      accumulator.tps.push(Number(group.avg_tps))
      accumulator.success.push(Number(group.success_rate))
      const inputTokens = Number(group.input_tokens)
      const cacheReadTokens = Number(group.cache_read_tokens)
      const cacheWriteTokens = Number(group.cache_write_tokens)
      if (Number.isFinite(inputTokens) && inputTokens > 0) {
        accumulator.inputTokens += inputTokens
        if (Number.isFinite(cacheReadTokens) && cacheReadTokens >= 0) {
          accumulator.cacheReadTokens += cacheReadTokens
        }
        if (Number.isFinite(cacheWriteTokens) && cacheWriteTokens >= 0) {
          accumulator.cacheWriteTokens += cacheWriteTokens
        }
      } else {
        const cacheHitRate = getCacheHitRate(group)
        if (Number.isFinite(cacheHitRate)) {
          accumulator.cacheRates.push(cacheHitRate)
        }
      }
      for (const point of group.series ?? []) {
        addSeriesPoint(
          accumulator.series,
          accumulator.seriesCounts,
          accumulator.seriesTokens,
          point
        )
      }
      groups.set(name, accumulator)
    }
  }

  return [...groups.entries()]
    .map(([group, accumulator]) => ({
      group,
      modelNames: [...accumulator.modelNames].sort((a, b) =>
        a.localeCompare(b)
      ),
      requestCount: accumulator.requestCount,
      successCount: accumulator.successCount,
      avgTtftMs: weightedOrAverage(
        accumulator.ttftWeightedSum,
        accumulator.ttftWeightedRequests,
        accumulator.ttft
      ),
      avgLatencyMs: weightedOrAverage(
        accumulator.latencyWeightedSum,
        accumulator.latencyWeightedRequests,
        accumulator.latency
      ),
      avgTps: weightedOrAverage(
        accumulator.tpsWeightedSum,
        accumulator.tpsWeightedRequests,
        accumulator.tps
      ),
      successRate:
        accumulator.requestCount > 0
          ? (accumulator.successCount / accumulator.requestCount) * 100
          : average(accumulator.success, true),
      inputTokens: accumulator.inputTokens,
      cacheReadTokens: accumulator.cacheReadTokens,
      cacheWriteTokens: accumulator.cacheWriteTokens,
      cacheHitRate:
        accumulator.inputTokens > 0
          ? (accumulator.cacheReadTokens / accumulator.inputTokens) * 100
          : average(accumulator.cacheRates, true),
      series: [...accumulator.series.entries()]
        .sort(([a], [b]) => a - b)
        .map(([ts, values]) => {
          const counts = accumulator.seriesCounts.get(ts)
          const tokens = accumulator.seriesTokens.get(ts)
          return {
            ts,
            successRate:
              counts && counts.requests > 0
                ? (counts.successes / counts.requests) * 100
                : average(values, true),
            inputTokens: tokens?.inputTokens ?? 0,
            cacheReadTokens: tokens?.cacheReadTokens ?? 0,
            cacheWriteTokens: tokens?.cacheWriteTokens ?? 0,
            cacheHitRate:
              tokens && tokens.inputTokens > 0
                ? (tokens.cacheReadTokens / tokens.inputTokens) * 100
                : average(tokens?.cacheRates ?? [], true),
          }
        }),
    }))
    .sort((a, b) => a.group.localeCompare(b.group))
}

/**
 * Keep configured groups visible even when the selected time window has no
 * samples for them. Metrics stay empty until the API provides observations.
 */
export function mergeConfiguredPerformanceGroups(
  groups: AggregatedPerformanceGroup[],
  configs: PerformanceGroupConfig[]
): AggregatedPerformanceGroup[] {
  const merged = new Map(groups.map((group) => [group.group, group]))

  for (const config of configs) {
    const name = config.group?.trim()
    if (!name) continue

    const existing = merged.get(name)
    if (existing) {
      merged.set(name, {
        ...existing,
        description: config.description ?? existing.description,
        ratio: config.ratio ?? existing.ratio,
      })
      continue
    }

    merged.set(name, {
      group: name,
      modelNames: [],
      description: config.description,
      ratio: config.ratio,
      requestCount: 0,
      successCount: 0,
      avgTtftMs: Number.NaN,
      avgLatencyMs: Number.NaN,
      avgTps: Number.NaN,
      successRate: Number.NaN,
      inputTokens: 0,
      cacheReadTokens: 0,
      cacheWriteTokens: 0,
      cacheHitRate: Number.NaN,
      series: [],
    })
  }

  return [...merged.values()].sort((a, b) => a.group.localeCompare(b.group))
}

/**
 * Calculate the workspace success rate from request counts when available.
 * Older responses without counts fall back to the average of finite rates.
 */
export function getOverallSuccessRate(
  groups: AggregatedPerformanceGroup[]
): number {
  const totalRequests = groups.reduce(
    (sum, group) => sum + Math.max(0, group.requestCount),
    0
  )
  if (totalRequests > 0) {
    const totalSuccesses = groups.reduce(
      (sum, group) => sum + Math.max(0, group.successCount),
      0
    )
    return (totalSuccesses / totalRequests) * 100
  }

  const values = groups
    .map((group) => group.successRate)
    .filter((value) => Number.isFinite(value))
  return values.length > 0
    ? values.reduce((sum, value) => sum + value, 0) / values.length
    : Number.NaN
}

export function getPerformanceStatus(
  successRate: number,
  hasData = true
): PerformanceStatus {
  if (!hasData || !Number.isFinite(successRate)) return 'empty'
  if (successRate >= 99) return 'healthy'
  if (successRate >= 90) return 'degraded'
  return 'critical'
}

export function getRecentStatusSeries(
  group: AggregatedPerformanceGroup,
  limit = 12
): AggregatedSeriesPoint[] {
  if (limit <= 0) return []
  return group.series.slice(-limit)
}

export function getModelGroupRows(
  details: ModelPerformanceDetail[],
  groupName: string
): Array<{ summary: PerfModelSummary; group: PerformanceGroup }> {
  const rows: Array<{ summary: PerfModelSummary; group: PerformanceGroup }> = []
  for (const detail of details) {
    const group = detail.groups.find(
      (item) => (item.group?.trim() || 'default') === groupName
    )
    if (group) rows.push({ summary: detail.summary, group })
  }
  return rows.sort((a, b) =>
    a.summary.model_name.localeCompare(b.summary.model_name)
  )
}

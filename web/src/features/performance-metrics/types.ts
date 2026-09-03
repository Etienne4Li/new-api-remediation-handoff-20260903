/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
export type PerformanceSeriesPoint = {
  ts: number
  request_count?: number
  success_count?: number
  input_tokens?: number
  cache_read_tokens?: number
  cache_write_tokens?: number
  cache_observed_requests?: number
  cache_hit_rate?: number
  total_latency_ms?: number
  ttft_sum_ms?: number
  ttft_count?: number
  output_tokens?: number
  generation_ms?: number
  avg_ttft_ms: number
  avg_latency_ms: number
  success_rate: number
  avg_tps: number
}

export type PerformanceGroup = {
  group: string
  request_count?: number
  success_count?: number
  input_tokens?: number
  cache_read_tokens?: number
  cache_write_tokens?: number
  cache_observed_requests?: number
  cache_hit_rate?: number
  total_latency_ms?: number
  ttft_sum_ms?: number
  ttft_count?: number
  output_tokens?: number
  generation_ms?: number
  avg_ttft_ms: number
  avg_latency_ms: number
  success_rate: number
  avg_tps: number
  series: PerformanceSeriesPoint[]
}

export type PerformanceMetricsData = {
  success: boolean
  message?: string
  data: {
    model_name: string
    series_schema?: string
    groups: PerformanceGroup[]
  }
}

export type PerfModelSummary = {
  model_name: string
  total_latency_ms?: number
  ttft_sum_ms?: number
  ttft_count?: number
  output_tokens?: number
  generation_ms?: number
  avg_latency_ms: number
  success_rate: number
  avg_tps: number
  recent_success_rates?: number[]
  request_count?: number
  input_tokens?: number
  cache_read_tokens?: number
  cache_write_tokens?: number
  cache_observed_requests?: number
  cache_hit_rate?: number
}

export type PerfSummaryAllData = {
  success: boolean
  message?: string
  data: {
    models: PerfModelSummary[]
  }
}

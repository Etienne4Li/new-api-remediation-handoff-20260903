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
import { render, screen, within } from '@testing-library/react'
import { beforeEach, describe, expect, test, vi } from 'vitest'

import type { PricingModel } from '../../types'
import { ModelCardGrid } from '../model-card-grid'
import { ModelDetailsPerformance } from '../model-details-performance'
import { ModelPerfBadge } from '../model-perf-badge'

const { getPerfMetricsMock, getPerfMetricsSummaryMock, useQueryMock } =
  vi.hoisted(() => ({
    getPerfMetricsMock: vi.fn(),
    getPerfMetricsSummaryMock: vi.fn(),
    useQueryMock: vi.fn(),
  }))

vi.mock('@tanstack/react-query', () => ({
  useQuery: useQueryMock,
}))

vi.mock('@/features/performance-metrics/api', () => ({
  getPerfMetrics: getPerfMetricsMock,
  getPerfMetricsSummary: getPerfMetricsSummaryMock,
  PERF_METRICS_AUTO_REFRESH_OPTIONS: {
    staleTime: 60_000,
    refetchInterval: 60_000,
    refetchIntervalInBackground: true,
  },
}))

vi.mock('../model-details-charts', () => ({
  LatencyTrendChart: () => <div data-testid='latency-chart' />,
  UptimeTrendChart: () => <div data-testid='uptime-chart' />,
}))

vi.mock('../model-details-uptime-sparkline', () => ({
  UptimeSparkline: () => <span>availability</span>,
}))

vi.mock('@/lib/lobe-icon', () => ({
  getLobeIcon: () => null,
}))

const model = { model_name: 'test-model' } as PricingModel
const performanceResponse = {
  success: true,
  data: {
    model_name: model.model_name,
    groups: [
      {
        group: 'alpha',
        avg_ttft_ms: 120,
        avg_latency_ms: 450,
        success_rate: 99.5,
        avg_tps: 42,
        input_tokens: 1_000,
        cache_read_tokens: 250,
        cache_write_tokens: 40,
        cache_observed_requests: 10,
        cache_hit_rate: 25,
        series: [
          {
            ts: 1_700_000_000,
            avg_ttft_ms: 120,
            avg_latency_ms: 450,
            success_rate: 99.5,
            avg_tps: 42,
          },
        ],
      },
      {
        group: 'beta',
        avg_ttft_ms: 180,
        avg_latency_ms: 600,
        success_rate: 100,
        avg_tps: 35,
        series: [
          {
            ts: 1_700_000_000,
            avg_ttft_ms: 180,
            avg_latency_ms: 600,
            success_rate: 100,
            avg_tps: 35,
          },
        ],
      },
    ],
  },
}

beforeEach(() => {
  useQueryMock.mockImplementation(
    (options: { queryKey: readonly unknown[] }) => ({
      isLoading: false,
      data:
        options.queryKey[0] === 'perf-metrics-summary'
          ? { success: true, data: { models: [] } }
          : performanceResponse,
    })
  )
  getPerfMetricsSummaryMock.mockResolvedValue({
    success: true,
    data: { models: [] },
  })
  getPerfMetricsMock.mockResolvedValue(performanceResponse)
})

describe('pricing performance metrics', () => {
  test('shows cache hit rate on model cards and preserves unavailable state', () => {
    const { rerender } = render(
      <ModelPerfBadge
        perf={{
          avg_latency_ms: 450,
          avg_tps: 42,
          success_rate: 99.5,
          cache_hit_rate: 25,
        }}
      />
    )

    expect(
      within(screen.getByTitle('Hit Rate: 25.0%')).getByText('25.0%')
    ).toBeVisible()

    rerender(
      <ModelPerfBadge
        perf={{
          avg_latency_ms: 450,
          avg_tps: 42,
          success_rate: 99.5,
        }}
      />
    )

    expect(
      within(screen.getByTitle('Hit Rate: Not available')).getByText('—')
    ).toBeVisible()
  })

  test('refreshes the model summary every minute in the background', () => {
    render(<ModelCardGrid models={[]} onModelClick={() => undefined} />)

    expect(useQueryMock).toHaveBeenCalledWith(
      expect.objectContaining({
        queryKey: ['perf-metrics-summary', 24],
        refetchInterval: 60_000,
        refetchIntervalInBackground: true,
      })
    )
  })

  test('shows cache hit rate per group and refreshes details every minute', () => {
    render(<ModelDetailsPerformance model={model} />)

    const alphaCell = screen.getByText('alpha')
    const alphaRow = alphaCell.closest('tr')
    const betaRow = screen.getByText('beta').closest('tr')
    expect(alphaRow).not.toBeNull()
    expect(betaRow).not.toBeNull()
    if (!alphaRow || !betaRow) {
      throw new Error('Expected performance table rows for alpha and beta')
    }
    expect(within(alphaRow).getByText('25.00%')).toBeVisible()
    expect(within(betaRow).getByText('—')).toBeVisible()
    expect(screen.getAllByText('Hit Rate')).toHaveLength(2)

    expect(useQueryMock).toHaveBeenCalledWith(
      expect.objectContaining({
        queryKey: ['perf-metrics', model.model_name],
        refetchInterval: 60_000,
        refetchIntervalInBackground: true,
      })
    )
  })
})

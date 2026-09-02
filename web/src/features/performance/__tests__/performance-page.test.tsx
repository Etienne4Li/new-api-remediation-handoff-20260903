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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { PerformancePage } from '..'

vi.mock('@/features/performance-metrics/api', () => ({
  getPerfMetricsSummary: vi.fn(async () => ({
    success: true,
    data: {
      models: [
        {
          model_name: 'alpha-model',
          avg_latency_ms: 800,
          success_rate: 99,
          avg_tps: 42,
          request_count: 100,
        },
        {
          model_name: 'beta-model',
          avg_latency_ms: 1200,
          success_rate: 96,
          avg_tps: 28,
          request_count: 50,
        },
      ],
    },
  })),
  getPerfMetrics: vi.fn(async (modelName: string) => {
    const isAlpha = modelName === 'alpha-model'
    return {
      success: true,
      data: {
        model_name: modelName,
        groups: [
          {
            group: isAlpha ? 'alpha' : 'beta',
            request_count: isAlpha ? 100 : 50,
            success_count: isAlpha ? 99 : 48,
            input_tokens: isAlpha ? 1_000 : 500,
            cache_read_tokens: isAlpha ? 600 : 100,
            cache_write_tokens: isAlpha ? 120 : 30,
            cache_observed_requests: isAlpha ? 100 : 50,
            avg_ttft_ms: isAlpha ? 180 : 260,
            avg_latency_ms: isAlpha ? 800 : 1_200,
            success_rate: isAlpha ? 99 : 96,
            avg_tps: isAlpha ? 42 : 28,
            series: [
              {
                ts: 1_785_804_400,
                request_count: isAlpha ? 100 : 50,
                success_count: isAlpha ? 99 : 48,
                avg_ttft_ms: isAlpha ? 180 : 260,
                avg_latency_ms: isAlpha ? 800 : 1_200,
                success_rate: isAlpha ? 99 : 96,
                avg_tps: isAlpha ? 42 : 28,
              },
            ],
          },
        ],
      },
    }
  }),
}))

vi.mock('@/lib/api', () => ({
  getUserGroups: vi.fn(async () => ({
    success: true,
    data: {
      alpha: { desc: 'Primary routing group', ratio: 1 },
      beta: { desc: 'Fallback routing group', ratio: 2 },
    },
  })),
}))

let queryClient: QueryClient | null = null

afterEach(() => {
  queryClient?.clear()
  queryClient = null
})

describe('PerformancePage group details', () => {
  test('opens the selected group, scopes its models, and closes accessibly', async () => {
    queryClient = new QueryClient({
      defaultOptions: {
        queries: {
          retry: false,
        },
      },
    })

    render(
      <QueryClientProvider client={queryClient}>
        <PerformancePage />
      </QueryClientProvider>
    )

    const alphaGroup = await screen.findByTestId('performance-group-alpha')
    expect(screen.getByTestId('performance-group-beta')).toBeInTheDocument()

    fireEvent.click(alphaGroup)

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('alpha-model')).toBeInTheDocument()
    expect(within(dialog).queryByText('beta-model')).not.toBeInTheDocument()

    fireEvent.click(within(dialog).getByRole('button', { name: 'Close' }))
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    })
  })
})

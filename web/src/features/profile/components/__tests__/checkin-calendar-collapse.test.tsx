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
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { CheckinCalendarCard } from '../checkin-calendar-card'

const apiMocks = vi.hoisted(() => ({
  getCheckinStatus: vi.fn(),
  performCheckin: vi.fn(),
}))

vi.mock('@/features/profile/api', () => apiMocks)

let queryClient: QueryClient | null = null

beforeEach(() => {
  apiMocks.getCheckinStatus.mockResolvedValue({
    success: true,
    data: {
      enabled: true,
      stats: {
        checked_in_today: true,
        total_checkins: 3,
        total_quota: 300,
        checkin_count: 2,
        records: [],
      },
    },
  })
})

afterEach(() => {
  queryClient?.clear()
  queryClient = null
})

describe('CheckinCalendarCard collapse state', () => {
  test('starts collapsed after check-in and preserves manual expansion', async () => {
    queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    render(
      <QueryClientProvider client={queryClient}>
        <CheckinCalendarCard
          checkinEnabled
          turnstileEnabled={false}
          turnstileSiteKey=''
        />
      </QueryClientProvider>
    )

    const header = await screen.findByRole('button', {
      name: /Daily Check-in/,
    })
    expect(header).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByText('Total check-ins')).not.toBeInTheDocument()

    await userEvent.click(header)

    expect(header).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText('Total check-ins')).toBeInTheDocument()
  })
})

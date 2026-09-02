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
import { render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { QuotaRecharge } from '..'

const getTopupInfo = vi.hoisted(() => vi.fn())

vi.mock('@/features/wallet/api', () => ({
  getTopupInfo,
}))

let queryClient: QueryClient | null = null

function renderPage() {
  queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
      },
    },
  })

  return render(
    <QueryClientProvider client={queryClient}>
      <QuotaRecharge />
    </QueryClientProvider>
  )
}

afterEach(() => {
  queryClient?.clear()
  queryClient = null
  vi.clearAllMocks()
})

describe('QuotaRecharge', () => {
  test('embeds the configured store and keeps a new-tab fallback', async () => {
    getTopupInfo.mockResolvedValue({
      success: true,
      data: { topup_link: 'https://catfk.com/shop/CJI0LOL6' },
    })

    renderPage()

    const frame = await screen.findByTitle('Quota Recharge')
    expect(frame).toHaveAttribute('src', 'https://catfk.com/shop/CJI0LOL6')
    expect(frame).toHaveAttribute('referrerpolicy', 'no-referrer')
    expect(frame).toHaveAttribute('allow', 'payment; clipboard-write')
    expect(frame).toHaveAttribute(
      'sandbox',
      'allow-forms allow-popups allow-popups-to-escape-sandbox allow-same-origin allow-scripts allow-top-navigation-by-user-activation'
    )

    const fallback = screen.getByRole('button', { name: /Open in new tab/ })
    expect(fallback).toHaveAttribute('target', '_blank')
    expect(fallback).toHaveAttribute('rel', 'noopener noreferrer')
  })

  test('does not render a frame for an unsafe configured link', async () => {
    getTopupInfo.mockResolvedValue({
      success: true,
      data: { topup_link: 'javascript:alert(1)' },
    })

    renderPage()

    await waitFor(() => {
      expect(
        screen.getByText('The configured quota recharge link is not valid.')
      ).toBeInTheDocument()
    })
    expect(screen.queryByTitle('Quota Recharge')).toBeNull()
    expect(screen.queryByRole('button', { name: /Open in new tab/ })).toBeNull()
  })
})

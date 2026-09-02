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
import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'

import type { UserWalletData } from '../../types'
import { WalletStatsCard } from '../wallet-stats-card'

const user: UserWalletData = {
  id: 1,
  username: 'wallet-user',
  quota: 123_456_789,
  used_quota: 98_765_432,
  request_count: Number.MAX_SAFE_INTEGER,
  aff_quota: 0,
  aff_history_quota: 0,
  aff_count: 0,
  group: 'default',
}

describe('WalletStatsCard', () => {
  test('keeps every wallet metric visible for long values', () => {
    render(<WalletStatsCard user={user} />)

    expect(screen.getByLabelText('Wallet')).toBeInTheDocument()
    expect(screen.getByText('Current Balance')).toBeInTheDocument()
    expect(screen.getByText('Total Usage')).toBeInTheDocument()
    expect(screen.getByText('API Requests')).toBeInTheDocument()
    expect(
      screen.getByText(Number.MAX_SAFE_INTEGER.toLocaleString())
    ).toBeInTheDocument()
  })

  test('announces a stable loading region', () => {
    render(<WalletStatsCard user={null} loading />)

    expect(screen.getByLabelText('Wallet')).toHaveAttribute('aria-busy', 'true')
  })
})

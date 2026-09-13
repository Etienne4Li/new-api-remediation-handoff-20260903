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
import type { AnchorHTMLAttributes, ReactNode } from 'react'
import { describe, expect, test, vi } from 'vitest'

import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'

import type { UserWalletData } from '../../types'
import { AffiliateRewardsCard } from '../affiliate-rewards-card'

vi.mock('@tanstack/react-router', () => ({
  Link: ({
    to,
    children,
    ...props
  }: AnchorHTMLAttributes<HTMLAnchorElement> & {
    to: string
    children?: ReactNode
  }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

const user: UserWalletData = {
  id: 1,
  username: 'wallet-user',
  quota: 1_000_000,
  used_quota: 0,
  request_count: 0,
  aff_quota: 250_000,
  aff_history_quota: 750_000,
  aff_count: 3,
  group: 'default',
}

describe('AffiliateRewardsCard', () => {
  // The shared Button renders its anchor with an explicit role="button", so
  // the entry is reached by that role rather than by "link".
  test('points at the referral page', () => {
    render(<AffiliateRewardsCard user={user} />)

    const entry = screen.getByRole('button', { name: /View referral rewards/ })
    expect(entry).toHaveAttribute('href', '/affiliate')
  })

  test('shows the transferable balance next to the title', () => {
    render(<AffiliateRewardsCard user={user} />)

    expect(screen.getByText('Referral Program')).toBeInTheDocument()
    expect(screen.getByText(/Available to transfer/)).toBeInTheDocument()
    // 250_000 quota at the default 500_000-per-unit rate.
    expect(screen.getByText('$0.5')).toBeInTheDocument()
  })

  // Everything below now lives on /affiliate. Leaving any of it here would
  // mean two places to keep in sync — and a referral link the wallet page can
  // no longer refresh, because it stopped fetching the affiliate code.
  test('no longer carries the link input, the ledger or the transfer action', () => {
    render(<AffiliateRewardsCard user={user} />)

    expect(
      screen.queryByLabelText('Copy referral link')
    ).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Rebate Details')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('Next page')).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Transfer to Balance' })
    ).not.toBeInTheDocument()
    expect(screen.queryByText(/rebate on each of their first/)).toBeNull()
  })

  test('holds the number back until the user data has arrived', () => {
    render(<AffiliateRewardsCard user={null} loading />)

    expect(screen.getByText('—')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: /View referral rewards/ })
    ).not.toBeInTheDocument()
  })

  test('falls back to zero when the user has never earned a rebate', () => {
    render(<AffiliateRewardsCard user={{ ...user, aff_quota: 0 }} />)

    expect(screen.getByText('$0')).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /View referral rewards/ })
    ).toBeInTheDocument()
  })
})

describe('AffiliateRewardsCard translations', () => {
  test('the entry-bar action is translated in both hand-written locales', () => {
    expect(en.translation['View referral rewards']).toBe(
      'View referral rewards'
    )
    expect(zh.translation['View referral rewards']).toBe('查看邀请返利')
  })

  test('keeps the balance label the rebate rules introduced', () => {
    expect(en.translation['Available to transfer']).toBe('Available')
    expect(zh.translation['Available to transfer']).toBe('可转移')
  })
})

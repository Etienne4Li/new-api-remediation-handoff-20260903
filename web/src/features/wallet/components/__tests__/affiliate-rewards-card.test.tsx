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
import { render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, test, vi } from 'vitest'

import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'

import type {
  AffRebateListResponse,
  ApiResponse,
  UserWalletData,
} from '../../types'
import { AffiliateRewardsCard } from '../affiliate-rewards-card'

const getAffRebates =
  vi.fn<
    (
      page: number,
      pageSize: number
    ) => Promise<ApiResponse<AffRebateListResponse>>
  >()

vi.mock('../../api', () => ({
  getAffRebates: (page: number, pageSize: number) =>
    getAffRebates(page, pageSize),
  isApiSuccess: (response: ApiResponse) =>
    response.success === true || response.message === 'success',
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

function respond(
  data: Partial<AffRebateListResponse>
): ApiResponse<AffRebateListResponse> {
  return {
    success: true,
    data: {
      enabled: false,
      percent: 0,
      max_times: 0,
      page: 1,
      page_size: 5,
      total: 0,
      items: [],
      ...data,
    },
  }
}

function renderCard() {
  return render(
    <AffiliateRewardsCard
      user={user}
      affiliateLink='https://example.com/register?aff=abcd'
      onTransfer={() => undefined}
    />
  )
}

describe('AffiliateRewardsCard', () => {
  beforeEach(() => {
    getAffRebates.mockResolvedValue(respond({}))
  })

  // Rebates are credited immediately, so "Pending" would tell users their money
  // is stuck somewhere. The translated label is asserted separately below.
  test('labels the affiliate balance as available, never as pending', async () => {
    renderCard()

    await waitFor(() => expect(getAffRebates).toHaveBeenCalled())
    expect(screen.getByText('Available to transfer')).toBeInTheDocument()
    expect(screen.queryByText('Pending')).not.toBeInTheDocument()
    expect(screen.getByText('Total Earned')).toBeInTheDocument()
    expect(screen.getByText('Invites')).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Transfer to Balance' })
    ).toBeInTheDocument()
  })

  test('hides the rebate rule and details while the feature is disabled', async () => {
    renderCard()

    await waitFor(() => expect(getAffRebates).toHaveBeenCalled())
    expect(screen.queryByLabelText('Rebate Details')).not.toBeInTheDocument()
    expect(screen.queryByText(/rebate on each of their first/)).toBeNull()
    // The original generic description stays in place when disabled.
    expect(
      screen.getByText(
        'Earn rewards when users join through your referral link. Transfer accumulated rewards to your balance anytime.'
      )
    ).toBeInTheDocument()
  })

  test('states the rule using the percentage and count the backend reports', async () => {
    getAffRebates.mockResolvedValue(
      respond({ enabled: true, percent: 7, max_times: 2 })
    )
    renderCard()

    expect(
      await screen.findByText(
        'Friends who sign up with your link earn you a 7% rebate on each of their first 2 top-ups, transferable to your balance anytime.'
      )
    ).toBeInTheDocument()
  })

  test('lists each rebate with the masked invitee, sequence and amounts', async () => {
    getAffRebates.mockResolvedValue(
      respond({
        enabled: true,
        percent: 5,
        max_times: 3,
        total: 2,
        items: [
          {
            id: 2,
            invitee: 'u***@ex***.com',
            topup_money: 20,
            rebate_quota: 500_000,
            sequence: 2,
            created_time: 1_757_000_000,
          },
          {
            id: 1,
            invitee: '用户 #123',
            topup_money: 10,
            rebate_quota: 250_000,
            sequence: 1,
            created_time: 1_756_900_000,
          },
        ],
      })
    )
    renderCard()

    expect(await screen.findByLabelText('Rebate Details')).toBeInTheDocument()
    expect(screen.getByText('u***@ex***.com')).toBeInTheDocument()
    expect(screen.getByText('用户 #123')).toBeInTheDocument()
    expect(screen.getByText(/Top-up 2\/3/)).toBeInTheDocument()
    expect(screen.getByText(/Top-up 1\/3/)).toBeInTheDocument()
    expect(screen.getByText(/Paid 20/)).toBeInTheDocument()
    expect(screen.getByText(/Paid 10/)).toBeInTheDocument()
    // Two rows, two rebate amounts, nothing collapsed away.
    expect(screen.getAllByText(/^\+/)).toHaveLength(2)
  })

  test('shows an empty state once the feature is on but nothing is earned', async () => {
    getAffRebates.mockResolvedValue(
      respond({ enabled: true, percent: 5, max_times: 3, total: 0, items: [] })
    )
    renderCard()

    expect(
      await screen.findByText(
        'No rebates yet. Share your referral link to start earning.'
      )
    ).toBeInTheDocument()
  })

  test('paginates only when there is more than one page of rebates', async () => {
    getAffRebates.mockResolvedValue(
      respond({
        enabled: true,
        percent: 5,
        max_times: 3,
        total: 12,
        page_size: 5,
        items: [
          {
            id: 1,
            invitee: 'u***@ex***.com',
            topup_money: 10,
            rebate_quota: 250_000,
            sequence: 1,
            created_time: 1_756_900_000,
          },
        ],
      })
    )
    renderCard()

    expect(await screen.findByLabelText('Next page')).toBeEnabled()
    expect(screen.getByLabelText('Previous page')).toBeDisabled()
  })
})

// The component renders i18n keys; these assertions pin the text users actually
// read, including the renamed balance label required by the rebate rules.
describe('AffiliateRewardsCard translations', () => {
  const rule =
    'Friends who sign up with your link earn you a {{percent}}% rebate on each of their first {{maxTimes}} top-ups, transferable to your balance anytime.'

  test('renames the balance label in both shipped languages', () => {
    expect(en.translation['Available to transfer']).toBe('Available')
    expect(zh.translation['Available to transfer']).toBe('可转移')
  })

  test('carries the rebate rule and detail labels', () => {
    expect(en.translation[rule]).toBe(rule)
    expect(zh.translation[rule]).toBe(
      '好友通过你的链接注册后，前 {{maxTimes}} 次充值你各得 {{percent}}% 返利，可随时转入余额使用。'
    )
    const detailKeys = [
      'Rebate Details',
      'Paid',
      'Top-up {{sequence}}/{{maxTimes}}',
      'No rebates yet. Share your referral link to start earning.',
    ] as const
    for (const key of detailKeys) {
      expect(en.translation[key]).toBeTruthy()
      expect(zh.translation[key]).toBeTruthy()
    }
  })
})

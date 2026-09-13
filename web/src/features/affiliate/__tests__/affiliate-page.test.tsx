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
import { render, renderHook, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, test, vi } from 'vitest'

import { checkIsActive } from '@/components/layout/lib/url-utils'
import { useSidebarData } from '@/hooks/use-sidebar-data'
import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'

import type {
  AffRebateListResponse,
  AffRebateRecord,
  ApiResponse,
  UserWalletData,
} from '../../wallet/types'
import { Affiliate } from '../index'

// The `@/components/layout` barrel drags in the app shell (and, through it, a
// zod import cycle that breaks collection in this environment). The page only
// needs the layout primitive, so pull it straight from its own module.
vi.mock('@/components/layout', async () => ({
  SectionPageLayout: (
    await vi.importActual<
      typeof import('@/components/layout/components/section-page-layout')
    >('@/components/layout/components/section-page-layout')
  ).SectionPageLayout,
}))

const getAffRebates =
  vi.fn<
    (
      page: number,
      pageSize: number
    ) => Promise<ApiResponse<AffRebateListResponse>>
  >()
const getAffiliateCode = vi.fn()
const getTopupInfo = vi.fn()
const transferAffiliateQuota = vi.fn()

// Partial mock: the wallet hook barrel also pulls in the payment hooks, which
// bind the rest of this module at import time.
vi.mock('@/features/wallet/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/wallet/api')>()),
  getAffRebates: (page: number, pageSize: number) =>
    getAffRebates(page, pageSize),
  getAffiliateCode: () => getAffiliateCode(),
  getTopupInfo: () => getTopupInfo(),
  transferAffiliateQuota: (payload: { quota: number }) =>
    transferAffiliateQuota(payload),
}))

const getSelf = vi.fn()

vi.mock('@/lib/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/lib/api')>()),
  getSelf: () => getSelf(),
}))

const USER: UserWalletData = {
  id: 1,
  username: 'inviter',
  quota: 1_000_000,
  used_quota: 0,
  request_count: 0,
  aff_quota: 250_000,
  aff_history_quota: 750_000,
  aff_count: 3,
  group: 'default',
}

function rebate(overrides: Partial<AffRebateRecord> = {}): AffRebateRecord {
  return {
    id: 1,
    invitee: 'u***@ex***.com',
    topup_money: 10,
    rebate_quota: 250_000,
    sequence: 1,
    created_time: 1_756_900_000,
    ...overrides,
  }
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
      page_size: 10,
      total: 0,
      items: [],
      ...data,
    },
  }
}

const ENABLED = { enabled: true, percent: 5, max_times: 3 }

beforeEach(() => {
  getAffRebates.mockResolvedValue(respond({}))
  getAffiliateCode.mockResolvedValue({ success: true, data: 'abcd' })
  getTopupInfo.mockResolvedValue({
    success: true,
    data: { payment_compliance_confirmed: true },
  })
  getSelf.mockResolvedValue({ success: true, data: USER })
  transferAffiliateQuota.mockResolvedValue({ success: true, message: 'ok' })
})

describe('Affiliate page', () => {
  test('always shows the referral link and the three totals', async () => {
    render(<Affiliate />)

    const link =
      await screen.findByLabelText<HTMLInputElement>('Your referral link')
    // Same algorithm as the wallet page: origin + /sign-up?aff=<code>.
    expect(link.value).toBe(`${window.location.origin}/sign-up?aff=abcd`)
    expect(link).toHaveAttribute('readonly')

    expect(screen.getByText('Available to transfer')).toBeInTheDocument()
    expect(screen.getByText('Total Earned')).toBeInTheDocument()
    expect(screen.getByText('Invites')).toBeInTheDocument()
    expect(screen.getByText('3')).toBeInTheDocument()
  })

  test('renders the rules and the ledger while the rebate feature is on', async () => {
    getAffRebates.mockResolvedValue(
      respond({ ...ENABLED, total: 1, items: [rebate()] })
    )
    render(<Affiliate />)

    expect(await screen.findByLabelText('Rebate Details')).toBeInTheDocument()
    // Every rule is spelled out, with the live percentage and count.
    expect(
      screen.getByText('Your friend signs up through your referral link.')
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        'Each of their first 3 online top-ups earns you 5% of the amount they actually paid.'
      )
    ).toBeInTheDocument()
    expect(
      screen.getByText('Rebates are credited instantly, with no cap.')
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        'Rebates can only be transferred to your site balance; they cannot be withdrawn.'
      )
    ).toBeInTheDocument()
    expect(
      screen.getByText(
        'Top-ups paid with a redemption code do not earn rebates.'
      )
    ).toBeInTheDocument()
    expect(screen.getByText('u***@ex***.com')).toBeInTheDocument()
    expect(screen.getByText(/Top-up 1\/3/)).toBeInTheDocument()
  })

  test('drops the rules and the ledger when the rebate feature is off, keeping link, totals and transfer', async () => {
    getAffRebates.mockResolvedValue(respond({ enabled: false }))
    render(<Affiliate />)

    await waitFor(() => expect(getAffRebates).toHaveBeenCalled())
    expect(screen.queryByLabelText('Rebate Details')).not.toBeInTheDocument()
    expect(
      screen.queryByText('Your friend signs up through your referral link.')
    ).not.toBeInTheDocument()
    expect(
      screen.getByText('The referral rebate program is not open at the moment.')
    ).toBeInTheDocument()

    // The pre-rebate features are unaffected by the switch.
    expect(screen.getByLabelText('Your referral link')).toBeInTheDocument()
    expect(screen.getByText('Available to transfer')).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Transfer to Balance' })
    ).toBeInTheDocument()
  })

  test('shows the empty state once the feature is on but nothing is earned', async () => {
    getAffRebates.mockResolvedValue(
      respond({ ...ENABLED, total: 0, items: [] })
    )
    render(<Affiliate />)

    expect(
      await screen.findByText(
        'No rebates yet. Share your referral link to start earning.'
      )
    ).toBeInTheDocument()
  })

  test('pages the ledger ten rows at a time and refetches on the next page', async () => {
    getAffRebates.mockResolvedValue(
      respond({ ...ENABLED, total: 24, items: [rebate()] })
    )
    render(<Affiliate />)

    const next = await screen.findByLabelText('Next page')
    // 24 rebates at the page size this page asks for -> 3 pages.
    expect(getAffRebates).toHaveBeenCalledWith(1, 10)
    expect(screen.getByText('1 / 3')).toBeInTheDocument()
    expect(screen.getByLabelText('Previous page')).toBeDisabled()

    await userEvent.click(next)

    await waitFor(() => expect(getAffRebates).toHaveBeenCalledWith(2, 10))
    expect(await screen.findByText('2 / 3')).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.getByLabelText('Previous page')).toBeEnabled()
    )

    await userEvent.click(screen.getByLabelText('Previous page'))
    await waitFor(() => expect(getAffRebates).toHaveBeenLastCalledWith(1, 10))
  })

  test('hides the pager when a single page holds every rebate', async () => {
    getAffRebates.mockResolvedValue(
      respond({ ...ENABLED, total: 4, items: [rebate()] })
    )
    render(<Affiliate />)

    expect(await screen.findByLabelText('Rebate Details')).toBeInTheDocument()
    expect(screen.queryByLabelText('Next page')).not.toBeInTheDocument()
  })

  test('transferring to balance refreshes the totals and the ledger', async () => {
    // The dialog's minimum is one whole unit (500_000 quota), so the fixture
    // needs at least that much for the confirm button to be reachable.
    const rich = { ...USER, aff_quota: 1_000_000 }
    getSelf.mockResolvedValue({ success: true, data: rich })
    getAffRebates.mockResolvedValue(
      respond({ ...ENABLED, total: 0, items: [] })
    )
    render(<Affiliate />)

    const transfer = await screen.findByRole('button', {
      name: 'Transfer to Balance',
    })
    const selfCallsBefore = getSelf.mock.calls.length
    const rebateCallsBefore = getAffRebates.mock.calls.length

    await userEvent.click(transfer)
    // The dialog opens pre-filled with the minimum transferable amount.
    const confirm = await screen.findByRole('button', { name: 'Transfer' })

    getSelf.mockResolvedValue({
      success: true,
      data: { ...rich, aff_quota: 0, quota: rich.quota + rich.aff_quota },
    })
    await userEvent.click(confirm)

    await waitFor(() => expect(transferAffiliateQuota).toHaveBeenCalled())
    await waitFor(() =>
      expect(getSelf.mock.calls.length).toBeGreaterThan(selfCallsBefore)
    )
    await waitFor(() =>
      expect(getAffRebates.mock.calls.length).toBeGreaterThan(rebateCallsBefore)
    )
    // Balance drained, so the transfer action retires with it.
    await waitFor(() =>
      expect(
        screen.queryByRole('button', { name: 'Transfer to Balance' })
      ).not.toBeInTheDocument()
    )
  })

  test('disables transferring until the administrator confirms compliance', async () => {
    getTopupInfo.mockResolvedValue({
      success: true,
      data: { payment_compliance_confirmed: false },
    })
    render(<Affiliate />)

    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Transfer to Balance' })
      ).toBeDisabled()
    )
    expect(
      screen.getByText(
        'Referral reward transfer is disabled until the administrator confirms compliance terms.'
      )
    ).toBeInTheDocument()
  })

  test('hides the transfer action when there is nothing to transfer', async () => {
    getSelf.mockResolvedValue({
      success: true,
      data: { ...USER, aff_quota: 0 },
    })
    render(<Affiliate />)

    await waitFor(() => expect(getSelf).toHaveBeenCalled())
    expect(
      screen.queryByRole('button', { name: 'Transfer to Balance' })
    ).not.toBeInTheDocument()
  })
})

describe('Referral sidebar entry', () => {
  function personalItems() {
    const { result } = renderHook(() => useSidebarData())
    return (
      result.current.navGroups.find((group) => group.id === 'personal')
        ?.items ?? []
    )
  }

  test('sits in the Personal group directly after Wallet', () => {
    const items = personalItems()
    const walletIndex = items.findIndex((item) => item.title === 'Wallet')

    expect(walletIndex).toBeGreaterThanOrEqual(0)
    expect(items[walletIndex + 1]).toMatchObject({
      title: 'Referral Program',
      url: '/affiliate',
    })
  })

  test('highlights only itself on /affiliate', () => {
    const items = personalItems()
    const active = items.filter((item) => checkIsActive('/affiliate', item))

    expect(active.map((item) => item.title)).toEqual(['Referral Program'])
  })
})

// The components render i18n keys; these pin the text users actually read.
describe('Affiliate page translations', () => {
  const NEW_KEYS = [
    'View referral rewards',
    'Your referral link',
    'Share this link to invite friends. Anyone who signs up through it is linked to your account.',
    'Referral Rewards',
    'How it works',
    'Your friend signs up through your referral link.',
    'Each of their first {{maxTimes}} online top-ups earns you {{percent}}% of the amount they actually paid.',
    'Rebates are credited instantly, with no cap.',
    'Rebates can only be transferred to your site balance; they cannot be withdrawn.',
    'Top-ups paid with a redemption code do not earn rebates.',
    'The referral rebate program is not open at the moment.',
  ] as const

  test('every new key is present in both hand-translated locales', () => {
    for (const key of NEW_KEYS) {
      expect(en.translation[key]).toBeTruthy()
      expect(zh.translation[key]).toBeTruthy()
    }
  })

  test('the Simplified Chinese wording uses the 邀请返利 naming', () => {
    expect(zh.translation['View referral rewards']).toBe('查看邀请返利')
    expect(zh.translation['Referral Rewards']).toBe('邀请返利')
    expect(
      zh.translation[
        'Each of their first {{maxTimes}} online top-ups earns you {{percent}}% of the amount they actually paid.'
      ]
    ).toBe(
      '其前 {{maxTimes}} 笔在线充值，每笔按实付金额的 {{percent}}% 返给你。'
    )
  })
})

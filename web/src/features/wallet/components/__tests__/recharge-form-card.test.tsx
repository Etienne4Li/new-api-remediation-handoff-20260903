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
import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, test, vi } from 'vitest'

import type { TopupInfo } from '../../types'
import { RechargeFormCard } from '../recharge-form-card'

const topupInfo: TopupInfo = {
  enable_online_topup: true,
  enable_stripe_topup: false,
  pay_methods: [
    {
      name: 'Card Pay',
      type: 'card',
      min_topup: 20,
    },
  ],
  min_topup: 10,
  stripe_min_topup: 0,
  amount_options: [10, 20],
  discount: {},
  enable_redemption: true,
}

function renderCard(overrides: Record<string, unknown> = {}) {
  const props = {
    topupInfo,
    presetAmounts: [{ value: 10 }, { value: 20 }],
    selectedPreset: 10,
    onSelectPreset: vi.fn(),
    topupAmount: 10,
    onTopupAmountChange: vi.fn(),
    paymentAmount: 10,
    calculating: false,
    onPaymentMethodSelect: vi.fn(),
    paymentLoading: null,
    redemptionCode: '',
    onRedemptionCodeChange: vi.fn(),
    onRedeem: vi.fn(),
    redeeming: false,
    onOpenBilling: vi.fn(),
    ...overrides,
  }

  render(<RechargeFormCard {...props} />)
  return props
}

describe('RechargeFormCard', () => {
  test('exposes preset selection and invokes the existing callback', async () => {
    const user = userEvent.setup()
    const props = renderCard()
    const presetButtons = screen
      .getAllByRole('button')
      .filter((button) => button.hasAttribute('aria-pressed'))

    expect(presetButtons).toHaveLength(2)
    expect(presetButtons[0]).toHaveAttribute('aria-pressed', 'true')
    expect(presetButtons[1]).toHaveAttribute('aria-pressed', 'false')

    await user.click(presetButtons[1])
    expect(props.onSelectPreset).toHaveBeenCalledWith({ value: 20 })
  })

  test('keeps an under-minimum payment method disabled with its reason', () => {
    renderCard()

    const paymentButton = screen.getByRole('button', { name: /Card Pay/ })
    expect(paymentButton).toBeDisabled()
    expect(paymentButton).toHaveAttribute('title', 'Minimum topup amount: 20')
  })

  test('opens the existing order history action', async () => {
    const user = userEvent.setup()
    const props = renderCard()

    await user.click(screen.getByRole('button', { name: 'Order History' }))
    expect(props.onOpenBilling).toHaveBeenCalledTimes(1)
  })
})

describe('RechargeFormCard top-up bonus', () => {
  const bonusInfo: TopupInfo = {
    ...topupInfo,
    amount_options: [50, 100, 500],
    topup_bonus_enabled: true,
    topup_bonus: { 100: 0.01, 200: 0.015, 500: 0.02 },
  }
  const bonusPresets = [{ value: 50 }, { value: 100 }, { value: 500 }]

  test('marks the qualifying presets and leaves the rest untouched', () => {
    renderCard({
      topupInfo: bonusInfo,
      presetAmounts: bonusPresets,
      selectedPreset: 100,
      topupAmount: 100,
    })

    // 100 at 1% and 500 at 2%; 50 clears no threshold.
    expect(screen.getByText('Extra 1 balance')).toBeInTheDocument()
    expect(screen.getByText('Extra 10 balance')).toBeInTheDocument()
    expect(screen.queryByText('Extra 0 balance')).not.toBeInTheDocument()
    expect(screen.queryByText('Extra 0.5 balance')).not.toBeInTheDocument()
  })

  test('renders the ladder panel with every configured tier', () => {
    renderCard({
      topupInfo: bonusInfo,
      presetAmounts: bonusPresets,
      topupAmount: 100,
    })

    expect(screen.getByText('Recharge Bonus')).toBeInTheDocument()
    expect(
      screen.getByText('Automatically credited when your top-up qualifies')
    ).toBeInTheDocument()
    expect(screen.getByText('100+ gets 1% extra')).toBeInTheDocument()
    expect(screen.getByText('200+ gets 1.5% extra')).toBeInTheDocument()
    expect(screen.getByText('500+ gets 2% extra')).toBeInTheDocument()
    expect(
      screen.getByText('Credited straight to your balance, never expires')
    ).toBeInTheDocument()
  })

  test('estimates the bonus for a custom amount and says so when it misses', () => {
    renderCard({
      topupInfo: bonusInfo,
      presetAmounts: bonusPresets,
      topupAmount: 300,
    })
    // 300 sits in the 200 tier: 1.5% of 300.
    expect(screen.getByText('Estimated extra balance: 4.5')).toBeInTheDocument()
    expect(
      screen.queryByText('Not eligible for a bonus')
    ).not.toBeInTheDocument()

    cleanup()

    renderCard({
      topupInfo: bonusInfo,
      presetAmounts: bonusPresets,
      topupAmount: 50,
    })
    expect(screen.getByText('Not eligible for a bonus')).toBeInTheDocument()
    expect(
      screen.queryByText(/Estimated extra balance/)
    ).not.toBeInTheDocument()
  })

  test('renders none of the promotion while the switch is off', () => {
    renderCard({
      topupInfo: { ...bonusInfo, topup_bonus_enabled: false },
      presetAmounts: bonusPresets,
      topupAmount: 500,
    })

    expect(screen.queryByText('Recharge Bonus')).not.toBeInTheDocument()
    expect(screen.queryByText(/Extra .* balance/)).not.toBeInTheDocument()
    expect(
      screen.queryByText(/Estimated extra balance/)
    ).not.toBeInTheDocument()
    expect(
      screen.queryByText('Not eligible for a bonus')
    ).not.toBeInTheDocument()
    expect(
      screen.queryByText('Credited straight to your balance, never expires')
    ).not.toBeInTheDocument()
    // The rest of the card is untouched.
    expect(screen.getByText('Add Funds')).toBeInTheDocument()
  })

  test('renders none of the promotion when no tier is configured', () => {
    renderCard({
      topupInfo: { ...bonusInfo, topup_bonus: {} },
      presetAmounts: bonusPresets,
      topupAmount: 500,
    })

    expect(screen.queryByText('Recharge Bonus')).not.toBeInTheDocument()
    expect(screen.queryByText(/Extra .* balance/)).not.toBeInTheDocument()
    expect(
      screen.queryByText(/Estimated extra balance/)
    ).not.toBeInTheDocument()
    expect(screen.getByText('Add Funds')).toBeInTheDocument()
  })
})

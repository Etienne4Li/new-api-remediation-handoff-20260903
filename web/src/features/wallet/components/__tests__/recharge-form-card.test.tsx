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

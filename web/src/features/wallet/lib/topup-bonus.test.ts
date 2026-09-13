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
import { describe, expect, test } from 'vitest'

import type { TopupInfo } from '../types'
import {
  formatTopupBonusPercent,
  getTopupBonusAmount,
  getTopupBonusRatio,
  getTopupBonusTiers,
} from './topup-bonus'

const ladder = { 100: 0.01, 200: 0.015, 500: 0.02 }

function infoWith(overrides: Partial<TopupInfo>): TopupInfo {
  return {
    enable_online_topup: true,
    enable_stripe_topup: false,
    pay_methods: [],
    min_topup: 1,
    stripe_min_topup: 0,
    amount_options: [],
    discount: {},
    ...overrides,
  }
}

describe('getTopupBonusTiers', () => {
  test('sorts the configured ladder by threshold', () => {
    const tiers = getTopupBonusTiers(
      infoWith({
        topup_bonus_enabled: true,
        topup_bonus: { 500: 0.02, 100: 0.01, 200: 0.015 },
      })
    )

    expect(tiers).toEqual([
      { threshold: 100, ratio: 0.01 },
      { threshold: 200, ratio: 0.015 },
      { threshold: 500, ratio: 0.02 },
    ])
  })

  test('yields nothing while the promotion is switched off', () => {
    expect(
      getTopupBonusTiers(
        infoWith({ topup_bonus_enabled: false, topup_bonus: ladder })
      )
    ).toEqual([])
  })

  test('yields nothing when the ladder is empty or absent', () => {
    expect(
      getTopupBonusTiers(
        infoWith({ topup_bonus_enabled: true, topup_bonus: {} })
      )
    ).toEqual([])
    expect(getTopupBonusTiers(infoWith({ topup_bonus_enabled: true }))).toEqual(
      []
    )
    expect(getTopupBonusTiers(null)).toEqual([])
  })

  test('drops entries that could never be granted', () => {
    const tiers = getTopupBonusTiers(
      infoWith({
        topup_bonus_enabled: true,
        topup_bonus: { 0: 0.5, [-100]: 0.5, 100: 0, 200: 0.015 },
      })
    )

    expect(tiers).toEqual([{ threshold: 200, ratio: 0.015 }])
  })
})

describe('getTopupBonusRatio', () => {
  const tiers = getTopupBonusTiers(
    infoWith({ topup_bonus_enabled: true, topup_bonus: ladder })
  )

  // The same ladder the backend test pins, so the card cannot promise a bonus
  // the settlement path would not grant.
  test.each([
    [99, 0],
    [100, 0.01],
    [199, 0.01],
    [200, 0.015],
    [300, 0.015],
    [500, 0.02],
    [1000, 0.02],
  ])('an amount of %i takes the %f tier', (amount, ratio) => {
    expect(getTopupBonusRatio(tiers, amount)).toBe(ratio)
  })

  test('refuses non-positive and non-finite amounts', () => {
    expect(getTopupBonusRatio(tiers, 0)).toBe(0)
    expect(getTopupBonusRatio(tiers, -100)).toBe(0)
    expect(getTopupBonusRatio(tiers, Number.NaN)).toBe(0)
  })
})

describe('getTopupBonusAmount', () => {
  const tiers = getTopupBonusTiers(
    infoWith({ topup_bonus_enabled: true, topup_bonus: ladder })
  )

  test.each([
    [99, 0],
    [100, 1],
    [200, 3],
    [300, 4.5],
    [500, 10],
    [1000, 20],
    [2000, 40],
  ])('an amount of %i earns %f extra', (amount, bonus) => {
    expect(getTopupBonusAmount(tiers, amount)).toBeCloseTo(bonus, 6)
  })

  test('scales inside a tier rather than paying a flat amount', () => {
    expect(getTopupBonusAmount(tiers, 300)).toBeGreaterThan(
      getTopupBonusAmount(tiers, 200)
    )
  })
})

describe('formatTopupBonusPercent', () => {
  test('trims trailing zeros without losing a half percent', () => {
    expect(formatTopupBonusPercent(0.01)).toBe('1')
    expect(formatTopupBonusPercent(0.015)).toBe('1.5')
    expect(formatTopupBonusPercent(0.02)).toBe('2')
  })
})

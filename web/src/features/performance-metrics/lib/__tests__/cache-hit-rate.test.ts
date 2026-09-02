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

import { getCombinedCacheHitRate, getObservedCacheHitRate } from '../format'

describe('cache hit rate formatting data', () => {
  test('keeps a measured zero distinct from unavailable data', () => {
    expect(
      getObservedCacheHitRate({
        cache_observed_requests: 12,
        cache_hit_rate: 0,
      })
    ).toBe(0)
    expect(getObservedCacheHitRate({})).toBe(Number.NaN)
  })

  test('derives and combines rates from observed token totals', () => {
    expect(
      getObservedCacheHitRate({
        input_tokens: 1_000,
        cache_read_tokens: 250,
      })
    ).toBe(25)

    expect(
      getCombinedCacheHitRate([
        { input_tokens: 1_000, cache_read_tokens: 250 },
        {
          input_tokens: 500,
          cache_observed_requests: 5,
          cache_hit_rate: 0,
        },
        {},
      ])
    ).toBeCloseTo(16.67, 2)
  })

  test('falls back to explicit rates when token totals are unavailable', () => {
    expect(
      getCombinedCacheHitRate([
        { cache_hit_rate: 10 },
        { cache_hit_rate: 30 },
        {},
      ])
    ).toBe(20)
    expect(getCombinedCacheHitRate([{}])).toBe(Number.NaN)
  })
})

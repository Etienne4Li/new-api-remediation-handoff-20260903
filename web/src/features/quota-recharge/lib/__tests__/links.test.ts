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

import { resolveQuotaRechargeLink } from '../links'

describe('resolveQuotaRechargeLink', () => {
  test('trims and returns an HTTPS store link', () => {
    expect(resolveQuotaRechargeLink('  https://catfk.com/shop/demo  ')).toBe(
      'https://catfk.com/shop/demo'
    )
  })

  test('rejects non-web schemes before they reach the iframe', () => {
    expect(resolveQuotaRechargeLink('javascript:alert(1)')).toBeNull()
    expect(resolveQuotaRechargeLink('data:text/html,<h1>store</h1>')).toBeNull()
    expect(resolveQuotaRechargeLink('http://catfk.com/shop/demo')).toBeNull()
  })

  test('rejects absent and malformed values', () => {
    expect(resolveQuotaRechargeLink(undefined)).toBeNull()
    expect(resolveQuotaRechargeLink('')).toBeNull()
    expect(resolveQuotaRechargeLink('not a URL')).toBeNull()
  })
})

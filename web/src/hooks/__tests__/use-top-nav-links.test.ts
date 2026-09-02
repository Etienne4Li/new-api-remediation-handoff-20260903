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

import { buildTopNavLinks } from '../use-top-nav-links'

describe('buildTopNavLinks', () => {
  test('keeps safe defaults when status is unavailable', () => {
    expect(buildTopNavLinks(null, false).map((link) => link.title)).toEqual([
      'Home',
      'Manage',
      'Models',
      'Hot Topics',
      'About',
    ])
  })

  test('honors an explicit configuration that disables every module', () => {
    const status = {
      HeaderNavModules: JSON.stringify({
        home: false,
        console: false,
        pricing: false,
        rankings: false,
        docs: false,
        about: false,
      }),
    }

    expect(buildTopNavLinks(status, true)).toEqual([])
  })

  test('does not add a docs link without a configured URL', () => {
    const links = buildTopNavLinks({ HeaderNavModules: '{"docs":true}' }, false)
    expect(links.some((link) => link.title === 'Docs')).toBe(false)
  })
})

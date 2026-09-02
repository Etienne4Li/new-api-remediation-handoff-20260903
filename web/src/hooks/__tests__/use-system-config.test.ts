/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { describe, expect, test } from 'vitest'

import { DEFAULT_SYSTEM_NAME } from '@/lib/constants'

import { mapStatusDataToConfig } from '../use-system-config'

describe('mapStatusDataToConfig branding', () => {
  test('maps the upstream backend default to the Lietio presentation brand', () => {
    expect(mapStatusDataToConfig({ system_name: 'New API' }).systemName).toBe(
      'Lietio'
    )
  })

  test('maps the production site alias to the Lietio presentation brand', () => {
    expect(
      mapStatusDataToConfig({ system_name: 'Lietio API' }).systemName
    ).toBe('Lietio')
  })

  test('keeps an explicitly configured site name', () => {
    expect(mapStatusDataToConfig({ system_name: 'Lietio' }).systemName).toBe(
      'Lietio'
    )
  })

  test('falls back to the Lietio presentation brand for blank or missing names', () => {
    expect(mapStatusDataToConfig({ system_name: '   ' }).systemName).toBe(
      'Lietio'
    )
    expect(mapStatusDataToConfig({}).systemName).toBe('Lietio')
    expect(DEFAULT_SYSTEM_NAME).toBe('Lietio')
  })

  test('maps the upstream default logo to the Lietio mark', () => {
    expect(mapStatusDataToConfig({ logo: '/logo.png' }).logo).toBe(
      '/lietio-mark.svg'
    )
    expect(mapStatusDataToConfig({}).logo).toBe('/lietio-mark.svg')
  })
})

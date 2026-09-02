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

import { getSystemDescription, getSystemVersion } from './system-status-brand'

describe('system status branding', () => {
  test('prefers direct configured values and trims them', () => {
    const status = {
      system_description: '  Private AI gateway  ',
      version: ' 2.4.1 ',
      data: {
        system_description: 'Nested fallback',
        version: '1.0.0',
      },
    }

    expect(getSystemDescription(status)).toBe('Private AI gateway')
    expect(getSystemVersion(status)).toBe('2.4.1')
  })

  test('supports nested status payloads and empty states', () => {
    expect(
      getSystemDescription({ data: { site_description: 'Team gateway' } })
    ).toBe('Team gateway')
    expect(getSystemVersion({ data: { version: '3.0.0' } })).toBe('3.0.0')
    expect(getSystemDescription(null)).toBeNull()
  })
})

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
import { describe, expect, it } from 'vitest'

import { isDevtoolsEnabled } from './devtools'

describe('isDevtoolsEnabled', () => {
  it('enables overlays only for an explicit development opt-in', () => {
    expect(
      isDevtoolsEnabled({ MODE: 'development', VITE_ENABLE_DEVTOOLS: 'true' })
    ).toBe(true)
  })

  it.each([
    {},
    { MODE: 'development' },
    { MODE: 'development', VITE_ENABLE_DEVTOOLS: 'false' },
    { MODE: 'development', VITE_ENABLE_DEVTOOLS: '1' },
    { MODE: 'development', VITE_ENABLE_DEVTOOLS: 'TRUE' },
    { MODE: 'development', VITE_ENABLE_DEVTOOLS: 'true ' },
    { MODE: 'production', VITE_ENABLE_DEVTOOLS: 'true' },
  ])('keeps overlays disabled for %#', (env) => {
    expect(isDevtoolsEnabled(env)).toBe(false)
  })
})

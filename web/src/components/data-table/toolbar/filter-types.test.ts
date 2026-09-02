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
import { describe, expect, test, vi } from 'vitest'

import { resolveDataTableFilterLabel } from './filter-types'

describe('resolveDataTableFilterLabel', () => {
  test('translates an explicit key even when the current label is already localized', () => {
    const translate = vi.fn((key: string) =>
      key === 'Enabled' ? '已启用' : key
    )

    const label = resolveDataTableFilterLabel(
      { label: '已启用', labelKey: 'Enabled' },
      translate,
      () => false
    )

    expect(label).toBe('已启用')
    expect(translate).toHaveBeenCalledWith('Enabled')
  })

  test('preserves dynamic labels that happen to match an i18n key', () => {
    const translate = vi.fn((key: string) => `translated:${key}`)

    const label = resolveDataTableFilterLabel(
      { label: 'OpenAI', translateLabel: false },
      translate,
      () => true
    )

    expect(label).toBe('OpenAI')
    expect(translate).not.toHaveBeenCalled()
  })

  test('keeps legacy key and localized option definitions compatible', () => {
    const translate = vi.fn((key: string) =>
      key === 'Enabled' ? 'Enabled (localized)' : key
    )

    expect(
      resolveDataTableFilterLabel(
        { label: 'Enabled' },
        translate,
        (key) => key === 'Enabled'
      )
    ).toBe('Enabled (localized)')
    expect(
      resolveDataTableFilterLabel({ label: '已启用' }, translate, () => false)
    ).toBe('已启用')
  })
})

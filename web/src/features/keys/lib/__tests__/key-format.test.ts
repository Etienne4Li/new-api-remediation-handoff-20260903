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

import { withApiKeyPrefix } from '../key-format'

describe('withApiKeyPrefix', () => {
  test('adds the marker to legacy raw token values', () => {
    expect(withApiKeyPrefix('  abc123  ')).toBe('sk-abc123')
  })

  test('does not duplicate a marker returned by secure reveal APIs', () => {
    expect(withApiKeyPrefix('sk-abc123')).toBe('sk-abc123')
  })

  test('returns an empty string for an empty value', () => {
    expect(withApiKeyPrefix('   ')).toBe('')
  })
})

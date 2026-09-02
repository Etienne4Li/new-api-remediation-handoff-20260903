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
import { describe, expect, test } from 'vitest'

import { DateTimePicker } from '../datetime-picker'

describe('DateTimePicker value synchronization', () => {
  test('reflects the initial value, a replacement value, and an external clear', () => {
    const initialValue = new Date(2026, 0, 2, 3, 4)
    const replacementValue = new Date(2027, 5, 7, 8, 9)
    const { rerender } = render(<DateTimePicker value={initialValue} />)

    expect(screen.getByRole('button', { name: '2026-01-02' })).toBeVisible()
    expect(screen.getByDisplayValue('03:04')).toBeEnabled()

    rerender(<DateTimePicker value={replacementValue} />)

    expect(screen.getByRole('button', { name: '2027-06-07' })).toBeVisible()
    expect(screen.getByDisplayValue('08:09')).toBeEnabled()

    rerender(<DateTimePicker value={undefined} />)

    expect(screen.getByRole('button', { name: 'Select date' })).toBeVisible()
    expect(screen.getByDisplayValue('08:09')).toBeDisabled()
  })
})

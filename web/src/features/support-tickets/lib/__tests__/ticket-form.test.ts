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

import { ticketFormSchema } from '../ticket-form'

function ticketInput(title: string, content = 'Details') {
  return {
    title,
    category: 'other' as const,
    priority: 'normal' as const,
    content,
  }
}

describe('ticketFormSchema', () => {
  test('trims and accepts a title with exactly five Unicode code points', () => {
    const result = ticketFormSchema.safeParse(ticketInput('  😀😀😀😀😀  '))

    expect(result.success).toBe(true)
    if (result.success) expect(result.data.title).toBe('😀😀😀😀😀')
  })

  test('rejects a title with four Unicode code points', () => {
    const result = ticketFormSchema.safeParse(ticketInput('😀😀😀😀'))

    expect(result.success).toBe(false)
    if (!result.success) {
      expect(result.error.issues[0]?.message).toBe(
        'Ticket title must be at least 5 characters'
      )
    }
  })

  test('accepts 120 Unicode code points and rejects 121', () => {
    expect(
      ticketFormSchema.safeParse(ticketInput('😀'.repeat(120))).success
    ).toBe(true)
    expect(
      ticketFormSchema.safeParse(ticketInput('😀'.repeat(121))).success
    ).toBe(false)
  })

  test('enforces the 5000-code-point message boundary', () => {
    expect(
      ticketFormSchema.safeParse(ticketInput('Valid title', '😀'.repeat(5000)))
        .success,
    ).toBe(true)
    expect(
      ticketFormSchema.safeParse(ticketInput('Valid title', '😀'.repeat(5001)))
        .success,
    ).toBe(false)
  })

  test('rejects a message containing only whitespace', () => {
    expect(
      ticketFormSchema.safeParse(ticketInput('Valid title', '   ')).success,
    ).toBe(false)
  })
})

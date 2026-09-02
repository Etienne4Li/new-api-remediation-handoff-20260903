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

import { Card, CardFooter, CardHeader } from '../card'

describe('Card radius contract', () => {
  test('caps panel corners at eight pixels while respecting smaller themes', () => {
    render(
      <Card data-testid='card'>
        <CardHeader data-testid='header'>Header</CardHeader>
        <CardFooter data-testid='footer'>Footer</CardFooter>
      </Card>
    )

    expect(screen.getByTestId('card')).toHaveClass(
      'rounded-[min(var(--radius),8px)]'
    )
    expect(screen.getByTestId('header')).toHaveClass(
      'rounded-t-[min(var(--radius),8px)]'
    )
    expect(screen.getByTestId('footer')).toHaveClass(
      'rounded-b-[min(var(--radius),8px)]'
    )
  })
})

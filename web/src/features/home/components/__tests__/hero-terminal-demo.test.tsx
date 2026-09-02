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
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'

import { HeroTerminalDemo } from '../hero-terminal-demo'

describe('gateway console', () => {
  test('shows live system status and switches protocol previews in place', () => {
    render(
      <HeroTerminalDemo statusReady systemName='Lietio API' version='v2.4.1' />
    )

    const consoleRegion = screen.getByRole('region', { name: 'API Requests' })
    expect(consoleRegion).toHaveAttribute('data-slot', 'gateway-console')
    expect(consoleRegion).toHaveClass('min-w-0', 'overflow-hidden')
    expect(screen.getByText('Online')).toBeInTheDocument()
    expect(screen.getByText('v2.4.1')).toBeInTheDocument()
    expect(screen.getAllByText('/v1/chat/completions').length).toBeGreaterThan(
      0
    )

    fireEvent.click(screen.getByRole('button', { name: /Claude/ }))

    expect(screen.getAllByText('/v1/messages').length).toBeGreaterThan(0)
    expect(
      screen.getByText('anthropic-version: 2023-06-01')
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Claude/ })).toHaveAttribute(
      'aria-pressed',
      'true'
    )
  })
})

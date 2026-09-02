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
import userEvent from '@testing-library/user-event'
import { describe, expect, test, vi } from 'vitest'

import { getVisibleDashboardSections } from '../dashboard-workspace'
import { DashboardWorkspaceHeader } from '../dashboard-workspace-header'

describe('dashboard workspace header', () => {
  test('keeps admin analytics hidden from regular users', () => {
    expect(getVisibleDashboardSections(false)).toEqual([
      'overview',
      'models',
      'flow',
    ])
    expect(getVisibleDashboardSections(true)).toEqual([
      'overview',
      'models',
      'flow',
      'users',
    ])
  })

  test('exposes the active view and changes sections through accessible tabs', async () => {
    const user = userEvent.setup()
    const onSectionChange = vi.fn()

    render(
      <DashboardWorkspaceHeader
        activeSection='models'
        visibleSections={getVisibleDashboardSections(false)}
        onSectionChange={onSectionChange}
      />
    )

    expect(screen.getByRole('tablist', { name: 'Dashboard' })).toHaveClass(
      'w-full',
      'overflow-x-auto'
    )
    expect(
      screen.getByRole('tab', { name: 'Model Call Analytics' })
    ).toHaveAttribute('aria-selected', 'true')
    expect(
      screen.queryByRole('tab', { name: 'User Analytics' })
    ).not.toBeInTheDocument()

    await user.click(screen.getByRole('tab', { name: 'Flow' }))

    expect(onSectionChange.mock.calls[0]?.[0]).toBe('flow')
  })

  test('keeps section actions reachable in a horizontally scrollable row', () => {
    const { container } = render(
      <DashboardWorkspaceHeader
        activeSection='flow'
        visibleSections={getVisibleDashboardSections(true)}
        onSectionChange={() => undefined}
        actions={<button type='button'>Filter</button>}
      />
    )

    const actions = container.querySelector<HTMLElement>(
      '[data-dashboard-workspace-actions]'
    )
    expect(actions).toHaveClass('min-w-0', 'shrink-0', 'overflow-x-auto')
    expect(screen.getByRole('button', { name: 'Filter' })).toBeVisible()
    expect(
      screen.getByRole('tab', { name: 'User Analytics' })
    ).toBeInTheDocument()
  })
})

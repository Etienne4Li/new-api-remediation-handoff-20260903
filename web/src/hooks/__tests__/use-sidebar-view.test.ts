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
import { renderHook } from '@testing-library/react'
import { describe, expect, test } from 'vitest'

import { ROLE } from '@/lib/roles'

import { useSidebarData } from '../use-sidebar-data'
import { filterNavGroupsByRole } from '../use-sidebar-view'

function adminLabels(role: number) {
  const { result } = renderHook(() => useSidebarData())
  const admin = filterNavGroupsByRole(result.current.navGroups, role).find(
    (group) => group.id === 'admin'
  )
  return admin?.items.map((item) => item.title) ?? []
}

describe('sidebar role visibility', () => {
  test('hides root-only system settings from regular administrators', () => {
    expect(adminLabels(ROLE.ADMIN)).not.toContain('System Settings')
  })

  test('keeps root-only system settings available to the root administrator', () => {
    expect(adminLabels(ROLE.SUPER_ADMIN)).toContain('System Settings')
  })

  test('hides the complete admin group from regular users', () => {
    const { result } = renderHook(() => useSidebarData())
    expect(
      filterNavGroupsByRole(result.current.navGroups, ROLE.USER).some(
        (group) => group.id === 'admin'
      )
    ).toBe(false)
  })
})

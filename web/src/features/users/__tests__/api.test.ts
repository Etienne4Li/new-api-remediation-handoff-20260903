/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.
*/
import { afterEach, describe, expect, test } from 'vitest'

import { api } from '@/lib/api'

import { deleteUser } from '../api'

describe('user management API', () => {
  const originalDelete = api.delete

  afterEach(() => {
    api.delete = originalDelete
  })

  test('uses the backend delete route without a trailing slash', async () => {
    let requestedUrl = ''
    api.delete = (async (url: string) => {
      requestedUrl = url
      return { data: { success: true } }
    }) as unknown as typeof api.delete

    await expect(deleteUser(42)).resolves.toEqual({ success: true })
    expect(requestedUrl).toBe('/api/user/42')
  })
})

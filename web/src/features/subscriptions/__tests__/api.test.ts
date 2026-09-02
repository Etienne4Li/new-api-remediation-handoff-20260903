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
import { afterEach, describe, expect, test } from 'vitest'

import { api } from '@/lib/api'

import { getGroups } from '../api'

describe('subscription group API', () => {
  const originalGet = api.get

  afterEach(() => {
    api.get = originalGet
  })

  test('uses the canonical trailing-slash backend route', async () => {
    let requestedUrl = ''
    api.get = (async (url: string) => {
      requestedUrl = url
      return { data: { success: true, data: ['default'] } }
    }) as unknown as typeof api.get

    await expect(getGroups()).resolves.toEqual({
      success: true,
      data: ['default'],
    })
    expect(requestedUrl).toBe('/api/group/')
  })
})

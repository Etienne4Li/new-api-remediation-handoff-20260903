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
import { renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { useTopupInfo } from '../use-topup-info'

describe('useTopupInfo failure state', () => {
  const originalGet = api.get

  afterEach(() => {
    api.get = originalGet
    vi.restoreAllMocks()
  })

  test('exposes a retryable error when the backend rejects the request', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    api.get = (async () => ({
      data: { success: false, message: 'billing unavailable' },
    })) as unknown as typeof api.get

    const { result } = renderHook(() => useTopupInfo())

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toBe(true)
    expect(result.current.topupInfo).toBeNull()
  })
})

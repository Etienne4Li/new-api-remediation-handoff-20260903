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
import { act, renderHook, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, test, vi } from 'vitest'

import { useTwoFA } from '../use-two-fa'

const apiMocks = vi.hoisted(() => ({
  get2FAStatus: vi.fn(),
}))

vi.mock('@/lib/api', () => apiMocks)

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((promiseResolve) => {
    resolve = promiseResolve
  })
  return { promise, resolve }
}

beforeEach(() => {
  apiMocks.get2FAStatus.mockResolvedValue({
    success: true,
    data: { enabled: false, locked: false, backup_codes_remaining: 0 },
  })
})

describe('useTwoFA request lifecycle', () => {
  test('is idle and does not request status while disabled', () => {
    const { result } = renderHook(() => useTwoFA(false))

    expect(result.current.loading).toBe(false)
    expect(apiMocks.get2FAStatus).not.toHaveBeenCalled()
  })

  test('keeps the newest status when an older refresh resolves last', async () => {
    const { result } = renderHook(() => useTwoFA())
    await waitFor(() => expect(result.current.loading).toBe(false))

    const olderRequest = deferred<{
      success: boolean
      data: {
        enabled: boolean
        locked: boolean
        backup_codes_remaining: number
      }
    }>()
    const newerRequest = deferred<{
      success: boolean
      data: {
        enabled: boolean
        locked: boolean
        backup_codes_remaining: number
      }
    }>()
    apiMocks.get2FAStatus
      .mockReturnValueOnce(olderRequest.promise)
      .mockReturnValueOnce(newerRequest.promise)

    let olderRefresh!: Promise<void>
    let newerRefresh!: Promise<void>
    act(() => {
      olderRefresh = result.current.refetch()
      newerRefresh = result.current.refetch()
    })

    await act(async () => {
      newerRequest.resolve({
        success: true,
        data: { enabled: true, locked: false, backup_codes_remaining: 8 },
      })
      await newerRefresh
    })

    await act(async () => {
      olderRequest.resolve({
        success: true,
        data: { enabled: false, locked: false, backup_codes_remaining: 0 },
      })
      await olderRefresh
    })

    expect(result.current.status).toEqual({
      enabled: true,
      locked: false,
      backup_codes_remaining: 8,
    })
  })
})

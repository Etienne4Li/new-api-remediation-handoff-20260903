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

import { usePasskeyManagement } from '../use-passkey-management'

const apiMocks = vi.hoisted(() => ({
  beginPasskeyRegistration: vi.fn(),
  deletePasskey: vi.fn(),
  finishPasskeyRegistration: vi.fn(),
  getPasskeyStatus: vi.fn(),
}))
const passkeyMocks = vi.hoisted(() => ({
  buildRegistrationResult: vi.fn(),
  createCredential: vi.fn(),
  isPasskeySupported: vi.fn(),
  prepareCredentialCreationOptions: vi.fn(),
}))
const toastMocks = vi.hoisted(() => ({
  error: vi.fn(),
}))

vi.mock('@/features/auth/passkey/api', () => apiMocks)
vi.mock('@/lib/passkey', () => passkeyMocks)
vi.mock('sonner', () => ({ toast: toastMocks }))

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve
    reject = promiseReject
  })
  return { promise, resolve, reject }
}

beforeEach(() => {
  apiMocks.getPasskeyStatus.mockResolvedValue({
    success: true,
    data: { enabled: true, last_used_at: '2026-09-01T00:00:00Z' },
  })
  passkeyMocks.isPasskeySupported.mockResolvedValue(true)
})

describe('usePasskeyManagement initial status', () => {
  test('loads account status and browser support after mounting', async () => {
    const onStatusChange = vi.fn()
    const { result } = renderHook(() =>
      usePasskeyManagement({ onStatusChange })
    )

    await waitFor(() => expect(result.current.loading).toBe(false))
    await waitFor(() => expect(result.current.supported).toBe(true))

    expect(result.current.status).toEqual({
      enabled: true,
      last_used_at: '2026-09-01T00:00:00Z',
    })
    expect(result.current.enabled).toBe(true)
    expect(onStatusChange).toHaveBeenCalledWith(result.current.status)
  })

  test('keeps the newest status when an older refresh resolves last', async () => {
    const { result } = renderHook(() => usePasskeyManagement())

    await waitFor(() => expect(result.current.loading).toBe(false))

    const olderRefresh = deferred<{
      success: boolean
      data: { enabled: boolean; last_used_at: string }
    }>()
    const newerRefresh = deferred<{
      success: boolean
      data: { enabled: boolean; last_used_at: string }
    }>()
    apiMocks.getPasskeyStatus
      .mockReturnValueOnce(olderRefresh.promise)
      .mockReturnValueOnce(newerRefresh.promise)

    let olderPromise!: Promise<void>
    let newerPromise!: Promise<void>
    act(() => {
      olderPromise = result.current.fetchStatus()
      newerPromise = result.current.fetchStatus()
    })

    await act(async () => {
      newerRefresh.resolve({
        success: true,
        data: { enabled: true, last_used_at: '2026-09-02T00:00:00Z' },
      })
      await newerPromise
    })
    expect(result.current.enabled).toBe(true)

    await act(async () => {
      olderRefresh.resolve({
        success: true,
        data: { enabled: false, last_used_at: '2026-09-01T00:00:00Z' },
      })
      await olderPromise
    })

    expect(result.current.enabled).toBe(true)
    expect(result.current.lastUsed).toBe('2026-09-02T00:00:00Z')
  })

  test('keeps loading while a newer status refresh is still pending', async () => {
    const { result } = renderHook(() => usePasskeyManagement())
    await waitFor(() => expect(result.current.loading).toBe(false))

    const olderRefresh = deferred<{
      success: boolean
      data: { enabled: boolean; last_used_at: string }
    }>()
    const newerRefresh = deferred<{
      success: boolean
      data: { enabled: boolean; last_used_at: string }
    }>()
    apiMocks.getPasskeyStatus
      .mockReturnValueOnce(olderRefresh.promise)
      .mockReturnValueOnce(newerRefresh.promise)

    let olderPromise!: Promise<void>
    let newerPromise!: Promise<void>
    act(() => {
      olderPromise = result.current.fetchStatus()
      newerPromise = result.current.fetchStatus()
    })

    await act(async () => {
      olderRefresh.resolve({
        success: true,
        data: { enabled: false, last_used_at: '2026-09-01T00:00:00Z' },
      })
      await olderPromise
    })
    expect(result.current.loading).toBe(true)

    await act(async () => {
      newerRefresh.resolve({
        success: true,
        data: { enabled: true, last_used_at: '2026-09-02T00:00:00Z' },
      })
      await newerPromise
    })
    expect(result.current.loading).toBe(false)
  })

  test('ignores an older status failure after a newer refresh succeeds', async () => {
    const onStatusChange = vi.fn()
    const { result } = renderHook(() =>
      usePasskeyManagement({ onStatusChange })
    )
    await waitFor(() => expect(result.current.loading).toBe(false))
    onStatusChange.mockClear()

    const olderRefresh = deferred<{
      success: boolean
      data: { enabled: boolean; last_used_at: string }
    }>()
    const newerRefresh = deferred<{
      success: boolean
      data: { enabled: boolean; last_used_at: string }
    }>()
    apiMocks.getPasskeyStatus
      .mockReturnValueOnce(olderRefresh.promise)
      .mockReturnValueOnce(newerRefresh.promise)

    let olderPromise!: Promise<void>
    let newerPromise!: Promise<void>
    act(() => {
      olderPromise = result.current.fetchStatus()
      newerPromise = result.current.fetchStatus()
    })

    await act(async () => {
      newerRefresh.resolve({
        success: true,
        data: { enabled: true, last_used_at: '2026-09-02T00:00:00Z' },
      })
      await newerPromise
    })
    await act(async () => {
      olderRefresh.reject(new Error('obsolete failure'))
      await olderPromise
    })

    expect(result.current.enabled).toBe(true)
    expect(result.current.lastUsed).toBe('2026-09-02T00:00:00Z')
    expect(result.current.loading).toBe(false)
    expect(onStatusChange).toHaveBeenCalledOnce()
    expect(toastMocks.error).not.toHaveBeenCalled()
  })
})

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

import type { UserProfile } from '../../types'
import { useProfile } from '../use-profile'

const apiMocks = vi.hoisted(() => ({
  getUserProfile: vi.fn(),
  updateUserProfile: vi.fn(),
  updateUserSettings: vi.fn(),
}))

vi.mock('@/features/profile/api', () => apiMocks)

function profile(username: string): UserProfile {
  return {
    id: 1,
    username,
    display_name: username,
    role: 1,
    group: 'default',
    quota: 0,
    used_quota: 0,
    request_count: 0,
    status: 1,
    aff_count: 0,
    aff_quota: 0,
    aff_history_quota: 0,
    created_time: 1,
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((promiseResolve) => {
    resolve = promiseResolve
  })
  return { promise, resolve }
}

beforeEach(() => {
  apiMocks.getUserProfile.mockResolvedValue({
    success: true,
    data: profile('initial-user'),
  })
})

describe('useProfile request lifecycle', () => {
  test('keeps the newest profile when an older refresh resolves last', async () => {
    const { result } = renderHook(() => useProfile())
    await waitFor(() => expect(result.current.loading).toBe(false))

    const olderRequest = deferred<{
      success: boolean
      data: UserProfile
    }>()
    const newerRequest = deferred<{
      success: boolean
      data: UserProfile
    }>()
    apiMocks.getUserProfile
      .mockReturnValueOnce(olderRequest.promise)
      .mockReturnValueOnce(newerRequest.promise)

    let olderRefresh!: Promise<void>
    let newerRefresh!: Promise<void>
    act(() => {
      olderRefresh = result.current.refreshProfile()
      newerRefresh = result.current.refreshProfile()
    })

    await act(async () => {
      newerRequest.resolve({ success: true, data: profile('newer-user') })
      await newerRefresh
    })

    await act(async () => {
      olderRequest.resolve({ success: true, data: profile('older-user') })
      await olderRefresh
    })

    expect(result.current.profile?.username).toBe('newer-user')
  })
})

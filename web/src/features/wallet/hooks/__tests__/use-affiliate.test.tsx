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
*/
import { act, renderHook, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, test, vi } from 'vitest'

import { useAffiliate } from '../use-affiliate'

const apiMocks = vi.hoisted(() => ({
  getAffiliateCode: vi.fn(),
  transferAffiliateQuota: vi.fn(),
  getSelf: vi.fn(),
}))

vi.mock('@/features/wallet/api', () => ({
  getAffiliateCode: apiMocks.getAffiliateCode,
  transferAffiliateQuota: apiMocks.transferAffiliateQuota,
}))

vi.mock('@/lib/api', () => ({
  getSelf: apiMocks.getSelf,
}))

vi.mock('@/hooks/use-copy-to-clipboard', () => ({
  useCopyToClipboard: () => ({
    copyToClipboard: vi.fn(),
  }),
}))

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((promiseResolve) => {
    resolve = promiseResolve
  })
  return { promise, resolve }
}

beforeEach(() => {
  apiMocks.getAffiliateCode.mockReset()
  apiMocks.transferAffiliateQuota.mockReset()
  apiMocks.getSelf.mockReset()
})

describe('useAffiliate request lifecycle', () => {
  test('keeps the newest affiliate code when an older refresh resolves last', async () => {
    const initialRequest = deferred<{ success: boolean; data: string }>()
    const refreshRequest = deferred<{ success: boolean; data: string }>()
    apiMocks.getAffiliateCode
      .mockReturnValueOnce(initialRequest.promise)
      .mockReturnValueOnce(refreshRequest.promise)

    const { result } = renderHook(() => useAffiliate())
    await waitFor(() =>
      expect(apiMocks.getAffiliateCode).toHaveBeenCalledTimes(1)
    )

    let refresh!: Promise<void>
    act(() => {
      refresh = result.current.refetch()
    })
    await waitFor(() =>
      expect(apiMocks.getAffiliateCode).toHaveBeenCalledTimes(2)
    )

    await act(async () => {
      refreshRequest.resolve({ success: true, data: 'new-code' })
      await refresh
    })
    expect(result.current.affiliateCode).toBe('new-code')

    await act(async () => {
      initialRequest.resolve({ success: true, data: 'old-code' })
      await initialRequest.promise
    })

    expect(result.current.affiliateCode).toBe('new-code')
    expect(result.current.affiliateLink).toContain('aff=new-code')
  })

  test('does not update state after unmount when the request resolves', async () => {
    const request = deferred<{ success: boolean; data: string }>()
    apiMocks.getAffiliateCode.mockReturnValueOnce(request.promise)
    const { unmount } = renderHook(() => useAffiliate())
    await waitFor(() =>
      expect(apiMocks.getAffiliateCode).toHaveBeenCalledTimes(1)
    )

    unmount()
    await act(async () => {
      request.resolve({ success: true, data: 'late-code' })
      await request.promise
    })
  })
})

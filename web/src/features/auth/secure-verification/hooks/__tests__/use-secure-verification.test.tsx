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

import { useSecureVerification } from '../use-secure-verification'

const apiMocks = vi.hoisted(() => ({
  checkVerificationMethods: vi.fn(),
  verify: vi.fn(),
}))

vi.mock('@/features/auth/secure-verification/api', () => apiMocks)

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((promiseResolve) => {
    resolve = promiseResolve
  })
  return { promise, resolve }
}

beforeEach(() => {
  apiMocks.checkVerificationMethods.mockResolvedValue({
    has2FA: true,
    hasPasskey: true,
    passkeySupported: true,
  })
})

describe('useSecureVerification initial methods', () => {
  test('loads the available verification methods after mounting', async () => {
    const { result } = renderHook(() => useSecureVerification())

    await waitFor(() => expect(result.current.hasAnyMethod).toBe(true))

    expect(result.current.methods).toEqual({
      has2FA: true,
      hasPasskey: true,
      passkeySupported: true,
    })
    expect(result.current.recommendedMethod).toBe('passkey')
  })

  test('keeps the newest verification flow when an older start resolves last', async () => {
    const { result } = renderHook(() => useSecureVerification())
    await waitFor(() => expect(result.current.hasAnyMethod).toBe(true))

    const olderMethods = deferred<{
      has2FA: boolean
      hasPasskey: boolean
      passkeySupported: boolean
    }>()
    const newerMethods = deferred<{
      has2FA: boolean
      hasPasskey: boolean
      passkeySupported: boolean
    }>()
    apiMocks.checkVerificationMethods
      .mockReturnValueOnce(olderMethods.promise)
      .mockReturnValueOnce(newerMethods.promise)

    let olderStart!: Promise<boolean>
    let newerStart!: Promise<boolean>
    act(() => {
      olderStart = result.current.startVerification(vi.fn(), {
        scope: 'channel.key.read',
        title: 'Older flow',
      })
      newerStart = result.current.startVerification(vi.fn(), {
        scope: 'passkey.delete',
        title: 'Newer flow',
      })
    })

    await act(async () => {
      newerMethods.resolve({
        has2FA: true,
        hasPasskey: false,
        passkeySupported: false,
      })
      await newerStart
    })
    expect(result.current.state.scope).toBe('passkey.delete')
    expect(result.current.state.title).toBe('Newer flow')

    await act(async () => {
      olderMethods.resolve({
        has2FA: false,
        hasPasskey: true,
        passkeySupported: true,
      })
      await olderStart
    })

    expect(result.current.state.scope).toBe('passkey.delete')
    expect(result.current.state.title).toBe('Newer flow')
    expect(result.current.currentMethod).toBe('2fa')
  })

  test('marks an older methods result as obsolete when a newer query wins', async () => {
    const { result } = renderHook(() => useSecureVerification())
    await waitFor(() => expect(result.current.hasAnyMethod).toBe(true))

    const olderMethods = deferred<{
      has2FA: boolean
      hasPasskey: boolean
      passkeySupported: boolean
    }>()
    const newerMethods = deferred<{
      has2FA: boolean
      hasPasskey: boolean
      passkeySupported: boolean
    }>()
    apiMocks.checkVerificationMethods
      .mockReturnValueOnce(olderMethods.promise)
      .mockReturnValueOnce(newerMethods.promise)

    let olderQuery!: ReturnType<typeof result.current.fetchVerificationMethods>
    let newerQuery!: ReturnType<typeof result.current.fetchVerificationMethods>
    act(() => {
      olderQuery = result.current.fetchVerificationMethods()
      newerQuery = result.current.fetchVerificationMethods()
    })

    const newest = {
      has2FA: true,
      hasPasskey: false,
      passkeySupported: false,
    }
    await act(async () => {
      newerMethods.resolve(newest)
      await newerQuery
    })

    let obsoleteResult: unknown
    await act(async () => {
      olderMethods.resolve({
        has2FA: false,
        hasPasskey: true,
        passkeySupported: true,
      })
      obsoleteResult = await olderQuery
    })

    expect(obsoleteResult).toBeNull()
    expect(result.current.methods).toEqual(newest)
  })

  test('does not let a superseded execution close the newer flow', async () => {
    const onSuccess = vi.fn()
    const olderApiResult = deferred<unknown>()
    const olderApiCall = vi.fn(() => olderApiResult.promise)
    const newerApiCall = vi.fn()
    apiMocks.verify.mockResolvedValue({
      proof_token: 'proof-token',
      expires_at: 1,
      method: '2fa',
      scope: 'channel.key.read',
    })

    const { result } = renderHook(() => useSecureVerification({ onSuccess }))
    await waitFor(() => expect(result.current.hasAnyMethod).toBe(true))

    await act(async () => {
      await result.current.startVerification(olderApiCall, {
        scope: 'channel.key.read',
        title: 'Older flow',
      })
    })

    let execution!: Promise<unknown>
    act(() => {
      execution = result.current.executeVerification('2fa', '123456')
    })
    await waitFor(() => expect(olderApiCall).toHaveBeenCalledOnce())

    await act(async () => {
      await result.current.startVerification(newerApiCall, {
        scope: 'passkey.delete',
        title: 'Newer flow',
      })
    })
    expect(result.current.state.scope).toBe('passkey.delete')

    await act(async () => {
      olderApiResult.resolve({ source: 'older flow' })
      await execution
    })

    expect(result.current.open).toBe(true)
    expect(result.current.state.scope).toBe('passkey.delete')
    expect(result.current.state.title).toBe('Newer flow')
    expect(onSuccess).not.toHaveBeenCalled()
  })

  test('does not run an older sensitive action when its proof resolves after a new flow starts', async () => {
    const olderProof = deferred<{
      proof_token: string
      expires_at: number
      method: '2fa'
      scope: string
    }>()
    const olderApiCall = vi.fn()
    const newerApiCall = vi.fn()
    apiMocks.verify.mockReturnValueOnce(olderProof.promise)

    const { result } = renderHook(() => useSecureVerification())
    await waitFor(() => expect(result.current.hasAnyMethod).toBe(true))

    await act(async () => {
      await result.current.startVerification(olderApiCall, {
        scope: 'channel.key.read',
        title: 'Older flow',
      })
    })

    let execution!: Promise<unknown>
    act(() => {
      execution = result.current.executeVerification('2fa', '123456')
    })
    await waitFor(() => expect(apiMocks.verify).toHaveBeenCalledOnce())

    await act(async () => {
      await result.current.startVerification(newerApiCall, {
        scope: 'passkey.delete',
        title: 'Newer flow',
      })
    })

    await act(async () => {
      olderProof.resolve({
        proof_token: 'obsolete-proof',
        expires_at: 1,
        method: '2fa',
        scope: 'channel.key.read',
      })
      await execution
    })

    expect(olderApiCall).not.toHaveBeenCalled()
    expect(result.current.open).toBe(true)
    expect(result.current.state.scope).toBe('passkey.delete')
    expect(result.current.state.title).toBe('Newer flow')
  })
})

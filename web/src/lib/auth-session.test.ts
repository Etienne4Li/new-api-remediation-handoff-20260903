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
import { QueryClient } from '@tanstack/react-query'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { useAuthStore, type AuthBundle } from '../stores/auth-store'
import {
  applyAuthBundle,
  applyAuthRotation,
  bootstrapAuthentication,
  clearAuthenticatedClientState,
  createAuthRefreshCoordinator,
  createRefreshRunner,
  isAuthBundle,
  refreshAuthentication,
  type AuthRefreshRuntime,
  type RefreshOutcome,
} from './auth-session'

const bundle: AuthBundle = {
  access_token: 'access-token',
  token_type: 'Bearer',
  access_expires_at: Math.floor(Date.now() / 1000) + 600,
  user: {
    id: 42,
    username: 'test-user',
    role: 1,
  },
  session: {
    sid: 'session-a',
    current: true,
    login_method: 'password',
    ip: '127.0.0.1',
    user_agent: 'test',
    created_at: 100,
    last_active_at: 100,
    expires_at: 1000,
  },
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve
    reject = promiseReject
  })
  return { promise, resolve, reject }
}

afterEach(() => {
  useAuthStore.getState().auth.reset('idle')
})

describe('authentication session coordination', () => {
  test('keeps the underlying refresh running after its only waiter aborts', async () => {
    const operation = deferred<RefreshOutcome>()
    let operationCount = 0
    const coordinateRefresh = createAuthRefreshCoordinator(() => {
      operationCount += 1
      return operation.promise
    })
    const abortController = new AbortController()
    const cancellation = new Error('GitHub login timed out')

    const outcome = coordinateRefresh(abortController.signal)
    await Promise.resolve()
    abortController.abort(cancellation)

    await expect(outcome).rejects.toBe(cancellation)

    const laterWaiter = coordinateRefresh()
    operation.resolve({ kind: 'anonymous' })
    await expect(laterWaiter).resolves.toEqual({ kind: 'anonymous' })
    expect(operationCount).toBe(1)
  })

  test('keeps a shared refresh running for a consumer that did not cancel', async () => {
    const operation = deferred<RefreshOutcome>()
    let operationCount = 0
    const coordinateRefresh = createAuthRefreshCoordinator(() => {
      operationCount += 1
      return operation.promise
    })
    const abortController = new AbortController()
    const cancellation = new Error('GitHub login timed out')

    const cancelledOutcome = coordinateRefresh(abortController.signal)
    const persistentOutcome = coordinateRefresh()
    await Promise.resolve()
    abortController.abort(cancellation)

    await expect(cancelledOutcome).rejects.toBe(cancellation)

    operation.resolve({ kind: 'authenticated', bundle })
    await expect(persistentOutcome).resolves.toEqual({
      kind: 'authenticated',
      bundle,
    })
    expect(operationCount).toBe(1)
  })

  test('does not start a refresh for a waiter that is already cancelled', async () => {
    const performRefresh = vi.fn(
      async (): Promise<RefreshOutcome> => ({
        kind: 'anonymous',
      })
    )
    const coordinateRefresh = createAuthRefreshCoordinator(performRefresh)
    const abortController = new AbortController()
    const cancellation = new Error('GitHub login timed out')
    abortController.abort(cancellation)

    await expect(coordinateRefresh(abortController.signal)).rejects.toBe(
      cancellation
    )
    expect(performRefresh).not.toHaveBeenCalled()
  })

  test('bootstrap distinguishes a completed anonymous check from an active session', async () => {
    useAuthStore.getState().auth.reset('complete')
    expect(await bootstrapAuthentication()).toEqual({ kind: 'anonymous' })

    useAuthStore.getState().auth.setBundle(bundle)
    expect(await bootstrapAuthentication()).toEqual({
      kind: 'authenticated',
      bundle,
    })
  })

  test('a session mismatch clears only local state and retries without the stale SID', async () => {
    let expectedSID: string | undefined = bundle.session.sid
    const requestedSIDs: Array<string | undefined> = []
    const clears: Array<[boolean, string | undefined]> = []
    const accepted: AuthBundle[] = []
    const runtime: AuthRefreshRuntime = {
      request: async (sid) => {
        requestedSIDs.push(sid)
        if (requestedSIDs.length === 1) {
          return {
            status: 409,
            data: { code: 'AUTH_SESSION_MISMATCH' },
          }
        }
        return { status: 200, data: { success: true, data: bundle } }
      },
      getExpectedSID: () => expectedSID,
      parseBundle: (value) => (isAuthBundle(value) ? value : null),
      acceptBundle: (acceptedBundle) => accepted.push(acceptedBundle),
      clear: (synchronizeTabs, bootstrapState) => {
        clears.push([synchronizeTabs, bootstrapState])
        expectedSID = undefined
      },
      markTransient: () => undefined,
      wait: async () => undefined,
    }

    const outcome = await createRefreshRunner(runtime)()

    expect(outcome.kind).toBe('authenticated')
    expect(requestedSIDs).toEqual([bundle.session.sid, undefined])
    expect(clears).toEqual([[false, 'idle']])
    expect(accepted).toEqual([bundle])
  })

  test('a rejected refresh confirms anonymous state and synchronizes sign-out', async () => {
    const clears: Array<[boolean, string | undefined]> = []
    const runtime: AuthRefreshRuntime = {
      request: async () => ({ status: 401 }),
      getExpectedSID: () => bundle.session.sid,
      parseBundle: () => null,
      acceptBundle: () => undefined,
      clear: (synchronizeTabs, bootstrapState) => {
        clears.push([synchronizeTabs, bootstrapState])
      },
      markTransient: () => undefined,
      wait: async () => undefined,
    }

    expect(await createRefreshRunner(runtime)()).toEqual({
      kind: 'anonymous',
    })
    expect(clears).toEqual([[true, undefined]])
  })

  test('a temporary refresh failure remains retryable without clearing the session', async () => {
    let transientCount = 0
    let clearCount = 0
    const runtime: AuthRefreshRuntime = {
      request: async () => ({ status: 503, error: new Error('unavailable') }),
      getExpectedSID: () => bundle.session.sid,
      parseBundle: () => null,
      acceptBundle: () => undefined,
      clear: () => {
        clearCount += 1
      },
      markTransient: () => {
        transientCount += 1
      },
      wait: async () => undefined,
    }

    const outcome = await createRefreshRunner(runtime)()

    expect(outcome.kind).toBe('transient_error')
    expect(clearCount).toBe(0)
    expect(transientCount).toBe(1)
  })

  test('a rate limited refresh remains retryable without clearing the session', async () => {
    let transientCount = 0
    let clearCount = 0
    const runtime: AuthRefreshRuntime = {
      request: async () => ({ status: 429 }),
      getExpectedSID: () => bundle.session.sid,
      parseBundle: () => null,
      acceptBundle: () => undefined,
      clear: () => {
        clearCount += 1
      },
      markTransient: () => {
        transientCount += 1
      },
      wait: async () => undefined,
    }

    const outcome = await createRefreshRunner(runtime)()

    expect(outcome.kind).toBe('transient_error')
    expect(clearCount).toBe(0)
    expect(transientCount).toBe(1)
  })

  test('an exhausted refresh race clears the unusable local session', async () => {
    const requestedDelays: number[] = []
    const clears: Array<[boolean, string | undefined]> = []
    const runtime: AuthRefreshRuntime = {
      request: async () => ({
        status: 409,
        data: { code: 'AUTH_REFRESH_RACE' },
      }),
      getExpectedSID: () => bundle.session.sid,
      parseBundle: () => null,
      acceptBundle: () => undefined,
      clear: (synchronizeTabs, bootstrapState) => {
        clears.push([synchronizeTabs, bootstrapState])
      },
      markTransient: () => undefined,
      wait: async (delay) => {
        requestedDelays.push(delay)
      },
    }

    expect(await createRefreshRunner(runtime)()).toEqual({
      kind: 'out_of_sync',
      code: 'AUTH_REFRESH_RACE',
    })
    expect(requestedDelays).toEqual([80, 200, 500])
    expect(clears).toEqual([[false, undefined]])
  })

  test('an unexpected successful response is treated as out of sync', async () => {
    let cleared = false
    const runtime: AuthRefreshRuntime = {
      request: async () => ({ status: 200, data: { success: true } }),
      getExpectedSID: () => bundle.session.sid,
      parseBundle: () => null,
      acceptBundle: () => undefined,
      clear: () => {
        cleared = true
      },
      markTransient: () => undefined,
      wait: async () => undefined,
    }

    expect(await createRefreshRunner(runtime)()).toEqual({
      kind: 'out_of_sync',
      code: 'AUTH_INVALID_REFRESH_RESPONSE',
    })
    expect(cleared).toBe(true)
  })

  test('a refresh response cannot restore credentials after a newer auth operation', async () => {
    let current = true
    let accepted = false
    const runtime: AuthRefreshRuntime = {
      request: async () => {
        current = false
        return { status: 200, data: { success: true, data: bundle } }
      },
      getExpectedSID: () => bundle.session.sid,
      parseBundle: (value) => (isAuthBundle(value) ? value : null),
      acceptBundle: () => {
        accepted = true
      },
      clear: () => undefined,
      markTransient: () => undefined,
      wait: async () => undefined,
      isCurrent: () => current,
    }

    const outcome = await createRefreshRunner(runtime)()

    expect(outcome.kind).toBe('transient_error')
    expect(accepted).toBe(false)
  })

  test('a cancelled waiter does not discard a late authentication bundle', async () => {
    const response = deferred<{
      status: number
      data: { success: boolean; data: AuthBundle }
    }>()
    const abortController = new AbortController()
    let requestCount = 0
    let acceptedCount = 0
    const runtime: AuthRefreshRuntime = {
      request: () => {
        requestCount += 1
        return response.promise
      },
      getExpectedSID: () => bundle.session.sid,
      parseBundle: (value) => (isAuthBundle(value) ? value : null),
      acceptBundle: () => {
        acceptedCount += 1
      },
      clear: () => undefined,
      markTransient: () => undefined,
      wait: async () => undefined,
    }

    const coordinateRefresh = createAuthRefreshCoordinator(() =>
      createRefreshRunner(runtime)()
    )
    const cancelledOutcome = coordinateRefresh(abortController.signal)
    await Promise.resolve()
    const cancellation = new Error('GitHub login timed out')
    abortController.abort(cancellation)
    await expect(cancelledOutcome).rejects.toBe(cancellation)

    const persistentOutcome = coordinateRefresh()
    response.resolve({
      status: 200,
      data: { success: true, data: bundle },
    })

    await expect(persistentOutcome).resolves.toEqual({
      kind: 'authenticated',
      bundle,
    })
    expect(requestCount).toBe(1)
    expect(acceptedCount).toBe(1)
  })

  test('does not let an old browser lock failure overwrite newer authentication', async () => {
    const lockError = new Error('Web Locks unavailable')
    const lockRequest = deferred<never>()
    const originalLocks = Object.getOwnPropertyDescriptor(navigator, 'locks')
    Object.defineProperty(navigator, 'locks', {
      configurable: true,
      value: {
        request: () => lockRequest.promise,
      },
    })

    try {
      const outcome = refreshAuthentication()
      await Promise.resolve()
      applyAuthBundle(bundle, false)
      lockRequest.reject(lockError)

      await expect(outcome).resolves.toEqual({
        kind: 'transient_error',
        error: lockError,
      })
      expect(useAuthStore.getState().auth.bootstrapState).toBe('complete')
      expect(useAuthStore.getState().auth.accessToken).toBe(bundle.access_token)
    } finally {
      if (originalLocks) {
        Object.defineProperty(navigator, 'locks', originalLocks)
      } else {
        Reflect.deleteProperty(navigator, 'locks')
      }
    }
  })

  test('explicit rotations update only the current session', () => {
    useAuthStore.getState().auth.setBundle(bundle)
    applyAuthRotation({
      access_token: 'rotated-token',
      token_type: 'Bearer',
      access_expires_at: bundle.access_expires_at + 60,
      session: { ...bundle.session, last_active_at: 200 },
    })

    expect(useAuthStore.getState().auth.accessToken).toBe('rotated-token')
    expect(useAuthStore.getState().auth.user).toBe(bundle.user)

    expect(() =>
      applyAuthRotation({
        access_token: 'non-bearer-token',
        token_type: 'Custom',
        access_expires_at: bundle.access_expires_at + 120,
        session: bundle.session,
      })
    ).toThrow(/Invalid authentication rotation response/)
    expect(() =>
      applyAuthRotation({
        access_token: 'non-current-token',
        token_type: 'Bearer',
        access_expires_at: bundle.access_expires_at + 120,
        session: { ...bundle.session, current: false },
      })
    ).toThrow(/Invalid authentication rotation response/)

    expect(() =>
      applyAuthRotation({
        access_token: 'wrong-session-token',
        token_type: 'Bearer',
        access_expires_at: bundle.access_expires_at + 120,
        session: { ...bundle.session, sid: 'session-b' },
      })
    ).toThrow(/session mismatch/)
    expect(useAuthStore.getState().auth.accessToken).toBe('rotated-token')
  })

  test('sign-out clears user-scoped query, mutation, and authentication state', () => {
    const queryClient = new QueryClient()
    queryClient.setQueryData(['account', bundle.user.id], {
      username: bundle.user.username,
    })
    queryClient.getMutationCache().build(queryClient, {
      mutationKey: ['account', bundle.user.id, 'update'],
      mutationFn: async () => undefined,
    })
    useAuthStore.getState().auth.setBundle(bundle)
    useAuthStore.getState().auth.setPending2FAFlowToken('pending-flow')

    clearAuthenticatedClientState(queryClient, false)

    expect(queryClient.getQueryCache().getAll().length).toBe(0)
    expect(queryClient.getMutationCache().getAll().length).toBe(0)
    expect(useAuthStore.getState().auth.user).toBe(null)
    expect(useAuthStore.getState().auth.accessToken).toBe(null)
    expect(useAuthStore.getState().auth.session).toBe(null)
    expect(useAuthStore.getState().auth.pending2FAFlowToken).toBe(null)
    expect(useAuthStore.getState().auth.bootstrapState).toBe('complete')

    const nextBundle: AuthBundle = {
      ...bundle,
      access_token: 'next-user-token',
      user: { id: 84, username: 'next-user', role: 1 },
      session: { ...bundle.session, sid: 'session-b' },
    }
    useAuthStore.getState().auth.setBundle(nextBundle)
    expect(queryClient.getQueryData(['account', bundle.user.id])).toBe(
      undefined
    )
  })
})

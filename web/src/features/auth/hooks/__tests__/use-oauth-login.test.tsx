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
import { act, renderHook } from '@testing-library/react'
import i18next from 'i18next'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { useOAuthLogin } from '../use-oauth-login'

const authApiMocks = vi.hoisted(() => ({
  createOAuthFlow: vi.fn(),
  logout: vi.fn(),
  telegramLogin: vi.fn(),
}))
const toastMocks = vi.hoisted(() => ({
  error: vi.fn(),
}))

vi.mock('@/features/auth/api', () => authApiMocks)
vi.mock('sonner', () => ({ toast: toastMocks }))

vi.mock('@/features/auth/hooks/use-auth-redirect', () => ({
  useAuthRedirect: () => ({ handleLoginSuccess: vi.fn() }),
}))

vi.mock('@/lib/api', () => ({
  clearAuthentication: vi.fn(),
  isAuthBundle: vi.fn(),
}))

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((promiseResolve) => {
    resolve = promiseResolve
  })
  return { promise, resolve }
}

beforeEach(async () => {
  vi.useFakeTimers()
  authApiMocks.logout.mockReturnValue(new Promise(() => undefined))
  i18next.addResource(
    'fr',
    'translation',
    'Continue with GitHub',
    'Continuer avec GitHub'
  )
  i18next.addResource(
    'fr',
    'translation',
    'Redirecting to GitHub...',
    'Redirection vers GitHub...'
  )
  await i18next.changeLanguage('en')
})

afterEach(() => {
  vi.useRealTimers()
})

describe('useOAuthLogin GitHub state', () => {
  test('keeps the pending timeout and translates its current state when the language changes', async () => {
    const { result } = renderHook(() =>
      useOAuthLogin({ github_client_id: 'github-client' })
    )

    act(() => {
      void result.current.handleGitHubLogin()
    })

    expect(result.current.githubButtonText).toBe('Redirecting to GitHub...')
    expect(result.current.githubButtonDisabled).toBe(true)
    expect(vi.getTimerCount()).toBe(1)

    await act(async () => {
      await i18next.changeLanguage('fr')
    })

    expect(result.current.githubButtonText).toBe('Redirection vers GitHub...')
    expect(result.current.githubButtonDisabled).toBe(true)
    expect(vi.getTimerCount()).toBe(1)
  })

  test('does not redirect when the OAuth flow resolves after the timeout', async () => {
    const flow = deferred<{
      flow_token: string
      code_challenge: string
      code_challenge_method: 'S256'
      redirect_uri: string
    }>()
    authApiMocks.logout.mockResolvedValue({ success: true })
    authApiMocks.createOAuthFlow.mockReturnValue(flow.promise)
    const open = vi.spyOn(window, 'open').mockImplementation(() => null)
    const { result } = renderHook(() =>
      useOAuthLogin({ github_client_id: 'github-client' })
    )

    let login!: Promise<void>
    await act(async () => {
      login = result.current.handleGitHubLogin()
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(authApiMocks.createOAuthFlow).toHaveBeenCalledOnce()
    const logoutSignal = authApiMocks.logout.mock.calls[0]?.[0] as AbortSignal
    const flowSignal = authApiMocks.createOAuthFlow.mock.calls[0]?.[2] as
      | AbortSignal
      | undefined
    expect(flowSignal).toBe(logoutSignal)
    expect(flowSignal?.aborted).toBe(false)

    act(() => {
      vi.advanceTimersByTime(20_000)
    })
    expect(flowSignal?.aborted).toBe(true)
    expect(result.current.githubButtonText).toBe(
      'Request timed out, please refresh and restart GitHub login'
    )

    await act(async () => {
      flow.resolve({
        flow_token: 'late-flow',
        code_challenge: 'late-challenge',
        code_challenge_method: 'S256',
        redirect_uri: 'https://example.test/oauth/github',
      })
      await login
    })

    expect(open).not.toHaveBeenCalled()
  })

  test('keeps the timeout state when logout rejects because it was cancelled', async () => {
    authApiMocks.logout.mockImplementation((signal?: AbortSignal) => {
      return new Promise((_resolve, reject) => {
        signal?.addEventListener('abort', () => reject(signal.reason), {
          once: true,
        })
      })
    })
    const open = vi.spyOn(window, 'open').mockImplementation(() => null)
    const { result } = renderHook(() =>
      useOAuthLogin({ github_client_id: 'github-client' })
    )

    let login!: Promise<void>
    act(() => {
      login = result.current.handleGitHubLogin()
    })
    expect(authApiMocks.logout).toHaveBeenCalledOnce()

    act(() => {
      vi.advanceTimersByTime(20_000)
    })
    await act(async () => {
      await login
    })

    expect(result.current.githubButtonText).toBe(
      'Request timed out, please refresh and restart GitHub login'
    )
    expect(result.current.githubButtonDisabled).toBe(true)
    expect(authApiMocks.createOAuthFlow).not.toHaveBeenCalled()
    expect(open).not.toHaveBeenCalled()
    expect(toastMocks.error).not.toHaveBeenCalled()
  })
})

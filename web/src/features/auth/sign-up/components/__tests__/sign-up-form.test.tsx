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
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest'

import { register, sendEmailVerification } from '../../../api'

import { SignUpForm } from '../sign-up-form'

vi.mock('@/hooks/use-status', () => ({
  useStatus: () => ({ status: mockStatus }),
}))

// Controllable Turnstile state shared across renders.
let mockTurnstileEnabled = true
let mockToken = ''
const listeners = new Set<() => void>()
function notify() {
  for (const listener of listeners) listener()
}
vi.mock('@/features/auth/hooks/use-turnstile', async () => {
  const { useEffect, useState } = await import('react')
  return {
    useTurnstile: () => {
      const [, setTick] = useState(0)
      useEffect(() => {
        const listener = () => setTick((t) => t + 1)
        listeners.add(listener)
        return () => {
          listeners.delete(listener)
        }
      }, [])
      return {
        isTurnstileEnabled: mockTurnstileEnabled,
        turnstileSiteKey: 'test-site-key',
        turnstileToken: mockToken,
        setTurnstileToken: (token: string) => {
          mockToken = token
          notify()
        },
        validateTurnstile: () =>
          !mockTurnstileEnabled || Boolean(mockToken),
      }
    },
  }
})

// Captures the onExpire callback passed to the real Turnstile component so
// tests can simulate Cloudflare expired/error callbacks directly.
let mockOnExpire: (() => void) | undefined
vi.mock('@/components/turnstile', () => ({
  Turnstile: (props: { onExpire?: () => void }) => {
    mockOnExpire = props.onExpire
    return <div data-testid='turnstile-mock' />
  },
}))

vi.mock('@/features/auth/hooks/use-auth-redirect', () => ({
  useAuthRedirect: () => ({
    redirectToLogin: mockRedirectToLogin,
    handleLoginResult: mockHandleLoginResult,
  }),
}))

vi.mock('../../../api', () => ({
  register: vi.fn(),
  sendEmailVerification: vi.fn(),
  wechatLoginByCode: vi.fn(),
}))

const mockRegister = register as Mock
const mockSendEmailVerification = sendEmailVerification as Mock
const mockRedirectToLogin = vi.fn()
const mockHandleLoginResult = vi.fn()

let mockStatus: Record<string, unknown> & { data?: Record<string, unknown> }

function enabledStatus() {
  return {
    register_enabled: true,
    email_verification: true,
    turnstile_check: true,
    turnstile_site_key: 'test-site-key',
    user_agreement_enabled: false,
    privacy_policy_enabled: false,
    oauth_register_enabled: false,
    wechat_login: false,
    data: {},
  }
}

function setToken(token: string) {
  mockToken = token
  notify()
}

async function fillForm(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByPlaceholderText('Enter your username'), 'alice')
  await user.type(
    screen.getByPlaceholderText('name@example.com'),
    'alice@example.com'
  )
  await user.type(
    screen.getByPlaceholderText('Enter password (8–128 characters)'),
    'password123'
  )
  await user.type(
    screen.getByPlaceholderText('Confirm password'),
    'password123'
  )
  await user.type(screen.getByPlaceholderText('Verification code'), '123456')
}

function pendingApi() {
  let resolve!: (value: { success: boolean; message?: string }) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<{ success: boolean; message?: string }>(
    (finish, fail) => {
      resolve = finish
      reject = fail
    }
  )
  return { promise, resolve, reject }
}

beforeEach(() => {
  mockStatus = enabledStatus()
  mockTurnstileEnabled = true
  mockToken = ''
  mockOnExpire = undefined
  listeners.clear()
})

afterEach(() => {
  vi.clearAllMocks()
  listeners.clear()
})

describe('SignUpForm register token consumption', () => {
  it('clears the token and remounts before dispatching a register request with the captured token', async () => {
    setToken('turnstile-token-a')
    mockRegister.mockResolvedValue({ success: true, message: '' })
    render(<SignUpForm />)
    const user = userEvent.setup()
    await fillForm(user)
    await user.click(screen.getByRole('button', { name: 'Create account' }))

    await waitFor(() => expect(mockRegister).toHaveBeenCalledTimes(1))
    expect(mockRegister.mock.calls[0][0]).toMatchObject({
      username: 'alice',
      turnstile: 'turnstile-token-a',
    })
    // Token was cleared before the request so it cannot be reused.
    expect(mockToken).toBe('')
  })

  it('clears the token and remounts when register is rejected, allowing a retry with a new token', async () => {
    setToken('turnstile-token-b')
    mockRegister.mockResolvedValueOnce({ success: false, message: 'bad' })
    render(<SignUpForm />)
    const user = userEvent.setup()
    await fillForm(user)
    await user.click(screen.getByRole('button', { name: 'Create account' }))

    await waitFor(() =>
      expect(mockRegister).toHaveBeenCalledWith(
        expect.objectContaining({ turnstile: 'turnstile-token-b' })
      )
    )
    expect(mockToken).toBe('')

    // The same widget flows to a new token (remount produced a new one).
    setToken('turnstile-token-c')
    await user.click(screen.getByRole('button', { name: 'Create account' }))

    await waitFor(() =>
      expect(mockRegister).toHaveBeenCalledWith(
        expect.objectContaining({ turnstile: 'turnstile-token-c' })
      )
    )
    expect(mockRegister).toHaveBeenCalledTimes(2)
    expect(mockToken).toBe('')
  })

  it('clears the token and remounts when register throws, allowing a retry with a new token', async () => {
    setToken('turnstile-token-d')
    mockRegister.mockRejectedValueOnce(new Error('boom'))
    render(<SignUpForm />)
    const user = userEvent.setup()
    await fillForm(user)
    await user.click(screen.getByRole('button', { name: 'Create account' }))

    await waitFor(() =>
      expect(mockRegister).toHaveBeenCalledWith(
        expect.objectContaining({ turnstile: 'turnstile-token-d' })
      )
    )
    expect(mockToken).toBe('')

    setToken('turnstile-token-e')
    await user.click(screen.getByRole('button', { name: 'Create account' }))

    await waitFor(() =>
      expect(mockRegister).toHaveBeenCalledWith(
        expect.objectContaining({ turnstile: 'turnstile-token-e' })
      )
    )
    expect(mockRegister).toHaveBeenCalledTimes(2)
    expect(mockToken).toBe('')
  })

  it('dispatches register only once when the submit button is double-clicked', async () => {
    setToken('turnstile-token-f')
    const apiResult = pendingApi()
    mockRegister.mockReturnValue(apiResult.promise)
    render(<SignUpForm />)
    const user = userEvent.setup()
    await fillForm(user)
    const submit = screen.getByRole('button', { name: 'Create account' })
    await user.dblClick(submit)

    await waitFor(() => expect(mockRegister).toHaveBeenCalledTimes(1))
    expect(mockToken).toBe('')

    await act(async () => {
      apiResult.resolve({ success: true, message: '' })
      await apiResult.promise
    })
  })

  it('does not dispatch register while a send-code request is in flight (mutual exclusion)', async () => {
    setToken('turnstile-token-g')
    const sendApi = pendingApi()
    mockSendEmailVerification.mockReturnValue(sendApi.promise)
    mockRegister.mockResolvedValue({ success: true, message: '' })
    render(<SignUpForm />)
    const user = userEvent.setup()
    await fillForm(user)

    await user.click(screen.getByRole('button', { name: 'Send code' }))
    expect(mockSendEmailVerification).toHaveBeenCalledTimes(1)

    // While send-code is in flight the token is consumed/cleared, so register
    // must not fire a request.
    await user.click(screen.getByRole('button', { name: 'Create account' }))
    expect(mockRegister).not.toHaveBeenCalled()

    await act(async () => {
      sendApi.resolve({ success: true, message: '' })
      await sendApi.promise
    })
  })

  it('consumes and resets the token on send-code success', async () => {
    setToken('turnstile-token-h')
    mockSendEmailVerification.mockResolvedValue({ success: true, message: '' })
    render(<SignUpForm />)
    const user = userEvent.setup()
    await user.type(
      screen.getByPlaceholderText('name@example.com'),
      'alice@example.com'
    )
    await user.click(screen.getByRole('button', { name: 'Send code' }))

    await waitFor(() =>
      expect(mockSendEmailVerification).toHaveBeenCalledWith(
        'alice@example.com',
        'turnstile-token-h'
      )
    )
    expect(mockToken).toBe('')
  })

  it('works normally when turnstile is disabled', async () => {
    mockTurnstileEnabled = false
    mockRegister.mockResolvedValue({ success: true, message: '' })
    render(<SignUpForm />)
    const user = userEvent.setup()
    await fillForm(user)
    await user.click(screen.getByRole('button', { name: 'Create account' }))

    await waitFor(() =>
      expect(mockRegister).toHaveBeenCalledWith(
        expect.objectContaining({ turnstile: '' })
      )
    )
    expect(mockRegister).toHaveBeenCalledTimes(1)
  })

  it('clears the token on expire/error and disables submit and send-code', async () => {
    setToken('turnstile-token-expire')
    mockRegister.mockResolvedValue({ success: true, message: '' })
    render(<SignUpForm />)
    const user = userEvent.setup()
    await fillForm(user)

    // The Turnstile component received an onExpire callback.
    expect(mockOnExpire).toBeTypeOf('function')

    // With a valid token both actions are ready.
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Create account' })
      ).toBeEnabled()
    )
    expect(screen.getByRole('button', { name: 'Send code' })).toBeEnabled()

    // Simulate Cloudflare expired/error callback.
    await act(async () => {
      mockOnExpire?.()
    })

    // Token is cleared so the widget is no longer ready (no reuse).
    expect(mockToken).toBe('')

    // Submit and send-code are both disabled until a fresh token arrives.
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Create account' })
      ).toBeDisabled()
    )
    expect(screen.getByRole('button', { name: 'Send code' })).toBeDisabled()

    // Clicking the disabled buttons must not dispatch any request.
    await user.click(screen.getByRole('button', { name: 'Create account' }))
    await user.click(screen.getByRole('button', { name: 'Send code' }))
    expect(mockRegister).not.toHaveBeenCalled()
    expect(mockSendEmailVerification).not.toHaveBeenCalled()
  })

  it('allows submit again once a new token arrives after expire', async () => {
    setToken('turnstile-token-expire-2')
    mockRegister.mockResolvedValue({ success: true, message: '' })
    render(<SignUpForm />)
    const user = userEvent.setup()
    await fillForm(user)

    // Expire clears the token.
    await act(async () => {
      mockOnExpire?.()
    })
    expect(mockToken).toBe('')

    // A fresh widget verifies and issues a new token.
    setToken('turnstile-token-fresh')

    // Submit becomes enabled again and registers with the new token.
    const submit = screen.getByRole('button', { name: 'Create account' })
    await waitFor(() => expect(submit).toBeEnabled())
    await user.click(submit)

    await waitFor(() =>
      expect(mockRegister).toHaveBeenCalledWith(
        expect.objectContaining({ turnstile: 'turnstile-token-fresh' })
      )
    )
    expect(mockRegister).toHaveBeenCalledTimes(1)
    expect(mockToken).toBe('')
  })
})

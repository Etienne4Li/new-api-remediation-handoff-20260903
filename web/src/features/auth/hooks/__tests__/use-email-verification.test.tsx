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
import { afterEach, describe, expect, it, vi, type Mock } from 'vitest'

import { sendEmailVerification } from '../../api'

import { useEmailVerification } from '../use-email-verification'

vi.mock('../../api', () => ({
  sendEmailVerification: vi.fn(),
}))

const mockSendEmailVerification = sendEmailVerification as Mock

interface HarnessOptions {
  turnstileToken?: string
  validateTurnstile?: () => boolean
  onTokenConsumed?: (token: string | undefined) => void
}

function Harness({
  options,
  email,
  onSent,
}: {
  options: HarnessOptions
  email: string
  onSent?: (result: boolean) => void
}) {
  const { isSending, sendCode } = useEmailVerification(options)
  return (
    <div>
      <button
        type='button'
        onClick={() => {
          void sendCode(email).then((r) => onSent?.(r))
        }}
      >
        send
      </button>
      <span data-testid='sending'>{String(isSending)}</span>
    </div>
  )
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

afterEach(() => {
  vi.clearAllMocks()
})

describe('useEmailVerification token consumption', () => {
  it('consumes the token then sends the captured token on success', async () => {
    const events: string[] = []
    const apiResult = pendingApi()
    mockSendEmailVerification.mockReturnValue(apiResult.promise)
    const token = 'turnstile-token-1'
    render(
      <Harness
        options={{
          turnstileToken: token,
          validateTurnstile: () => {
            events.push('validate')
            return true
          },
          onTokenConsumed: (captured) => {
            events.push(`consumed:${captured}`)
          },
        }}
        email='user@example.com'
      />
    )
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'send' }))

    expect(events).toEqual(['validate', 'consumed:turnstile-token-1'])
    expect(mockSendEmailVerification).toHaveBeenCalledWith(
      'user@example.com',
      'turnstile-token-1'
    )

    await act(async () => {
      apiResult.resolve({ success: true, message: '' })
      await apiResult.promise
    })

    await waitFor(() =>
      expect(mockSendEmailVerification).toHaveBeenCalledTimes(1)
    )
  })

  it('consumes the token and resets even when the business response rejects', async () => {
    const events: string[] = []
    mockSendEmailVerification.mockResolvedValue({
      success: false,
      message: 'rate limited',
    })
    render(
      <Harness
        options={{
          turnstileToken: 'turnstile-token-2',
          onTokenConsumed: () => events.push('consumed'),
        }}
        email='user@example.com'
      />
    )
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'send' }))

    await waitFor(() =>
      expect(mockSendEmailVerification).toHaveBeenCalledTimes(1)
    )
    expect(events).toEqual(['consumed'])
    expect(mockSendEmailVerification).toHaveBeenCalledWith(
      'user@example.com',
      'turnstile-token-2'
    )
  })

  it('consumes the token and resets even when the request throws (network failure)', async () => {
    const events: string[] = []
    mockSendEmailVerification.mockRejectedValue(new Error('network down'))
    render(
      <Harness
        options={{
          turnstileToken: 'turnstile-token-3',
          onTokenConsumed: () => events.push('consumed'),
        }}
        email='user@example.com'
      />
    )
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'send' }))

    await waitFor(() =>
      expect(mockSendEmailVerification).toHaveBeenCalledTimes(1)
    )
    expect(events).toEqual(['consumed'])
  })

  it('does not consume the token when local validation fails (empty email)', async () => {
    const events: string[] = []
    render(
      <Harness
        options={{
          turnstileToken: 'turnstile-token-4',
          onTokenConsumed: () => events.push('consumed'),
        }}
        email=''
      />
    )
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'send' }))

    expect(mockSendEmailVerification).not.toHaveBeenCalled()
    expect(events).toEqual([])
  })

  it('does not consume the token when turnstile validation fails', async () => {
    const events: string[] = []
    render(
      <Harness
        options={{
          turnstileToken: 'turnstile-token-5',
          validateTurnstile: () => false,
          onTokenConsumed: () => events.push('consumed'),
        }}
        email='user@example.com'
      />
    )
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'send' }))

    expect(mockSendEmailVerification).not.toHaveBeenCalled()
    expect(events).toEqual([])
  })

  it('sends only once when clicked repeatedly while a request is in flight', async () => {
    const events: string[] = []
    const apiResult = pendingApi()
    mockSendEmailVerification.mockReturnValue(apiResult.promise)
    render(
      <Harness
        options={{
          turnstileToken: 'turnstile-token-6',
          onTokenConsumed: () => events.push('consumed'),
        }}
        email='user@example.com'
      />
    )
    const user = userEvent.setup()
    const button = screen.getByRole('button', { name: 'send' })

    await user.dblClick(button)

    expect(mockSendEmailVerification).toHaveBeenCalledTimes(1)
    expect(events).toEqual(['consumed'])

    await act(async () => {
      apiResult.resolve({ success: true, message: '' })
      await apiResult.promise
    })
  })

  it('works normally when turnstile is disabled (no token, no validation)', async () => {
    mockSendEmailVerification.mockResolvedValue({ success: true, message: '' })
    const events: string[] = []
    render(
      <Harness
        options={{
          onTokenConsumed: (token) => events.push(`consumed:${token}`),
        }}
        email='user@example.com'
      />
    )
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'send' }))

    await waitFor(() =>
      expect(mockSendEmailVerification).toHaveBeenCalledTimes(1)
    )
    expect(mockSendEmailVerification).toHaveBeenCalledWith(
      'user@example.com',
      undefined
    )
    expect(events).toEqual(['consumed:undefined'])
  })
})

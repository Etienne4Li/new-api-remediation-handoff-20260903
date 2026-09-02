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
import { beforeEach, describe, expect, test, vi } from 'vitest'

import { PasskeyCard } from '../passkey-card'

const passkeyMocks = vi.hoisted(() => ({
  register: vi.fn(),
  remove: vi.fn(),
  usePasskeyManagement: vi.fn(),
}))
const verificationApiMocks = vi.hoisted(() => ({
  checkVerificationMethods: vi.fn(),
  verify: vi.fn(),
}))

vi.mock('@/features/auth/passkey', () => ({
  usePasskeyManagement: passkeyMocks.usePasskeyManagement,
}))
vi.mock('@/features/auth/secure-verification/api', () => verificationApiMocks)

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((promiseResolve) => {
    resolve = promiseResolve
  })
  return { promise, resolve }
}

beforeEach(() => {
  passkeyMocks.usePasskeyManagement.mockReturnValue({
    status: { enabled: false },
    loading: false,
    registering: false,
    removing: false,
    supported: true,
    enabled: false,
    lastUsed: null,
    register: passkeyMocks.register,
    remove: passkeyMocks.remove,
  })
})

describe('PasskeyCard verification methods', () => {
  test('opens registration verification from the same method snapshot it checked', async () => {
    verificationApiMocks.checkVerificationMethods
      .mockResolvedValueOnce({
        has2FA: false,
        hasPasskey: true,
        passkeySupported: true,
      })
      .mockResolvedValueOnce({
        has2FA: true,
        hasPasskey: false,
        passkeySupported: true,
      })

    render(<PasskeyCard loading={false} />)
    await waitFor(() =>
      expect(
        verificationApiMocks.checkVerificationMethods
      ).toHaveBeenCalledOnce()
    )

    await userEvent.click(
      screen.getByRole('button', { name: 'Enable Passkey' })
    )

    expect(await screen.findByRole('dialog')).toBeInTheDocument()
    expect(
      screen.getByRole('heading', { name: 'Security verification' })
    ).toBeInTheDocument()
    expect(
      screen.getByRole('tab', { name: 'Authenticator code' })
    ).toBeInTheDocument()
    expect(screen.queryByRole('tab', { name: 'Passkey' })).toBeNull()
    expect(verificationApiMocks.checkVerificationMethods).toHaveBeenCalledTimes(
      2
    )
  })

  test('ignores an older registration click when its method query resolves last', async () => {
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
    verificationApiMocks.checkVerificationMethods
      .mockResolvedValueOnce({
        has2FA: false,
        hasPasskey: true,
        passkeySupported: true,
      })
      .mockReturnValueOnce(olderMethods.promise)
      .mockReturnValueOnce(newerMethods.promise)

    render(<PasskeyCard loading={false} />)
    await waitFor(() =>
      expect(
        verificationApiMocks.checkVerificationMethods
      ).toHaveBeenCalledOnce()
    )

    const registerButton = screen.getByRole('button', {
      name: 'Enable Passkey',
    })
    await userEvent.click(registerButton)
    await userEvent.click(registerButton)
    expect(verificationApiMocks.checkVerificationMethods).toHaveBeenCalledTimes(
      3
    )

    await act(async () => {
      newerMethods.resolve({
        has2FA: true,
        hasPasskey: false,
        passkeySupported: true,
      })
      await Promise.resolve()
    })
    expect(await screen.findByRole('dialog')).toBeInTheDocument()

    await act(async () => {
      olderMethods.resolve({
        has2FA: false,
        hasPasskey: false,
        passkeySupported: true,
      })
      await Promise.resolve()
    })

    expect(passkeyMocks.register).not.toHaveBeenCalled()
    expect(
      screen.getByRole('heading', { name: 'Security verification' })
    ).toBeInTheDocument()
  })

  test('ignores an older removal click and uses the newest method snapshot', async () => {
    passkeyMocks.usePasskeyManagement.mockReturnValue({
      status: { enabled: true },
      loading: false,
      registering: false,
      removing: false,
      supported: true,
      enabled: true,
      lastUsed: null,
      register: passkeyMocks.register,
      remove: passkeyMocks.remove,
    })
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
    verificationApiMocks.checkVerificationMethods
      .mockResolvedValueOnce({
        has2FA: false,
        hasPasskey: true,
        passkeySupported: true,
      })
      .mockReturnValueOnce(olderMethods.promise)
      .mockReturnValueOnce(newerMethods.promise)

    render(<PasskeyCard loading={false} />)
    await waitFor(() =>
      expect(
        verificationApiMocks.checkVerificationMethods
      ).toHaveBeenCalledOnce()
    )

    await userEvent.click(
      screen.getByRole('button', { name: 'Remove Passkey' })
    )
    const confirmButton = await screen.findByRole('button', { name: 'Remove' })
    await userEvent.click(confirmButton)
    await userEvent.click(confirmButton)
    expect(verificationApiMocks.checkVerificationMethods).toHaveBeenCalledTimes(
      3
    )

    await act(async () => {
      newerMethods.resolve({
        has2FA: true,
        hasPasskey: true,
        passkeySupported: true,
      })
      await Promise.resolve()
    })
    expect(
      await screen.findByRole('heading', { name: 'Security verification' })
    ).toBeInTheDocument()
    expect(
      screen.getByRole('tab', { name: 'Authenticator code' })
    ).toBeInTheDocument()
    expect(screen.queryByRole('tab', { name: 'Passkey' })).toBeNull()

    await act(async () => {
      olderMethods.resolve({
        has2FA: false,
        hasPasskey: false,
        passkeySupported: true,
      })
      await Promise.resolve()
    })

    expect(passkeyMocks.remove).not.toHaveBeenCalled()
    expect(
      screen.getByRole('heading', { name: 'Security verification' })
    ).toBeInTheDocument()
  })
})

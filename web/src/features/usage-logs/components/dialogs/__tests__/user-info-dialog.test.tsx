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
import { act, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, test, vi } from 'vitest'

import { UserInfoDialog } from '../user-info-dialog'

const { getUserInfoMock } = vi.hoisted(() => ({
  getUserInfoMock: vi.fn(),
}))

vi.mock('@/features/usage-logs/api', () => ({
  getUserInfo: getUserInfoMock,
}))

function userInfo(username: string) {
  return {
    id: username === 'first-user' ? 1 : 2,
    username,
    quota: 100,
    used_quota: 10,
    request_count: 2,
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
  getUserInfoMock.mockReset()
})

describe('UserInfoDialog request lifecycle', () => {
  test('keeps the selected user when an older request resolves last', async () => {
    const firstRequest = deferred<{
      success: boolean
      data: ReturnType<typeof userInfo>
    }>()
    const secondRequest = deferred<{
      success: boolean
      data: ReturnType<typeof userInfo>
    }>()
    getUserInfoMock
      .mockReturnValueOnce(firstRequest.promise)
      .mockReturnValueOnce(secondRequest.promise)

    const rendered = render(
      <UserInfoDialog open userId={1} onOpenChange={() => undefined} />
    )

    await waitFor(() => expect(getUserInfoMock).toHaveBeenCalledTimes(1))

    rendered.rerender(
      <UserInfoDialog open userId={2} onOpenChange={() => undefined} />
    )

    await waitFor(() => expect(getUserInfoMock).toHaveBeenCalledTimes(2))
    expect(getUserInfoMock.mock.calls).toEqual([[1], [2]])

    await act(async () => {
      secondRequest.resolve({ success: true, data: userInfo('second-user') })
      await secondRequest.promise
    })
    await waitFor(() => expect(screen.getByText('second-user')).toBeVisible())

    await act(async () => {
      firstRequest.resolve({ success: true, data: userInfo('first-user') })
      await firstRequest.promise
    })

    expect(screen.getByText('second-user')).toBeVisible()
    expect(screen.queryByText('first-user')).not.toBeInTheDocument()
  })

  test('ignores a request that resolves after the dialog closes', async () => {
    const request = deferred<{
      success: boolean
      data: ReturnType<typeof userInfo>
    }>()
    getUserInfoMock.mockReturnValueOnce(request.promise)

    const rendered = render(
      <UserInfoDialog open userId={1} onOpenChange={() => undefined} />
    )
    rendered.rerender(
      <UserInfoDialog
        open={false}
        userId={null}
        onOpenChange={() => undefined}
      />
    )

    await act(async () => {
      request.resolve({ success: true, data: userInfo('closed-user') })
      await request.promise
    })

    expect(screen.queryByText('closed-user')).not.toBeInTheDocument()
  })
})

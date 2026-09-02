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
import { Toaster, toast } from 'sonner'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { CacheStatsDialog } from '../cache-stats-dialog'

const { getMock } = vi.hoisted(() => ({
  getMock: vi.fn(),
}))

vi.mock('@/lib/api', () => ({
  api: { get: getMock },
}))

const target = {
  rule_name: 'cached-rule',
  using_group: 'default',
  key_hint: 'sk-...1234',
  key_fp: 'fingerprint-1234',
}

type Deferred<T> = {
  promise: Promise<T>
  reject: (reason?: unknown) => void
}

function deferred<T>(): Deferred<T> {
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((_resolve, promiseReject) => {
    reject = promiseReject
  })
  return { promise, reject }
}

function dialogTree(open: boolean) {
  return (
    <>
      <CacheStatsDialog
        open={open}
        onOpenChange={() => undefined}
        target={target}
      />
      <Toaster duration={60_000} />
    </>
  )
}

afterEach(() => {
  toast.dismiss()
})

describe('cache stats request lifecycle', () => {
  test('shows an error and finishes loading when the current request rejects', async () => {
    getMock.mockRejectedValueOnce(new Error('network failure'))

    render(dialogTree(true))

    expect(screen.getByText('Loading...')).toBeInTheDocument()
    expect(await screen.findByText('Request failed')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryByText('Loading...')).not.toBeInTheDocument()
    })
  })

  test('does not report an obsolete request failure after the dialog closes', async () => {
    const request = deferred<{ data: unknown }>()
    const errorToast = vi.spyOn(toast, 'error')
    getMock.mockReturnValueOnce(request.promise)
    const rendered = render(dialogTree(true))
    await waitFor(() => expect(getMock).toHaveBeenCalledOnce())

    rendered.rerender(dialogTree(false))
    await act(async () => {
      request.reject(new Error('obsolete network failure'))
      await request.promise.catch(() => undefined)
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(errorToast).not.toHaveBeenCalled()
  })
})

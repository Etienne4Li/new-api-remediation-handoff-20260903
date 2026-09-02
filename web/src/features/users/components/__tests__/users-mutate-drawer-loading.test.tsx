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
import {
  render,
  screen,
  waitFor,
  type RenderResult,
} from '@testing-library/react'
import { afterEach, describe, expect, test } from 'vitest'

import type { User } from '../../types'

const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { Toaster, toast } = await import('sonner')
const { api } = await import('@/lib/api')
const { UsersProvider } = await import('../users-provider')
const { UsersMutateDrawer } = await import('../users-mutate-drawer')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: { en: { translation: {} } },
})

type ApiMethod = (url: string) => Promise<{ data: unknown }>
type MockableApi = { get: ApiMethod }
type Deferred<T> = {
  promise: Promise<T>
  resolve: (value: T) => void
  reject: (reason?: unknown) => void
}

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
let queryClient: InstanceType<typeof QueryClient> | null = null
let rendered: RenderResult | null = null

function user(id: number, displayName: string): User {
  return {
    id,
    username: `user-${id}`,
    display_name: displayName,
    quota: 500_000,
    used_quota: 0,
    request_count: 0,
    group: 'default',
    status: 1,
    role: 1,
  }
}

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve
    reject = promiseReject
  })
  return { promise, resolve, reject }
}

function drawerTree(currentRow: User) {
  if (!queryClient) throw new Error('Expected query client')
  return (
    <QueryClientProvider client={queryClient}>
      <I18nextProvider i18n={i18n}>
        <UsersProvider>
          <UsersMutateDrawer
            open
            currentRow={currentRow}
            onOpenChange={() => undefined}
          />
        </UsersProvider>
        <Toaster duration={60_000} />
      </I18nextProvider>
    </QueryClientProvider>
  )
}

function renderDrawer(currentRow: User): void {
  queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const freshAt = Date.now() + 60_000
  queryClient.setQueryData(
    ['groups'],
    { success: true, data: ['default'] },
    { updatedAt: freshAt }
  )
  queryClient.setQueryData(
    ['admin-permission-catalog'],
    { resources: [], roles: [] },
    { updatedAt: freshAt }
  )
  rendered = render(drawerTree(currentRow))
}

afterEach(() => {
  apiClient.get = originalGet
  queryClient?.clear()
  queryClient = null
  rendered = null
  toast.dismiss()
})

describe('user mutate drawer loading', () => {
  test('reports a rejected user request instead of leaving an unhandled promise', async () => {
    apiClient.get = async (url) => {
      if (url === '/api/user/1') throw new Error('network failure')
      throw new Error(`Unexpected GET ${url}`)
    }

    renderDrawer(user(1, 'Cached user'))

    expect(await screen.findByText('Failed to load users')).toBeInTheDocument()
  })

  test('ignores an older response after the selected user changes', async () => {
    const first = deferred<{ data: unknown }>()
    apiClient.get = async (url) => {
      if (url === '/api/user/1') return first.promise
      if (url === '/api/user/2') {
        return {
          data: { success: true, data: user(2, 'Current user') },
        }
      }
      throw new Error(`Unexpected GET ${url}`)
    }

    renderDrawer(user(1, 'Cached first user'))
    rendered?.rerender(drawerTree(user(2, 'Cached current user')))

    const displayName = screen.getByPlaceholderText('Enter display name')
    await waitFor(() => expect(displayName).toHaveValue('Current user'))

    first.resolve({
      data: { success: true, data: user(1, 'Stale first user') },
    })
    await waitFor(() => expect(displayName).toHaveValue('Current user'))
  })
})

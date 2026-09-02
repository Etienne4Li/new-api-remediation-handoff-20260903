/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.
*/
import { afterEach, describe, expect, test } from 'vitest'

import { api } from '@/lib/api'

import { login } from '../api'

describe('authentication API', () => {
  const originalPost = api.post

  afterEach(() => {
    api.post = originalPost
  })

  test('passes Turnstile tokens as encoded query params', async () => {
    let requestedUrl = ''
    let requestedConfig: Record<string, unknown> | undefined
    api.post = (async (
      url: string,
      _data?: unknown,
      config?: Record<string, unknown>
    ) => {
      requestedUrl = url
      requestedConfig = config
      return {
        data: {
          success: true,
          message: '',
          data: undefined,
        },
      }
    }) as unknown as typeof api.post

    await login({
      username: 'alice',
      password: 'secret',
      turnstile: 'token+with&reserved=chars',
    })

    expect(requestedUrl).toBe('/api/user/login')
    expect(requestedConfig).toMatchObject({
      params: { turnstile: 'token+with&reserved=chars' },
      skipAuthRefresh: true,
    })
  })
})

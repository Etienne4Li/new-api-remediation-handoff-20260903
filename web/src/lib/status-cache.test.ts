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
import { beforeEach, describe, expect, test } from 'vitest'

import {
  clearCachedStatus,
  readCachedStatus,
  sanitizeStatusForCache,
  writeCachedStatus,
} from './status-cache'

describe('status cache security', () => {
  beforeEach(() => {
    window.localStorage.clear()
  })

  test('removes chat templates from a status snapshot', () => {
    const status = {
      system_name: 'Gateway',
      chats: [{ Client: 'client://open?key={key}' }],
      data: {
        Chats: [{ Other: 'https://example.test/?key=secret' }],
      },
    }

    expect(sanitizeStatusForCache(status)).toEqual({
      system_name: 'Gateway',
      data: {},
    })

    writeCachedStatus(status)
    const stored = window.localStorage.getItem('status')
    expect(stored).not.toContain('chats')
    expect(stored).not.toContain('key')
    expect(readCachedStatus()).toEqual({ system_name: 'Gateway', data: {} })
  })

  test('cleans an old cache on read', () => {
    window.localStorage.setItem(
      'status',
      JSON.stringify({
        system_name: 'Legacy',
        Chats: [{ Client: 'client://open?key={key}' }],
      })
    )

    expect(readCachedStatus()).toEqual({ system_name: 'Legacy' })
    expect(window.localStorage.getItem('status')).toBe(
      JSON.stringify({ system_name: 'Legacy' })
    )
  })

  test('removes whitespace-variant chat fields', () => {
    const status = {
      ' Chats ': [{ Client: 'client://open?key=secret' }],
      '\tCHATS\n': [{ Other: 'client://open?token=secret' }],
      keep: true,
    }
    expect(sanitizeStatusForCache(status)).toEqual({ keep: true })
  })

  test('removes malformed or non-object cache values', () => {
    window.localStorage.setItem('status', 'not-json')
    expect(readCachedStatus()).toBeUndefined()
    expect(window.localStorage.getItem('status')).toBeNull()

    window.localStorage.setItem('status', JSON.stringify(null))
    expect(readCachedStatus()).toBeUndefined()
    clearCachedStatus()
    expect(window.localStorage.getItem('status')).toBeNull()
  })
})

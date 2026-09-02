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
import { describe, expect, test } from 'vitest'

import type { SystemStatus } from '@/features/auth/types'

import {
  getHomeApiEndpoint,
  getHomeDocsUrl,
  getHomeEntryPoint,
  isExternalHomeLink,
  sanitizeHomeLink,
} from '../home-entry'

describe('home entry point', () => {
  test('takes authenticated users directly to the dashboard', () => {
    expect(getHomeEntryPoint(true, null)).toEqual({
      route: '/dashboard',
      labelKey: 'Go to Dashboard',
    })
  })

  test('offers password registration when it is available', () => {
    const status: SystemStatus = {
      register_enabled: true,
      password_register_enabled: true,
      self_use_mode_enabled: false,
    }

    expect(getHomeEntryPoint(false, status)).toEqual({
      route: '/sign-up',
      labelKey: 'Get Started',
    })
  })

  test('routes OAuth-only registration through the sign-in page', () => {
    const status: SystemStatus = {
      register_enabled: true,
      password_register_enabled: false,
      self_use_mode_enabled: false,
    }

    expect(getHomeEntryPoint(false, status)).toEqual({
      route: '/sign-in',
      labelKey: 'Get Started',
    })
  })

  test('falls back to sign-in when registration status is unavailable', () => {
    expect(getHomeEntryPoint(false, null)).toEqual({
      route: '/sign-in',
      labelKey: 'Sign in',
    })
  })
})

describe('home API endpoint', () => {
  test('prefers the configured server address and removes a trailing v1', () => {
    expect(
      getHomeApiEndpoint(
        { server_address: 'https://api.lietio.com/v1/' },
        'http://localhost:3000'
      )
    ).toBe('https://api.lietio.com')
  })

  test('falls back to the first published API endpoint', () => {
    expect(
      getHomeApiEndpoint(
        {
          api_info: JSON.stringify([
            { url: 'https://edge.lietio.com/' },
            { url: 'https://backup.lietio.com/' },
          ]),
        },
        'http://localhost:3000'
      )
    ).toBe('https://edge.lietio.com')
  })

  test('uses the current origin when no endpoint has been configured', () => {
    expect(getHomeApiEndpoint(null, 'http://127.0.0.1:4173')).toBe(
      'http://127.0.0.1:4173'
    )
  })
})

describe('home links', () => {
  const currentOrigin = 'https://gateway.example.com'

  test('keeps same-origin paths with their query and hash', () => {
    expect(sanitizeHomeLink('/docs?section=auth#tokens', currentOrigin)).toBe(
      '/docs?section=auth#tokens'
    )
    expect(isExternalHomeLink('/docs', currentOrigin)).toBe(false)
  })

  test('normalizes external HTTP links and identifies them as external', () => {
    const docsUrl = getHomeDocsUrl(
      { docs_link: 'https://docs.example.com/start' },
      currentOrigin
    )

    expect(docsUrl).toBe('https://docs.example.com/start')
    expect(isExternalHomeLink(docsUrl, currentOrigin)).toBe(true)
  })

  test.each([undefined, '', '   ', 'javascript:alert(1)'])(
    'falls back to the New API documentation for invalid configured value %s',
    (value) => {
      expect(getHomeDocsUrl({ docs_link: value }, currentOrigin)).toBe(
        'https://docs.newapi.pro'
      )
    }
  )

  test('reads the configured documentation path from a nested status payload', () => {
    expect(
      getHomeDocsUrl(
        { data: { docs_link: '/docs/getting-started' } },
        currentOrigin
      )
    ).toBe('/docs/getting-started')
  })

  test.each([
    'javascript:alert(1)',
    'data:text/html,unsafe',
    '//docs.example.com',
    '/\\docs.example.com',
  ])('rejects unsafe configured link %s', (value) => {
    expect(sanitizeHomeLink(value, currentOrigin)).toBe('')
  })
})

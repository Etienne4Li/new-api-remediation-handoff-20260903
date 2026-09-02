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
import type { SystemStatus } from '@/features/auth/types'

export type HomeEntryPoint = {
  route: '/dashboard' | '/sign-in' | '/sign-up'
  labelKey: 'Get Started' | 'Go to Dashboard' | 'Sign in'
}

const DEFAULT_HOME_DOCS_URL = 'https://docs.newapi.pro'

/**
 * Accept only web URLs or same-origin relative paths from admin configuration.
 * Relative paths are returned unchanged so router links remain local; absolute
 * HTTP(S) URLs are normalized by URL for predictable external navigation.
 */
export function sanitizeHomeLink(
  value: unknown,
  currentOrigin: string
): string {
  if (typeof value !== 'string') return ''

  const candidate = value.trim()
  if (!candidate || candidate.startsWith('//')) return ''

  if (candidate.startsWith('/')) {
    try {
      const parsed = new URL(candidate, currentOrigin)
      if (parsed.origin !== currentOrigin) return ''
      return `${parsed.pathname}${parsed.search}${parsed.hash}`
    } catch {
      return ''
    }
  }

  try {
    const parsed = new URL(candidate)
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
      return ''
    }
    return parsed.href
  } catch {
    return ''
  }
}

export function isExternalHomeLink(value: string, currentOrigin: string) {
  try {
    return new URL(value, currentOrigin).origin !== currentOrigin
  } catch {
    return false
  }
}

function readStatusValue(status: SystemStatus | null, key: string): unknown {
  if (!status) return undefined
  if (key in status) return status[key]
  return status.data?.[key]
}

export function getHomeDocsUrl(
  status: SystemStatus | null,
  currentOrigin: string
): string {
  return (
    sanitizeHomeLink(readStatusValue(status, 'docs_link'), currentOrigin) ||
    DEFAULT_HOME_DOCS_URL
  )
}

function readApiInfoEndpoint(value: unknown): string {
  let candidate = value

  if (typeof candidate === 'string') {
    const trimmed = candidate.trim()
    if (!trimmed) return ''
    if (!trimmed.startsWith('[')) return trimmed

    try {
      candidate = JSON.parse(trimmed) as unknown
    } catch {
      return ''
    }
  }

  if (!Array.isArray(candidate)) return ''

  for (const item of candidate) {
    if (!item || typeof item !== 'object') continue
    const url = (item as Record<string, unknown>).url
    if (typeof url === 'string' && url.trim()) return url.trim()
  }

  return ''
}

export function getHomeApiEndpoint(
  status: SystemStatus | null,
  fallbackOrigin: string
): string {
  const configuredAddress = [
    readStatusValue(status, 'server_address'),
    readStatusValue(status, 'serverAddress'),
  ].find((value) => typeof value === 'string' && value.trim())

  const apiInfoAddress = readApiInfoEndpoint(
    readStatusValue(status, 'api_info')
  )
  const endpoint =
    (typeof configuredAddress === 'string' ? configuredAddress : '') ||
    apiInfoAddress ||
    fallbackOrigin

  return endpoint.trim().replace(/\/+$/, '').replace(/\/v1$/i, '')
}

export function getHomeEntryPoint(
  isAuthenticated: boolean,
  status: SystemStatus | null
): HomeEntryPoint {
  if (isAuthenticated) {
    return { route: '/dashboard', labelKey: 'Go to Dashboard' }
  }

  if (
    status &&
    status.register_enabled !== false &&
    !status.self_use_mode_enabled
  ) {
    if (status.password_register_enabled !== false) {
      return { route: '/sign-up', labelKey: 'Get Started' }
    }

    return { route: '/sign-in', labelKey: 'Get Started' }
  }

  return { route: '/sign-in', labelKey: 'Sign in' }
}

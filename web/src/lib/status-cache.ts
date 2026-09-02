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

const STATUS_STORAGE_KEY = 'status'
const MAX_STATUS_CACHE_DEPTH = 16

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function stripChatFields(value: unknown, depth = 0): unknown {
  if (depth > MAX_STATUS_CACHE_DEPTH) return undefined
  if (Array.isArray(value)) {
    let changed = false
    const result = value.map((child) => {
      const cleaned = stripChatFields(child, depth + 1)
      if (cleaned !== child) changed = true
      return cleaned
    })
    return changed ? result : value
  }
  if (!isRecord(value)) return value

  let changed = false
  const result: Record<string, unknown> = {}
  Object.entries(value).forEach(([key, child]) => {
    if (key.trim().toLowerCase() === 'chats') {
      changed = true
      return
    }
    const cleaned = stripChatFields(child, depth + 1)
    if (cleaned !== child) changed = true
    if (cleaned !== undefined) result[key] = cleaned
  })

  return changed ? result : value
}

/** Remove chat templates before a status payload reaches persistent storage. */
export function sanitizeStatusForCache(
  status: unknown
): Record<string, unknown> | null {
  if (!isRecord(status)) return null
  const cleaned = stripChatFields(status)
  return isRecord(cleaned) ? cleaned : null
}

export function readCachedStatus<T extends SystemStatus = SystemStatus>():
  | T
  | undefined {
  if (typeof window === 'undefined') return undefined
  try {
    const raw = window.localStorage.getItem(STATUS_STORAGE_KEY)
    if (!raw) return undefined
    const parsed: unknown = JSON.parse(raw)
    const cleaned = sanitizeStatusForCache(parsed)
    if (!cleaned) {
      window.localStorage.removeItem(STATUS_STORAGE_KEY)
      return undefined
    }
    // Rewrite legacy caches that still contain `chats`/`Chats` so a later
    // consumer cannot accidentally recover a credential-bearing template.
    if (cleaned !== parsed) {
      window.localStorage.setItem(STATUS_STORAGE_KEY, JSON.stringify(cleaned))
    }
    return cleaned as T
  } catch {
    try {
      window.localStorage.removeItem(STATUS_STORAGE_KEY)
    } catch {
      /* Storage may be disabled by the browser. */
    }
    return undefined
  }
}

export function writeCachedStatus(status: unknown): void {
  if (typeof window === 'undefined') return
  try {
    const cleaned = sanitizeStatusForCache(status)
    if (!cleaned) {
      window.localStorage.removeItem(STATUS_STORAGE_KEY)
      return
    }
    window.localStorage.setItem(STATUS_STORAGE_KEY, JSON.stringify(cleaned))
  } catch {
    /* Storage may be disabled or full; the network response remains usable. */
  }
}

export function clearCachedStatus(): void {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.removeItem(STATUS_STORAGE_KEY)
  } catch {
    /* empty */
  }
}

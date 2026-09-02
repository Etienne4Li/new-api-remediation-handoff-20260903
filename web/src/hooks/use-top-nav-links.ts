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
import { useMemo } from 'react'

import { useStatus } from '@/hooks/use-status'
import { parseHeaderNavModulesFromStatus } from '@/lib/nav-modules'
import { useAuthStore } from '@/stores/auth-store'

export type TopNavLink = {
  title: string
  href: string
  isActive?: boolean
  disabled?: boolean
  requiresAuth?: boolean
  external?: boolean
}

export function buildTopNavLinks(
  status: Record<string, unknown> | null,
  isAuthed: boolean
): TopNavLink[] {
  const modules = parseHeaderNavModulesFromStatus(status)
  const docsLink =
    typeof status?.docs_link === 'string' ? status.docs_link.trim() : ''
  const links: TopNavLink[] = []

  if (modules.home !== false) {
    links.push({ title: 'Home', href: '/' })
  }

  if (modules.console !== false) {
    links.push({ title: 'Manage', href: '/dashboard' })
  }

  const pricing = modules.pricing
  if (pricing.enabled) {
    links.push({
      title: 'Models',
      href: '/pricing',
      requiresAuth: pricing.requireAuth && !isAuthed,
    })
  }

  const rankings = modules.rankings
  if (rankings.enabled) {
    links.push({
      title: 'Hot Topics',
      href: '/rankings',
      requiresAuth: rankings.requireAuth && !isAuthed,
    })
  }

  // Do not expose a guaranteed 404 when docs are enabled without a URL.
  if (modules.docs !== false && docsLink) {
    links.push({ title: 'Docs', href: docsLink, external: true })
  }

  if (modules.about !== false) {
    links.push({ title: 'About', href: '/about' })
  }

  return links
}

/**
 * Generate top navigation links based on HeaderNavModules configuration from backend /api/status
 * Backend format example (stringified JSON):
 * {
 *   home: true,
 *   console: true,
 *   pricing: { enabled: true, requireAuth: false },
 *   rankings: { enabled: true, requireAuth: false },
 *   docs: true,
 *   about: true
 * }
 */
export function useTopNavLinks(): TopNavLink[] {
  const { status, error } = useStatus()
  const { auth } = useAuthStore()
  const links = useMemo(
    () =>
      buildTopNavLinks(status as Record<string, unknown> | null, !!auth?.user),
    [auth?.user, status]
  )

  // Let each header use its provided static links when there is neither a
  // live status response nor cached status to build from.
  return error && !status ? [] : links
}

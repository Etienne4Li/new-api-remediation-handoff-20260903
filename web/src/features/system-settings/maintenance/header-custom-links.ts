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
  MAX_HEADER_NAV_CUSTOM_LINKS,
  parseHeaderNavCustomLinks,
  type HeaderNavCustomLink,
} from '@/hooks/use-top-nav-links'

/**
 * Admin-facing text form for the `HeaderNavCustomLinks` option:
 * one link per line, `Title | URL [| auth]`. `auth` marks a link that
 * prompts the visitor to sign in first.
 */
export function linksToLines(raw: string): string {
  return parseHeaderNavCustomLinks(raw)
    .map((link) =>
      [link.title, link.href, link.requiresAuth ? 'auth' : '']
        .filter(Boolean)
        .join(' | ')
    )
    .join('\n')
}

export function linesToLinks(text: string): {
  links: HeaderNavCustomLink[]
  invalid: string[]
} {
  const links: HeaderNavCustomLink[] = []
  const invalid: string[] = []
  for (const rawLine of text.split(/\r?\n/)) {
    const line = rawLine.trim()
    if (!line) continue
    const [title = '', href = '', flag = ''] = line
      .split('|')
      .map((part) => part.trim())
    const candidate: HeaderNavCustomLink = {
      title,
      href,
      requireAuth: flag.toLowerCase() === 'auth',
    }
    if (parseHeaderNavCustomLinks([candidate]).length === 0) {
      invalid.push(line)
      continue
    }
    links.push(candidate)
  }
  return { links: links.slice(0, MAX_HEADER_NAV_CUSTOM_LINKS), invalid }
}

export function serializeHeaderNavCustomLinks(
  links: HeaderNavCustomLink[]
): string {
  if (links.length === 0) return ''
  return JSON.stringify(
    links.map((link) => ({
      title: link.title,
      href: link.href,
      ...(link.requireAuth ? { requireAuth: true } : {}),
    }))
  )
}

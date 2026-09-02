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
import { Link, useRouterState } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { useStatus } from '@/hooks/use-status'
import { useTopNavLinks } from '@/hooks/use-top-nav-links'
import { cn } from '@/lib/utils'

import { defaultTopNavLinks } from '../config/top-nav.config'
import { isNavPathActive } from '../lib/url-utils'
import type { TopNavLink } from '../types'

interface PublicNavigationProps {
  /**
   * Custom navigation links
   * If not provided, will use dynamic links from backend or defaults
   */
  links?: TopNavLink[]
  /**
   * Additional className
   */
  className?: string
}

/**
 * Neutral public navigation used by the public shell.
 */
export function PublicNavigation({
  links: providedLinks,
  className,
}: PublicNavigationProps = {}) {
  const { t } = useTranslation()
  // Use the same logic as AppHeader: prioritize dynamic links from backend
  const dynamicLinks = useTopNavLinks()
  const { status: statusSnapshot } = useStatus()
  const defaultLinks = providedLinks || defaultTopNavLinks
  const pathname = useRouterState({
    select: (state) => state.location.pathname,
  })
  const links =
    dynamicLinks.length > 0 || statusSnapshot !== null
      ? dynamicLinks
      : defaultLinks

  return (
    <nav className={cn('hidden items-center gap-0.5 md:flex', className)}>
      {links.map((link) => {
        const isActive =
          link.isActive ??
          (!link.external && isNavPathActive(pathname, link.href))

        // Handle external links
        if (link.external) {
          return (
            <a
              key={`${link.title}-${link.href}`}
              href={link.href}
              target='_blank'
              rel='noopener noreferrer'
              aria-current={isActive ? 'page' : undefined}
              className={cn(
                'text-muted-foreground hover:bg-foreground/[0.045] hover:text-foreground focus-visible:ring-foreground/20 inline-flex h-8 w-max items-center justify-center rounded-none bg-transparent px-2.5 py-0 text-sm font-medium transition-colors focus-visible:ring-2 focus-visible:outline-none',
                isActive &&
                  'bg-foreground/[0.065] text-foreground font-semibold',
                link.disabled && 'pointer-events-none opacity-50'
              )}
            >
              {t(link.title)}
            </a>
          )
        }
        // Handle internal links
        return (
          <Link
            key={`${link.title}-${link.href}`}
            to={link.href}
            aria-current={isActive ? 'page' : undefined}
            className={cn(
              'text-muted-foreground hover:bg-foreground/[0.045] hover:text-foreground focus-visible:ring-foreground/20 inline-flex h-8 w-max items-center justify-center rounded-none bg-transparent px-2.5 py-0 text-sm font-medium transition-colors focus-visible:ring-2 focus-visible:outline-none',
              isActive && 'bg-foreground/[0.065] text-foreground font-semibold',
              link.disabled && 'pointer-events-none opacity-50'
            )}
          >
            {t(link.title)}
          </Link>
        )
      })}
    </nav>
  )
}

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
import { Link, useLocation } from '@tanstack/react-router'
import { Menu } from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { cn } from '@/lib/utils'

import { isNavPathActive } from '../lib/url-utils'
import type { TopNavLink } from '../types'

type TopNavProps = React.HTMLAttributes<HTMLElement> & {
  links: TopNavLink[]
}

/**
 * 顶部导航栏组件
 * 在大屏幕显示水平导航，在小屏幕显示下拉菜单
 */
export function TopNav({ className, links, ...props }: TopNavProps) {
  const { t } = useTranslation()
  const pathname = useLocation({
    select: (location) => location.pathname,
  })

  // Normalize links once, then derive active state from the router location.
  // Explicit `isActive` remains an escape hatch for custom/external links.
  const normalizedLinks = useMemo(
    () =>
      links.map((link) => ({
        isActive: false,
        disabled: false,
        external: false,
        ...link,
        isCurrent:
          link.isActive ??
          (!link.external && isNavPathActive(pathname, link.href)),
      })),
    [links, pathname]
  )

  if (normalizedLinks.length === 0) return null

  return (
    <>
      {/* 移动端下拉菜单 */}
      <div className='@7xl/app-header:hidden'>
        <DropdownMenu modal={false}>
          <DropdownMenuTrigger
            render={
              <Button
                size='icon'
                variant='outline'
                className='border-foreground/15 size-7 rounded-none'
                aria-label={t('Toggle navigation menu')}
              />
            }
          >
            <Menu aria-hidden='true' />
          </DropdownMenuTrigger>
          <DropdownMenuContent side='bottom' align='start'>
            {normalizedLinks.map(
              ({ title, href, isCurrent, disabled, external }) => (
                <DropdownMenuItem
                  key={`${title}-${href}`}
                  render={
                    external ? (
                      <a
                        href={href}
                        target='_blank'
                        rel='noopener noreferrer'
                        aria-current={isCurrent ? 'page' : undefined}
                        className={cn(
                          'text-muted-foreground',
                          isCurrent && 'text-foreground font-medium'
                        )}
                      >
                        {t(title)}
                      </a>
                    ) : (
                      <Link
                        to={href}
                        aria-current={isCurrent ? 'page' : undefined}
                        className={cn(
                          'text-muted-foreground',
                          isCurrent && 'text-foreground font-medium'
                        )}
                        disabled={disabled}
                      >
                        {t(title)}
                      </Link>
                    )
                  }
                />
              )
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {/* 桌面端水平导航 */}
      <nav
        className={cn(
          'hidden items-center space-x-4 @7xl/app-header:flex @7xl/app-header:space-x-6',
          className
        )}
        {...props}
      >
        {normalizedLinks.map(({ title, href, isCurrent, disabled, external }) =>
          external ? (
            <a
              key={`${title}-${href}`}
              href={href}
              target='_blank'
              rel='noopener noreferrer'
              aria-current={isCurrent ? 'page' : undefined}
              className={cn(
                'hover:bg-foreground/[0.045] hover:text-foreground inline-flex items-center rounded-none px-2.5 py-1.5 text-sm font-medium transition-colors',
                isCurrent
                  ? 'bg-foreground/[0.065] text-foreground font-semibold'
                  : 'text-muted-foreground'
              )}
            >
              {t(title)}
            </a>
          ) : (
            <Link
              key={`${title}-${href}`}
              to={href}
              disabled={disabled}
              aria-current={isCurrent ? 'page' : undefined}
              className={cn(
                'hover:bg-foreground/[0.045] hover:text-foreground inline-flex items-center rounded-none px-2.5 py-1.5 text-sm font-medium transition-colors',
                isCurrent
                  ? 'bg-foreground/[0.065] text-foreground font-semibold'
                  : 'text-muted-foreground'
              )}
            >
              {t(title)}
            </Link>
          )
        )}
      </nav>
    </>
  )
}

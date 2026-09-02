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
import { Link } from '@tanstack/react-router'
import { PanelLeftClose, PanelLeftOpen } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  useSidebar,
} from '@/components/ui/sidebar'
import { useStatus } from '@/hooks/use-status'
import { useSystemConfig } from '@/hooks/use-system-config'
import { resolveSystemName } from '@/lib/constants'
import { cn } from '@/lib/utils'

type SystemBrandProps = {
  defaultName?: string
  defaultVersion?: string
  /**
   * Visual layout:
   * - 'sidebar': stacked card style (used inside the sidebar header).
   * - 'inline': compact horizontal pill (used inside the top app bar).
   */
  variant?: 'sidebar' | 'inline'
}

/**
 * System brand component
 * Displays current system logo + name.
 * - inline: compact sidebar control in the top app bar
 * - sidebar: stacked navigation treatment for sidebar headers
 */
export function SystemBrand(props: SystemBrandProps) {
  const { t } = useTranslation()
  const { state, toggleSidebar } = useSidebar()
  const { status } = useStatus()
  const { logo, systemName } = useSystemConfig()

  const variant = props.variant ?? 'sidebar'
  const name = resolveSystemName(
    systemName || status?.system_name || props.defaultName || 'New API'
  )
  const version =
    status?.version || props.defaultVersion || t('Unknown version')

  if (variant === 'inline') {
    const actionLabel = t('Sidebar')
    const SidebarStateIcon =
      state === 'expanded' ? PanelLeftClose : PanelLeftOpen

    return (
      <Button
        type='button'
        variant='ghost'
        aria-label={actionLabel}
        aria-expanded={state === 'expanded'}
        title={actionLabel}
        onClick={toggleSidebar}
        className={cn(
          'group/brand text-foreground h-7 min-w-0 max-w-[9.5rem] shrink gap-1 rounded-md px-1.5 text-sm font-medium sm:max-w-[14.5rem]',
          'justify-start hover:bg-accent focus-visible:ring-ring/40 focus-visible:ring-2'
        )}
      >
        <span className='relative flex size-5 shrink-0 items-center justify-center overflow-hidden rounded-md'>
          <img
            src={logo}
            alt={t('Logo')}
            className='size-full rounded-md object-cover transition-opacity group-hover/brand:opacity-15 group-focus-visible/brand:opacity-15'
          />
          <SidebarStateIcon
            className='absolute size-3.5 opacity-0 transition-opacity group-hover/brand:opacity-100 group-focus-visible/brand:opacity-100'
            aria-hidden='true'
          />
        </span>
        <span className='block max-w-[7.5rem] min-w-0 truncate sm:max-w-[12rem]'>
          {name}
        </span>
      </Button>
    )
  }

  return (
    <SidebarMenu>
      <SidebarMenuItem>
        <SidebarMenuButton
          size='lg'
          tooltip={name}
          className='hover:bg-sidebar-accent/70 h-11 gap-2 px-1.5'
          render={
            <Link to='/dashboard/$section' params={{ section: 'overview' }} />
          }
        >
          <div className='flex aspect-square size-8 items-center justify-center overflow-hidden rounded-md border border-white/10 shadow-xs'>
            <img
              src={logo}
              alt={t('Logo')}
              className='size-full rounded-md object-cover'
            />
          </div>
          <div className='grid flex-1 text-start text-sm leading-tight group-data-[collapsible=icon]:hidden'>
            <span className='truncate font-semibold'>{name}</span>
            <span className='text-sidebar-foreground/50 truncate text-[11px]'>
              {t('Dashboard')} · {version}
            </span>
          </div>
        </SidebarMenuButton>
      </SidebarMenuItem>
    </SidebarMenu>
  )
}

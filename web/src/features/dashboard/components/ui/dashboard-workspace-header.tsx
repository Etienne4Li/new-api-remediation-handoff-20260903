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
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { cn } from '@/lib/utils'

import type { DashboardSectionId } from '../../section-registry'
import { DASHBOARD_SECTION_PRESENTATION } from './dashboard-workspace'

type DashboardWorkspaceHeaderProps = {
  activeSection: DashboardSectionId
  visibleSections: DashboardSectionId[]
  onSectionChange: (section: string) => void
  actions?: ReactNode
}

export function DashboardWorkspaceHeader(props: DashboardWorkspaceHeaderProps) {
  const { t } = useTranslation()

  return (
    <div
      className='mt-3 flex min-w-0 items-end justify-between gap-3 sm:mt-4'
      data-dashboard-workspace
    >
      <Tabs
        value={props.activeSection}
        onValueChange={props.onSectionChange}
        className='min-w-0 flex-1'
      >
        <TabsList
          variant='line'
          aria-label={t('Dashboard')}
          className='h-auto w-full max-w-full justify-start gap-1 overflow-x-auto rounded-none bg-transparent p-0'
        >
          {props.visibleSections.map((section) => {
            const presentation = DASHBOARD_SECTION_PRESENTATION[section]
            const Icon = presentation.icon

            return (
              <TabsTrigger
                key={section}
                value={section}
                className={cn(
                  'min-h-9 flex-none gap-1.5 rounded-none px-2 py-2 text-xs sm:px-3 sm:text-sm',
                  section === props.activeSection && 'font-semibold'
                )}
              >
                <Icon aria-hidden='true' />
                {t(presentation.titleKey)}
              </TabsTrigger>
            )
          })}
        </TabsList>
      </Tabs>

      {props.actions != null && (
        <div
          className='flex min-w-0 shrink-0 items-center gap-1.5 overflow-x-auto pb-1 [&>*]:shrink-0'
          data-dashboard-workspace-actions
        >
          {props.actions}
        </div>
      )}
    </div>
  )
}

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
import { ArrowUpDown, Check, Filter } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { DataTableViewModeToggle } from '@/components/data-table'
import {
  sideDrawerContentClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { cn } from '@/lib/utils'

import { getSortLabels, type SortOption, type ViewMode } from '../constants'
import type { PricingModel, PricingVendor, TokenUnit } from '../types'
import { PricingSidebar } from './pricing-sidebar'
import { SearchBar } from './search-bar'

export interface PricingToolbarProps {
  filteredCount: number
  totalCount?: number
  searchValue: string
  onSearchChange: (value: string) => void
  onClearSearch: () => void
  sortBy: string
  onSortChange: (value: string) => void
  tokenUnit: TokenUnit
  onTokenUnitChange: (value: TokenUnit) => void
  showRechargePrice: boolean
  onRechargePriceChange: (value: boolean) => void
  viewMode: ViewMode
  onViewModeChange: (value: ViewMode) => void
  quotaTypeFilter: string
  endpointTypeFilter: string
  vendorFilter: string
  groupFilter: string
  tagFilter: string
  onQuotaTypeChange: (value: string) => void
  onEndpointTypeChange: (value: string) => void
  onVendorChange: (value: string) => void
  onGroupChange: (value: string) => void
  onTagChange: (value: string) => void
  vendors: PricingVendor[]
  groups: string[]
  groupRatios?: Record<string, number>
  tags: string[]
  models: PricingModel[]
  hasActiveFilters: boolean
  activeFilterCount: number
  onClearFilters: () => void
}

function SegmentedControl(props: {
  options: SegmentOption[]
  value: string
  onChange: (value: string) => void
  ariaLabel: string
}) {
  return (
    <div
      role='group'
      aria-label={props.ariaLabel}
      className='bg-muted/60 inline-flex h-8 items-center rounded-none border p-0.5'
    >
      {props.options.map((option) => {
        const Icon = option.icon
        const isActive = option.value === props.value
        const button = (
          <button
            key={option.value}
            type='button'
            onClick={() => props.onChange(option.value)}
            aria-pressed={isActive}
            className={cn(
              'inline-flex h-full items-center justify-center rounded-none text-xs font-medium transition-all',
              Icon && !option.label ? 'w-7' : 'gap-1.5 px-3',
              isActive
                ? 'bg-primary text-primary-foreground'
                : 'text-muted-foreground hover:text-foreground'
            )}
          >
            {Icon && <Icon className='size-3.5' />}
            {option.label}
          </button>
        )

        if (!option.tooltip) {
          return button
        }

        return (
          <Tooltip key={option.value}>
            <TooltipTrigger render={button} />
            <TooltipContent side='bottom' className='text-xs'>
              {option.tooltip}
            </TooltipContent>
          </Tooltip>
        )
      })}
    </div>
  )
}

export function PricingToolbar(props: PricingToolbarProps) {
  const { t } = useTranslation()
  const [mobileFiltersOpen, setMobileFiltersOpen] = useState(false)
  const sortLabels = getSortLabels(t)

  return (
    <section
      aria-label={t('Pricing')}
      className='min-h-[66px] rounded-none border p-3 sm:p-3'
    >
      <div className='flex flex-col gap-3 xl:flex-row xl:items-center xl:justify-between'>
        <div className='flex min-w-0 items-center gap-3'>
          <Button
            type='button'
            variant='outline'
            size='sm'
            onClick={() => setMobileFiltersOpen(true)}
            className='gap-1.5 xl:hidden'
          >
            <Filter className='size-4' />
            {t('Filter')}
            {props.activeFilterCount > 0 && (
              <Badge className='ml-0.5 size-5 justify-center p-0 text-[10px]'>
                {props.activeFilterCount}
              </Badge>
            )}
          </Button>

          <div
            aria-live='polite'
            className='text-muted-foreground flex shrink-0 items-baseline gap-1 text-sm'
          >
            <span className='text-foreground font-semibold tabular-nums'>
              {props.filteredCount.toLocaleString()}
            </span>
            <span>{props.filteredCount === 1 ? t('model') : t('models')}</span>
            {props.hasActiveFilters && props.totalCount && (
              <span className='text-muted-foreground/60 text-xs'>
                / {props.totalCount.toLocaleString()}
              </span>
            )}
            {props.hasActiveFilters && (
              <Badge
                variant='secondary'
                className='ml-1 hidden h-5 px-1.5 text-[10px] sm:inline-flex'
              >
                {t('Filters active')}
              </Badge>
            )}
          </div>

          <SearchBar
            value={props.searchValue}
            onChange={props.onSearchChange}
            onClear={props.onClearSearch}
            placeholder={t('Search models')}
            className='min-w-0 flex-1 sm:w-64 sm:flex-none'
          />
        </div>

        <div className='flex flex-wrap items-center gap-2 xl:justify-end'>
          <div className='hidden items-center gap-2 sm:flex'>
            <SegmentedControl
              options={[
                { value: 'standard', label: t('Standard') },
                { value: 'recharge', label: t('Recharge') },
              ]}
              value={props.showRechargePrice ? 'recharge' : 'standard'}
              onChange={handleRechargePriceChange}
              ariaLabel={t('Price display mode')}
            />
            <SegmentedControl
              options={[
                { value: 'M', label: '/1M' },
                { value: 'K', label: '/1K' },
              ]}
              value={props.tokenUnit}
              onChange={handleTokenUnitChange}
              ariaLabel={t('Token unit')}
            />
          </div>

          <DropdownMenu>
            <DropdownMenuTrigger
              render={
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  className='h-8 gap-1.5 rounded-none px-3 text-xs'
                />
              }
            >
              <ArrowUpDown className='size-3.5' />
              <span>{sortLabels[props.sortBy as SortOption] || t('Sort')}</span>
            </DropdownMenuTrigger>
            <DropdownMenuContent align='end' className='w-44'>
              <DropdownMenuGroup>
                {Object.entries(sortLabels).map(([value, label]) => (
                  <DropdownMenuItem
                    key={value}
                    onClick={() => props.onSortChange(value)}
                    className='gap-2'
                  >
                    <Check
                      className={cn(
                        'size-4 shrink-0',
                        props.sortBy === value ? 'opacity-100' : 'opacity-0'
                      )}
                    />
                    {label}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuGroup>
            </DropdownMenuContent>
          </DropdownMenu>

          <DataTableViewModeToggle
            value={props.viewMode}
            onChange={props.onViewModeChange}
          />
        </div>
      </div>

      <Sheet open={mobileFiltersOpen} onOpenChange={setMobileFiltersOpen}>
        <SheetContent
          side='left'
          className={sideDrawerContentClassName('sm:max-w-md')}
        >
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle>{t('Filter')}</SheetTitle>
            <SheetDescription>
              {t('Filter models by provider, group, type, endpoint, and tags.')}
            </SheetDescription>
          </SheetHeader>
          <div className={sideDrawerFormClassName('gap-0')}>
            <PricingSidebar
              quotaTypeFilter={props.quotaTypeFilter}
              endpointTypeFilter={props.endpointTypeFilter}
              vendorFilter={props.vendorFilter}
              groupFilter={props.groupFilter}
              tagFilter={props.tagFilter}
              onQuotaTypeChange={props.onQuotaTypeChange}
              onEndpointTypeChange={props.onEndpointTypeChange}
              onVendorChange={props.onVendorChange}
              onGroupChange={props.onGroupChange}
              onTagChange={props.onTagChange}
              vendors={props.vendors}
              groups={props.groups}
              groupRatios={props.groupRatios}
              tags={props.tags}
              models={props.models}
              hasActiveFilters={props.hasActiveFilters}
              onClearFilters={props.onClearFilters}
              className='border-0 bg-transparent p-0 shadow-none'
            />
          </div>
        </SheetContent>
      </Sheet>
    </section>
  )
}

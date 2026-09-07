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
import { BookOpen, ReceiptText } from 'lucide-react'
import { lazy, Suspense, useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { PublicLayout } from '@/components/layout'
import { PageTransition } from '@/components/page-transition'

import {
  LoadingSkeleton,
  EmptyState,
  PricingTable,
  PricingSidebar,
  PricingToolbar,
  ModelCardGrid,
} from './components'
import { EXCLUDED_GROUPS, VIEW_MODES } from './constants'
import { useFilters } from './hooks/use-filters'
import { usePricingData } from './hooks/use-pricing-data'

const LazyModelDetailsDrawer = lazy(() =>
  import('./components/model-details').then((module) => ({
    default: module.ModelDetailsDrawer,
  }))
)

export function Pricing() {
  const { t } = useTranslation()
  const [selectedModelName, setSelectedModelName] = useState<string | null>(
    null
  )

  const {
    models,
    vendors,
    groupRatio,
    usableGroup,
    endpointMap,
    autoGroups,
    isLoading,
    priceRate,
    usdExchangeRate,
  } = usePricingData()

  const {
    searchInput,
    sortBy,
    vendorFilter,
    groupFilter,
    quotaTypeFilter,
    endpointTypeFilter,
    tagFilter,
    tokenUnit,
    viewMode,
    showRechargePrice,
    setSearchInput,
    setSortBy,
    setVendorFilter,
    setGroupFilter,
    setQuotaTypeFilter,
    setEndpointTypeFilter,
    setTagFilter,
    setTokenUnit,
    setViewMode,
    setShowRechargePrice,
    filteredModels,
    hasActiveFilters,
    activeFilterCount,
    availableTags,
    clearFilters,
    clearSearch,
  } = useFilters(models || [])

  const handleModelClick = useCallback((modelName: string) => {
    setSelectedModelName(modelName)
  }, [])

  const selectedModel = useMemo(
    () =>
      selectedModelName
        ? (models || []).find(
            (model) => model.model_name === selectedModelName
          ) || null
        : null,
    [models, selectedModelName]
  )

  const availableGroups = useMemo(
    () =>
      Object.keys(usableGroup || {}).filter(
        (g) => !EXCLUDED_GROUPS.includes(g)
      ),
    [usableGroup]
  )

  const handleClearAll = useCallback(() => {
    clearFilters()
    clearSearch()
  }, [clearFilters, clearSearch])

  const renderPricingContent = () => {
    if (filteredModels.length === 0) {
      return (
        <EmptyState
          searchQuery={searchInput}
          hasActiveFilters={hasActiveFilters}
          onClearFilters={handleClearAll}
        />
      )
    }

    if (viewMode === VIEW_MODES.CARD) {
      return (
        <ModelCardGrid
          models={filteredModels}
          onModelClick={handleModelClick}
          priceRate={priceRate}
          usdExchangeRate={usdExchangeRate}
          tokenUnit={tokenUnit}
          showRechargePrice={showRechargePrice}
          selectedGroup={groupFilter}
        />
      )
    }

    return (
      <PricingTable
        models={filteredModels}
        priceRate={priceRate}
        usdExchangeRate={usdExchangeRate}
        tokenUnit={tokenUnit}
        showRechargePrice={showRechargePrice}
        selectedGroup={groupFilter}
        onModelClick={handleModelClick}
      />
    )
  }

  if (isLoading) {
    return (
      <PublicLayout showMainContainer={false}>
        <div className='mx-auto w-full max-w-[1800px] px-4 pt-20 pb-8 sm:px-6 xl:px-8'>
          <LoadingSkeleton viewMode={viewMode} />
        </div>
      </PublicLayout>
    )
  }

  return (
    <PublicLayout showMainContainer={false}>
      <PageTransition className='pt-16 sm:pt-16'>
        <div className='mx-auto w-full max-w-[1800px] px-4 py-4 sm:px-6 sm:py-4 xl:px-8'>
          <div className='mb-6 flex h-9 items-center justify-between gap-4 border-b px-0 pb-0'>
            <div className='flex min-w-0 items-center gap-2'>
              <h1 id='pricing-catalog-title' className='sr-only'>
                {t('Models')}
              </h1>
              <span className='text-sm font-semibold'>
                {t('Enabled models')}
              </span>
              <span
                aria-label={t(
                  'This site currently has {{count}} models enabled',
                  {
                    count: models?.length || 0,
                  }
                )}
                className='bg-muted text-foreground inline-flex h-7 min-w-7 items-center justify-center border px-2 font-mono text-sm font-semibold tabular-nums'
              >
                {models?.length || 0}
              </span>
            </div>
            <a
              href='https://docs.newapi.pro'
              target='_blank'
              rel='noopener noreferrer'
              className='text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 text-xs transition-colors'
            >
              <BookOpen className='size-3.5' aria-hidden='true' />
              {t('Pricing guide')}
            </a>
          </div>

          <section
            aria-labelledby='pricing-explanation-title'
            className='bg-primary/5 border-primary/20 mb-6 flex h-[62px] items-center justify-between gap-4 overflow-hidden border px-4 py-3'
          >
            <div className='flex min-w-0 items-start gap-3'>
              <span className='bg-primary/10 text-primary mt-0.5 flex size-7 shrink-0 items-center justify-center'>
                <ReceiptText className='size-4' aria-hidden='true' />
              </span>
              <div className='min-w-0'>
                <h2
                  id='pricing-explanation-title'
                  className='text-sm font-semibold'
                >
                  {t('How to understand billing')}
                </h2>
                <p className='text-muted-foreground mt-0.5 text-xs leading-relaxed'>
                  {t(
                    'Each request cost is calculated from input, cache creation, cache reads, and output.'
                  )}
                </p>
              </div>
            </div>
            <a
              href='https://docs.newapi.pro'
              target='_blank'
              rel='noopener noreferrer'
              className='text-foreground inline-flex shrink-0 items-center gap-1 text-xs font-semibold hover:underline'
            >
              {t('View explanation')}
              <span aria-hidden='true'>↗</span>
            </a>
          </section>

          <div className='grid gap-4 xl:grid-cols-[280px_minmax(0,1fr)]'>
            <PricingSidebar
              quotaTypeFilter={quotaTypeFilter}
              endpointTypeFilter={endpointTypeFilter}
              vendorFilter={vendorFilter}
              groupFilter={groupFilter}
              tagFilter={tagFilter}
              onQuotaTypeChange={setQuotaTypeFilter}
              onEndpointTypeChange={setEndpointTypeFilter}
              onVendorChange={setVendorFilter}
              onGroupChange={setGroupFilter}
              onTagChange={setTagFilter}
              vendors={vendors || []}
              groups={availableGroups}
              groupRatios={groupRatio}
              tags={availableTags}
              models={models || []}
              hasActiveFilters={hasActiveFilters}
              onClearFilters={clearFilters}
              className='hover-scrollbar sticky top-20 hidden max-h-[calc(100dvh-6rem)] self-start overflow-y-auto xl:block'
            />

            <main
              aria-labelledby='pricing-catalog-title'
              className='bg-background min-w-0'
            >
              <PricingToolbar
                filteredCount={filteredModels.length}
                totalCount={models?.length}
                searchValue={searchInput}
                onSearchChange={setSearchInput}
                onClearSearch={clearSearch}
                sortBy={sortBy}
                onSortChange={setSortBy}
                tokenUnit={tokenUnit}
                onTokenUnitChange={setTokenUnit}
                showRechargePrice={showRechargePrice}
                onRechargePriceChange={setShowRechargePrice}
                viewMode={viewMode}
                onViewModeChange={setViewMode}
                quotaTypeFilter={quotaTypeFilter}
                endpointTypeFilter={endpointTypeFilter}
                vendorFilter={vendorFilter}
                groupFilter={groupFilter}
                tagFilter={tagFilter}
                onQuotaTypeChange={setQuotaTypeFilter}
                onEndpointTypeChange={setEndpointTypeFilter}
                onVendorChange={setVendorFilter}
                onGroupChange={setGroupFilter}
                onTagChange={setTagFilter}
                vendors={vendors || []}
                groups={availableGroups}
                groupRatios={groupRatio}
                tags={availableTags}
                models={models || []}
                hasActiveFilters={hasActiveFilters}
                activeFilterCount={activeFilterCount}
                onClearFilters={clearFilters}
              />

              <div className='p-3 sm:p-4'>{renderPricingContent()}</div>
            </main>
          </div>

          {selectedModel && (
            <Suspense fallback={null}>
              <LazyModelDetailsDrawer
                open={Boolean(selectedModel)}
                onOpenChange={(open) => {
                  if (!open) setSelectedModelName(null)
                }}
                model={selectedModel}
                groupRatio={groupRatio || {}}
                usableGroup={usableGroup || {}}
                endpointMap={
                  (endpointMap as Record<
                    string,
                    { path?: string; method?: string }
                  >) || {}
                }
                autoGroups={autoGroups || []}
                priceRate={priceRate ?? 1}
                usdExchangeRate={usdExchangeRate ?? 1}
                tokenUnit={tokenUnit}
                showRechargePrice={showRechargePrice}
              />
            </Suspense>
          )}
        </div>
      </PageTransition>
    </PublicLayout>
  )
}

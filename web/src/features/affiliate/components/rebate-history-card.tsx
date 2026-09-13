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
import { ChevronLeft, ChevronRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import type { AffRebateRecord } from '@/features/wallet/types'

import { RebateDetailList } from './rebate-detail-list'

/**
 * Paginated rebate ledger. Only mounted while the backend reports the rebate
 * feature on — with it off there is nothing to page through.
 */
export function RebateHistoryCard({
  rebates,
  maxTimes,
  loading,
  page,
  pageSize,
  total,
  onPageChange,
}: {
  rebates: AffRebateRecord[]
  maxTimes: number
  loading: boolean
  page: number
  pageSize: number
  total: number
  onPageChange: (page: number) => void
}) {
  const { t } = useTranslation()
  const totalPages = Math.max(1, Math.ceil(total / pageSize))

  return (
    <Card data-card-hover='false' className='py-0 shadow-sm'>
      <CardContent className='p-0'>
        <section aria-label={t('Rebate Details')}>
          <h3 className='bg-muted/20 text-muted-foreground rounded-t-xl border-b px-3 py-2 text-[10px] font-medium uppercase'>
            {t('Rebate Details')}
          </h3>
          <RebateDetailList
            rebates={rebates}
            maxTimes={maxTimes}
            loading={loading}
          />
          {total > pageSize && (
            <div className='flex items-center justify-between gap-2 border-t px-3 py-2'>
              <span className='text-muted-foreground text-[11px] tabular-nums'>
                {page} / {totalPages}
              </span>
              <div className='flex items-center gap-1'>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  className='size-7 p-0'
                  aria-label={t('Previous page')}
                  onClick={() => onPageChange(page - 1)}
                  disabled={page <= 1 || loading}
                >
                  <ChevronLeft className='size-3.5' />
                </Button>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  className='size-7 p-0'
                  aria-label={t('Next page')}
                  onClick={() => onPageChange(page + 1)}
                  disabled={page >= totalPages || loading}
                >
                  <ChevronRight className='size-3.5' />
                </Button>
              </div>
            </div>
          )}
        </section>
      </CardContent>
    </Card>
  )
}

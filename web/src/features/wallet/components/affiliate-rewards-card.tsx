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
import { ChevronLeft, ChevronRight, Share2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { IconBadge } from '@/components/ui/icon-badge'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { formatNumber, formatQuota } from '@/lib/format'

import { useAffRebates } from '../hooks/use-aff-rebates'
import { formatTimestamp } from '../lib/billing'
import type { AffRebateRecord, UserWalletData } from '../types'

// Named rather than index-keyed so the placeholder rows carry stable keys.
const REBATE_SKELETON_ROWS = ['first', 'second', 'third']

function RebateDetailList({
  rebates,
  maxTimes,
  loading,
}: {
  rebates: AffRebateRecord[]
  maxTimes: number
  loading: boolean
}) {
  const { t } = useTranslation()

  if (loading && rebates.length === 0) {
    return (
      <div className='space-y-2 p-3'>
        {REBATE_SKELETON_ROWS.map((row) => (
          <Skeleton key={row} className='h-8' />
        ))}
      </div>
    )
  }

  if (rebates.length === 0) {
    return (
      <p className='text-muted-foreground px-3 py-4 text-center text-xs'>
        {t('No rebates yet. Share your referral link to start earning.')}
      </p>
    )
  }

  return (
    <ul className='divide-border/70 divide-y'>
      {rebates.map((rebate) => (
        <li
          key={rebate.id}
          className='flex items-center justify-between gap-3 px-3 py-2'
        >
          <div className='min-w-0'>
            <p className='truncate text-xs font-medium'>{rebate.invitee}</p>
            <p className='text-muted-foreground mt-0.5 text-[11px] tabular-nums'>
              {formatTimestamp(rebate.created_time)}
              {' · '}
              {t('Top-up {{sequence}}/{{maxTimes}}', {
                sequence: rebate.sequence,
                maxTimes,
              })}
              {' · '}
              {t('Paid')} {formatNumber(rebate.topup_money)}
            </p>
          </div>
          <span className='text-success shrink-0 text-sm font-semibold tabular-nums'>
            +{formatQuota(rebate.rebate_quota)}
          </span>
        </li>
      ))}
    </ul>
  )
}

interface AffiliateRewardsCardProps {
  user: UserWalletData | null
  affiliateLink: string
  onTransfer: () => void
  complianceConfirmed?: boolean
  loading?: boolean
}

export function AffiliateRewardsCard({
  user,
  affiliateLink,
  onTransfer,
  complianceConfirmed = true,
  loading,
}: AffiliateRewardsCardProps) {
  const { t } = useTranslation()
  // The rebate block stays hidden until the backend reports the feature on, so
  // a disabled site shows exactly the card it shows today.
  const {
    records: rebates,
    total: rebateTotal,
    page: rebatePage,
    pageSize: rebatePageSize,
    enabled: rebateEnabled,
    percent: rebatePercent,
    maxTimes: rebateMaxTimes,
    loading: rebatesLoading,
    setPage: setRebatePage,
  } = useAffRebates()

  if (loading) {
    return (
      <Card data-card-hover='false' className='py-0 shadow-sm'>
        <CardContent className='grid gap-4 p-3 sm:p-4 lg:grid-cols-[minmax(220px,1fr)_minmax(220px,0.72fr)_minmax(320px,1.15fr)] lg:items-center'>
          <div>
            <Skeleton className='h-5 w-32' />
            <Skeleton className='mt-2 h-4 w-48' />
          </div>
          <Skeleton className='h-14 rounded-lg' />
          <Skeleton className='h-10 rounded-lg' />
        </CardContent>
      </Card>
    )
  }

  const hasRewards = (user?.aff_quota ?? 0) > 0
  const rebateTotalPages = Math.max(1, Math.ceil(rebateTotal / rebatePageSize))

  return (
    <Card data-card-hover='false' className='py-0 shadow-sm'>
      <CardContent className='grid gap-3 p-3 sm:gap-4 sm:p-4 lg:grid-cols-[minmax(200px,1fr)_minmax(180px,0.65fr)_minmax(280px,1fr)] lg:items-center'>
        <div className='flex min-w-0 items-center gap-2.5'>
          <IconBadge tone='chart-3'>
            <Share2 />
          </IconBadge>
          <div className='min-w-0'>
            <h3 className='truncate text-sm font-semibold'>
              {t('Referral Program')}
            </h3>
            <p className='text-muted-foreground line-clamp-2 text-xs'>
              {rebateEnabled
                ? t(
                    'Friends who sign up with your link earn you a {{percent}}% rebate on each of their first {{maxTimes}} top-ups, transferable to your balance anytime.',
                    { percent: rebatePercent, maxTimes: rebateMaxTimes }
                  )
                : t(
                    'Earn rewards when users join through your referral link. Transfer accumulated rewards to your balance anytime.'
                  )}
            </p>
          </div>
        </div>

        <dl className='grid grid-cols-3 gap-1.5 text-center'>
          {[
            // Rebates land immediately, so this balance is available to
            // transfer right now — nothing is ever "pending".
            [t('Available to transfer'), formatQuota(user?.aff_quota ?? 0)],
            [t('Total Earned'), formatQuota(user?.aff_history_quota ?? 0)],
            [t('Invites'), String(user?.aff_count ?? 0)],
          ].map(([label, value]) => (
            <div key={label} className='min-w-0'>
              <dt className='text-muted-foreground truncate text-[10px] font-medium uppercase'>
                {label}
              </dt>
              <dd className='mt-0.5 truncate text-sm font-semibold tabular-nums'>
                {value}
              </dd>
            </div>
          ))}
        </dl>

        <div className='grid grid-cols-[minmax(0,1fr)_auto] items-center gap-2 sm:grid-cols-[minmax(0,1fr)_auto_auto]'>
          <Input
            value={affiliateLink}
            readOnly
            aria-label={t('Copy referral link')}
            className='border-muted bg-background/70 h-9 min-w-0 flex-1 font-mono text-xs'
          />
          <CopyButton
            value={affiliateLink}
            variant='outline'
            className='bg-background size-9 shrink-0'
            iconClassName='size-4'
            tooltip={t('Copy referral link')}
            aria-label={t('Copy referral link')}
          />
          {hasRewards && (
            <Button
              onClick={onTransfer}
              disabled={!complianceConfirmed}
              className='col-span-2 h-9 w-full px-3 whitespace-normal sm:col-span-1 sm:w-auto'
              size='sm'
            >
              {t('Transfer to Balance')}
            </Button>
          )}
        </div>
        {!complianceConfirmed ? (
          <p className='text-muted-foreground text-xs lg:col-span-3'>
            {t(
              'Referral reward transfer is disabled until the administrator confirms compliance terms.'
            )}
          </p>
        ) : null}

        {rebateEnabled ? (
          <section
            aria-label={t('Rebate Details')}
            className='border-border/80 overflow-hidden rounded-lg border lg:col-span-3'
          >
            <h4 className='bg-muted/20 text-muted-foreground border-b px-3 py-2 text-[10px] font-medium uppercase'>
              {t('Rebate Details')}
            </h4>
            <RebateDetailList
              rebates={rebates}
              maxTimes={rebateMaxTimes}
              loading={rebatesLoading}
            />
            {rebateTotal > rebatePageSize ? (
              <div className='flex items-center justify-between gap-2 border-t px-3 py-2'>
                <span className='text-muted-foreground text-[11px] tabular-nums'>
                  {rebatePage} / {rebateTotalPages}
                </span>
                <div className='flex items-center gap-1'>
                  <Button
                    type='button'
                    variant='outline'
                    size='sm'
                    className='size-7 p-0'
                    aria-label={t('Previous page')}
                    onClick={() => setRebatePage(rebatePage - 1)}
                    disabled={rebatePage <= 1 || rebatesLoading}
                  >
                    <ChevronLeft className='size-3.5' />
                  </Button>
                  <Button
                    type='button'
                    variant='outline'
                    size='sm'
                    className='size-7 p-0'
                    aria-label={t('Next page')}
                    onClick={() => setRebatePage(rebatePage + 1)}
                    disabled={rebatePage >= rebateTotalPages || rebatesLoading}
                  >
                    <ChevronRight className='size-3.5' />
                  </Button>
                </div>
              </div>
            ) : null}
          </section>
        ) : null}
      </CardContent>
    </Card>
  )
}

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
import { useTranslation } from 'react-i18next'

import { Skeleton } from '@/components/ui/skeleton'
import { formatTimestamp } from '@/features/wallet/lib/billing'
import type { AffRebateRecord } from '@/features/wallet/types'
import { formatNumber, formatQuota } from '@/lib/format'

// Named rather than index-keyed so the placeholder rows carry stable keys.
const REBATE_SKELETON_ROWS = ['first', 'second', 'third']

/**
 * One row per granted rebate: masked invitee, when it landed, which of the
 * invitee's eligible top-ups it came from, what they paid, and what the
 * inviter earned.
 *
 * Rows stack on narrow screens — the metadata line wraps under the invitee
 * instead of pushing the amount off-screen.
 */
export function RebateDetailList({
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
      <p className='text-muted-foreground px-3 py-6 text-center text-xs'>
        {t('No rebates yet. Share your referral link to start earning.')}
      </p>
    )
  }

  return (
    <ul className='divide-border/70 divide-y'>
      {rebates.map((rebate) => (
        <li
          key={rebate.id}
          className='flex flex-wrap items-center justify-between gap-x-3 gap-y-1 px-3 py-2.5'
        >
          <div className='min-w-0 flex-1'>
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

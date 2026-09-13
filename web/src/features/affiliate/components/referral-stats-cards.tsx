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
import { Coins, TrendingUp, Users } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import type { UserWalletData } from '@/features/wallet/types'
import { formatQuota } from '@/lib/format'

/**
 * Transferable balance, lifetime earnings and invite count.
 *
 * One card per metric so the numbers stay legible; the grid collapses to a
 * single column on narrow screens rather than scrolling sideways.
 */
export function ReferralStatsCards({
  user,
  loading,
  onTransfer,
  complianceConfirmed = true,
}: {
  user: UserWalletData | null
  loading: boolean
  onTransfer: () => void
  complianceConfirmed?: boolean
}) {
  const { t } = useTranslation()

  const stats = [
    {
      // Rebates land immediately, so this balance is available to transfer
      // right now — nothing is ever "pending".
      label: t('Available to transfer'),
      value: formatQuota(user?.aff_quota ?? 0),
      icon: Coins,
      tone: 'text-success',
    },
    {
      label: t('Total Earned'),
      value: formatQuota(user?.aff_history_quota ?? 0),
      icon: TrendingUp,
      tone: 'text-info',
    },
    {
      label: t('Invites'),
      value: String(user?.aff_count ?? 0),
      icon: Users,
      tone: 'text-primary',
    },
  ]

  const hasRewards = (user?.aff_quota ?? 0) > 0

  return (
    <section aria-label={t('Referral Rewards')} className='space-y-3'>
      <div className='grid gap-3 sm:grid-cols-3'>
        {stats.map((stat) => (
          <Card key={stat.label} data-card-hover='false' className='py-0'>
            <CardContent className='flex min-w-0 items-center gap-3 p-4'>
              <stat.icon
                className={`size-5 shrink-0 ${stat.tone}`}
                aria-hidden='true'
              />
              {/* One list per card keeps `dt`/`dd` as direct `dl` children. */}
              <dl className='min-w-0'>
                <dt className='text-muted-foreground truncate text-[10px] font-medium uppercase'>
                  {stat.label}
                </dt>
                <dd className='mt-0.5 truncate text-lg font-semibold tabular-nums'>
                  {loading ? <Skeleton className='h-6 w-20' /> : stat.value}
                </dd>
              </dl>
            </CardContent>
          </Card>
        ))}
      </div>

      {hasRewards && (
        <Button
          onClick={onTransfer}
          disabled={!complianceConfirmed}
          className='w-full sm:w-auto'
        >
          {t('Transfer to Balance')}
        </Button>
      )}
      {!complianceConfirmed && (
        <p className='text-muted-foreground text-xs'>
          {t(
            'Referral reward transfer is disabled until the administrator confirms compliance terms.'
          )}
        </p>
      )}
    </section>
  )
}

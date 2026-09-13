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
import { ArrowRight, Share2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { IconBadge } from '@/components/ui/icon-badge'
import { Skeleton } from '@/components/ui/skeleton'
import { formatQuota } from '@/lib/format'

import type { UserWalletData } from '../types'

interface AffiliateRewardsCardProps {
  user: UserWalletData | null
  loading?: boolean
}

/**
 * One-line entry point to the referral page.
 *
 * The link, rules, transfer action and rebate ledger now live on
 * `/affiliate`; the wallet only advertises the balance waiting there so this
 * row does not compete with the top-up form above it.
 */
export function AffiliateRewardsCard({
  user,
  loading,
}: AffiliateRewardsCardProps) {
  const { t } = useTranslation()

  return (
    <Card data-card-hover='false' className='py-0 shadow-sm'>
      <CardContent className='flex flex-wrap items-center gap-3 p-3 sm:p-4'>
        <IconBadge tone='chart-3'>
          <Share2 />
        </IconBadge>

        <div className='min-w-0 flex-1'>
          <h3 className='truncate text-sm font-semibold'>
            {t('Referral Program')}
          </h3>
          <p className='text-muted-foreground truncate text-xs'>
            {t('Available to transfer')}
            {': '}
            <span className='text-foreground font-semibold tabular-nums'>
              {loading ? '—' : formatQuota(user?.aff_quota ?? 0)}
            </span>
          </p>
        </div>

        {loading ? (
          <Skeleton className='h-9 w-40 max-sm:w-full' />
        ) : (
          <Button
            size='sm'
            variant='outline'
            className='max-sm:w-full'
            render={<Link to='/affiliate' />}
          >
            {t('View referral rewards')}
            <ArrowRight data-icon='inline-end' />
          </Button>
        )}
      </CardContent>
    </Card>
  )
}

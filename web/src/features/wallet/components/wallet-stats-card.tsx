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
import { Activity, BarChart3, WalletCards } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { IconBadge, type IconBadgeTone } from '@/components/ui/icon-badge'
import { Skeleton } from '@/components/ui/skeleton'
import { formatQuota } from '@/lib/format'
import { cn } from '@/lib/utils'

import type { UserWalletData } from '../types'

interface WalletStatsCardProps {
  user: UserWalletData | null
  loading?: boolean
}

export function WalletStatsCard(props: WalletStatsCardProps) {
  const { t } = useTranslation()
  if (props.loading) {
    return (
      <section
        aria-label={t('Wallet')}
        aria-busy='true'
        className='bg-card grid overflow-hidden rounded-lg border shadow-sm md:grid-cols-[1.35fr_1fr_1fr]'
      >
        {['balance', 'usage', 'requests'].map((key, index) => (
          <div
            key={key}
            className={cn(
              'min-w-0 border-b p-4 last:border-b-0 sm:p-5 md:border-r md:border-b-0 md:last:border-r-0',
              index === 0 && 'bg-muted/20 sm:p-6'
            )}
          >
            <Skeleton className='h-4 w-28' />
            <Skeleton
              className={cn('mt-3 h-7 w-36', index === 0 && 'sm:h-9 sm:w-44')}
            />
            <Skeleton className='mt-2 h-3.5 w-24' />
          </div>
        ))}
      </section>
    )
  }

  const stats: {
    label: string
    value: string
    description: string
    icon: typeof WalletCards
    tone: IconBadgeTone
    primary?: boolean
  }[] = [
    {
      label: t('Current Balance'),
      value: formatQuota(props.user?.quota ?? 0),
      description: t('Remaining quota'),
      icon: WalletCards,
      tone: 'success',
      primary: true,
    },
    {
      label: t('Total Usage'),
      value: formatQuota(props.user?.used_quota ?? 0),
      description: t('Total consumed quota'),
      icon: BarChart3,
      tone: 'info',
    },
    {
      label: t('API Requests'),
      value: (props.user?.request_count ?? 0).toLocaleString(),
      description: t('Total requests made'),
      icon: Activity,
      tone: 'chart-4',
    },
  ]

  return (
    <dl
      aria-label={t('Wallet')}
      className='bg-card grid overflow-hidden rounded-lg border shadow-sm md:grid-cols-[1.35fr_1fr_1fr]'
    >
      {stats.map((item) => (
        <div
          key={item.label}
          className={cn(
            'min-w-0 border-b p-4 last:border-b-0 sm:p-5 md:border-r md:border-b-0 md:last:border-r-0',
            item.primary && 'bg-muted/20 sm:p-6'
          )}
        >
          <dt className='flex items-center gap-2.5'>
            <IconBadge tone={item.tone} size={item.primary ? 'md' : 'sm'}>
              <item.icon />
            </IconBadge>
            <span className='text-muted-foreground min-w-0 truncate text-xs font-medium uppercase'>
              {item.label}
            </span>
          </dt>

          <dd
            className={cn(
              'text-foreground mt-3 font-mono text-xl leading-tight font-bold break-all tabular-nums sm:text-2xl',
              item.primary && 'text-2xl sm:text-3xl'
            )}
          >
            {item.value}
          </dd>
          <dd className='text-muted-foreground mt-1.5 text-xs leading-5'>
            {item.description}
          </dd>
        </div>
      ))}
    </dl>
  )
}

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
import { Link2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Card, CardContent } from '@/components/ui/card'
import { IconBadge } from '@/components/ui/icon-badge'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'

/**
 * The page's primary action: the invite link, given the full content width
 * so it is readable rather than squeezed into a wallet grid column.
 */
export function ReferralLinkCard({
  affiliateLink,
  loading,
}: {
  affiliateLink: string
  loading: boolean
}) {
  const { t } = useTranslation()

  return (
    <Card data-card-hover='false' className='py-0 shadow-sm'>
      <CardContent className='space-y-3 p-4 sm:p-5'>
        <div className='flex min-w-0 items-center gap-2.5'>
          <IconBadge tone='chart-3'>
            <Link2 />
          </IconBadge>
          <div className='min-w-0'>
            <h3 className='text-sm font-semibold'>{t('Your referral link')}</h3>
            <p className='text-muted-foreground text-xs'>
              {t(
                'Share this link to invite friends. Anyone who signs up through it is linked to your account.'
              )}
            </p>
          </div>
        </div>

        {loading ? (
          <Skeleton className='h-10 rounded-lg' />
        ) : (
          <div className='flex items-center gap-2'>
            <Input
              value={affiliateLink}
              readOnly
              // Distinct from the copy button beside it, so screen readers
              // announce the field and the action differently.
              aria-label={t('Your referral link')}
              className='border-muted bg-background/70 h-10 min-w-0 flex-1 font-mono text-xs sm:text-sm'
            />
            <CopyButton
              value={affiliateLink}
              variant='outline'
              className='bg-background size-10 shrink-0'
              iconClassName='size-4'
              tooltip={t('Copy referral link')}
              aria-label={t('Copy referral link')}
            />
          </div>
        )}
      </CardContent>
    </Card>
  )
}

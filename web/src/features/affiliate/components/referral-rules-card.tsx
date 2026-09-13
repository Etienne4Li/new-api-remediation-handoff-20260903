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
import { CheckCircle2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Card, CardContent } from '@/components/ui/card'

/**
 * The rebate rules, spelled out in full.
 *
 * The wallet card had to truncate this to two clamped lines; on a dedicated
 * page each rule gets its own bullet with the live percentage and top-up
 * count substituted in.
 */
export function ReferralRulesCard({
  enabled,
  percent,
  maxTimes,
}: {
  enabled: boolean
  percent: number
  maxTimes: number
}) {
  const { t } = useTranslation()

  if (!enabled) {
    return (
      <Card data-card-hover='false' className='py-0 shadow-sm'>
        <CardContent className='p-4 sm:p-5'>
          <h3 className='text-sm font-semibold'>{t('How it works')}</h3>
          <p className='text-muted-foreground mt-1.5 text-xs'>
            {t('The referral rebate program is not open at the moment.')}
          </p>
        </CardContent>
      </Card>
    )
  }

  const rules = [
    t('Your friend signs up through your referral link.'),
    t(
      'Each of their first {{maxTimes}} online top-ups earns you {{percent}}% of the amount they actually paid.',
      { percent, maxTimes }
    ),
    t('Rebates are credited instantly, with no cap.'),
    t(
      'Rebates can only be transferred to your site balance; they cannot be withdrawn.'
    ),
    t('Top-ups paid with a redemption code do not earn rebates.'),
  ]

  return (
    <Card data-card-hover='false' className='py-0 shadow-sm'>
      <CardContent className='p-4 sm:p-5'>
        <h3 className='text-sm font-semibold'>{t('How it works')}</h3>
        <ul className='mt-3 space-y-2'>
          {rules.map((rule) => (
            <li key={rule} className='flex items-start gap-2 text-xs'>
              <CheckCircle2
                className='text-success mt-0.5 size-3.5 shrink-0'
                aria-hidden='true'
              />
              <span className='text-muted-foreground min-w-0'>{rule}</span>
            </li>
          ))}
        </ul>
      </CardContent>
    </Card>
  )
}

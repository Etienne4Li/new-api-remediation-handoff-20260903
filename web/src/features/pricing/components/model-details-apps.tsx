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
import { Info } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import type { PricingModel } from '../types'

export function ModelDetailsApps(_props: { model: PricingModel }) {
  const { t } = useTranslation()
  return (
    <div
      data-model-detail-section='app-rankings'
      className='text-muted-foreground flex items-start gap-2 rounded-lg border border-dashed p-4 text-sm'
    >
      <Info className='mt-0.5 size-4 shrink-0' aria-hidden='true' />
      <div>
        <p className='text-foreground font-medium'>
          {t('App usage rankings are not available.')}
        </p>
        <p className='mt-1 text-xs'>
          {t('This gateway does not expose app-level usage data yet.')}
        </p>
      </div>
    </div>
  )
}

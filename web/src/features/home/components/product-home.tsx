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
import { Footer } from '@/components/layout/components/footer'
import { getSystemVersion } from '@/features/auth/lib/system-status-brand'
import { useStatus } from '@/hooks/use-status'
import { useSystemConfig } from '@/hooks/use-system-config'

import {
  getHomeApiEndpoint,
  getHomeDocsUrl,
  getHomeEntryPoint,
} from '../lib/home-entry'
import { HomeSignalField } from './home-signal-field'
import { ProductHero } from './product-hero'
import { BrandReveal } from './sections/brand-reveal'
import { BrandStatement } from './sections/brand-statement'
import { OperatingInformation } from './sections/operating-information'
import { OperatingRules } from './sections/operating-rules'
import { RequestJourney } from './sections/request-journey'

interface ProductHomeProps {
  isAuthenticated: boolean
}

export function ProductHome(props: ProductHomeProps) {
  const { status, loading: statusLoading } = useStatus()
  const { systemName, logo, loading } = useSystemConfig()
  const entryPoint = getHomeEntryPoint(props.isAuthenticated, status)
  const apiEndpoint = getHomeApiEndpoint(
    status,
    typeof window === 'undefined' ? '' : window.location.origin
  )
  const docsUrl = getHomeDocsUrl(
    status,
    typeof window === 'undefined' ? 'http://localhost' : window.location.origin
  )

  return (
    <main data-home-page='true' className='relative overflow-x-clip'>
      <div className='bg-background relative z-10'>
        <div
          data-testid='home-motion-stage'
          className='home-motion-stage home-entry-started relative isolate'
        >
          <div
            data-testid='home-motion-layer'
            className='pointer-events-none absolute inset-0 z-0'
          >
            <div
              data-testid='home-motion-background'
              className='sticky top-0 h-dvh overflow-hidden'
            >
              <HomeSignalField className='home-entry-reveal-layer opacity-90' />
            </div>
          </div>

          <div data-testid='home-motion-content' className='relative z-10'>
            <div data-testid='home-hero-stage'>
              <ProductHero
                apiEndpoint={apiEndpoint}
                docsUrl={docsUrl}
                entryPoint={entryPoint}
                loading={loading}
                logo={logo}
                statusLoading={statusLoading && !status}
                statusReady={Boolean(status)}
                systemName={systemName}
                version={getSystemVersion(status)}
              />
              <OperatingInformation
                apiEndpoint={apiEndpoint}
                docsUrl={docsUrl}
              />
            </div>
            <OperatingRules />
            <BrandStatement />
          </div>
        </div>
        <RequestJourney />
        <Footer />
      </div>
      <BrandReveal name={systemName} />
    </main>
  )
}

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
import { describe, expect, test } from 'vitest'

import {
  DEFAULT_CURRENCY_CONFIG,
  migratePersistedSystemConfig,
} from '../system-config-store'

describe('system config persistence migration', () => {
  test.each(['New API', 'NewAPI', 'Lietio API'])(
    'replaces the system name alias %s with the presentation brand',
    (systemName) => {
      const migrated = migratePersistedSystemConfig({
        config: {
          systemName,
          logo: '/legacy-logo.png',
          currency: DEFAULT_CURRENCY_CONFIG,
        },
        loadedLogoUrl: '/legacy-logo.png',
      })

      expect(migrated.config.systemName).toBe('Lietio')
      expect(migrated.config.logo).toBe('/legacy-logo.png')
      expect(migrated.loadedLogoUrl).toBe('/legacy-logo.png')
    }
  )

  test('replaces the upstream default logo with the Lietio mark', () => {
    const migrated = migratePersistedSystemConfig({
      config: {
        systemName: 'New API',
        logo: '/logo.png',
        currency: DEFAULT_CURRENCY_CONFIG,
      },
      loadedLogoUrl: '/logo.png',
    })

    expect(migrated.config.logo).toBe('/lietio-mark.svg')
    expect(migrated.loadedLogoUrl).toBe('/lietio-mark.svg')
  })

  test('preserves an administrator-configured site name and settings', () => {
    const migrated = migratePersistedSystemConfig({
      config: {
        systemName: 'Atelier Console',
        logo: '/atelier.png',
        demoSiteEnabled: true,
        currency: {
          ...DEFAULT_CURRENCY_CONFIG,
          quotaDisplayType: 'CNY',
          usdExchangeRate: 7.1,
        },
      },
      loadedLogoUrl: '/atelier.png',
    })

    expect(migrated.config.systemName).toBe('Atelier Console')
    expect(migrated.config.demoSiteEnabled).toBe(true)
    expect(migrated.config.currency.quotaDisplayType).toBe('CNY')
    expect(migrated.config.currency.usdExchangeRate).toBe(7.1)
  })
})

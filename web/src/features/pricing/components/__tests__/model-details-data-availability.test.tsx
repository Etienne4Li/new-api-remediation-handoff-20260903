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
import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'

import { ModelDetailsRateLimitsUnavailable } from '../model-details-api'
import { ModelDetailsApps } from '../model-details-apps'

describe('model detail data availability', () => {
  test('does not fabricate app usage rankings when the backend has no data', () => {
    render(<ModelDetailsApps model={{} as never} />)

    expect(
      screen.getByText('App usage rankings are not available.')
    ).toBeVisible()
    expect(
      screen.getByText('This gateway does not expose app-level usage data yet.')
    ).toBeVisible()
    expect(screen.queryByText('Cline')).not.toBeInTheDocument()
  })

  test('does not fabricate model-specific rate limits', () => {
    render(<ModelDetailsRateLimitsUnavailable />)

    expect(
      screen.getByText(
        'Model-specific rate limits are not published by this gateway.'
      )
    ).toBeVisible()
    expect(screen.queryByText('RPM')).not.toBeInTheDocument()
    expect(screen.queryByText('TPM')).not.toBeInTheDocument()
    expect(screen.queryByText('RPD')).not.toBeInTheDocument()
  })
})

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

import { SectionPageLayout } from '../section-page-layout'

describe('SectionPageLayout', () => {
  test('exposes editorial hierarchy and fixed workspace behavior', () => {
    render(
      <SectionPageLayout fixedContent variant='editorial' density='compact'>
        <SectionPageLayout.Title>API Keys</SectionPageLayout.Title>
        <SectionPageLayout.Description>
          Create and audit credentials.
        </SectionPageLayout.Description>
        <SectionPageLayout.Actions>
          <button type='button'>Create</button>
        </SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          <div>Workspace</div>
        </SectionPageLayout.Content>
      </SectionPageLayout>
    )

    const layout = document.querySelector('[data-slot="section-page-layout"]')
    expect(layout).toBeInstanceOf(HTMLDivElement)
    expect(layout).toHaveAttribute('data-variant', 'editorial')
    expect(layout).toHaveAttribute('data-density', 'compact')
    expect(layout).toHaveAttribute('data-fixed-content', 'true')
    expect(screen.getByRole('heading', { name: 'API Keys' })).toBeVisible()
    expect(screen.getByText('Create and audit credentials.')).toBeVisible()
    expect(screen.getByRole('button', { name: 'Create' })).toBeVisible()
    expect(screen.getByText('Workspace')).toBeVisible()
  })
})

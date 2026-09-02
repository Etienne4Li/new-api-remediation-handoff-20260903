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

import { WebPreview, WebPreviewBody } from '../web-preview'

describe('WebPreview iframe isolation', () => {
  test('keeps the preview in an opaque origin even when a caller requests same-origin access', () => {
    render(
      <WebPreview defaultUrl='https://preview.example'>
        <WebPreviewBody sandbox='allow-scripts allow-same-origin' />
      </WebPreview>
    )

    const iframe = screen.getByTitle('Preview')
    expect(iframe).toHaveAttribute(
      'sandbox',
      'allow-scripts allow-forms allow-popups allow-presentation'
    )
    expect(iframe).toHaveAttribute('referrerpolicy', 'no-referrer')
  })
})

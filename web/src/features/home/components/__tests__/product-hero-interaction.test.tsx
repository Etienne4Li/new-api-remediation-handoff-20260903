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
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ProductHero } from '../product-hero'

const copyToClipboard = vi.hoisted(() => vi.fn())

vi.mock('@tanstack/react-router', () => ({
  Link: ({
    children,
    to,
    ...props
  }: React.AnchorHTMLAttributes<HTMLAnchorElement> & { to: string }) => (
    <a href={to} data-router-link='true' {...props}>
      {children}
    </a>
  ),
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

vi.mock('@/hooks/use-copy-to-clipboard', () => ({
  useCopyToClipboard: () => ({ copiedText: null, copyToClipboard }),
}))

describe('ProductHero endpoint actions', () => {
  beforeEach(() => {
    copyToClipboard.mockReset()
    copyToClipboard.mockResolvedValue(true)
  })

  it('copies the configured API endpoint when the copy button is clicked', async () => {
    const user = userEvent.setup()

    render(
      <ProductHero
        apiEndpoint='https://gateway.example.com'
        docsUrl='/docs'
        entryPoint={{ route: '/sign-up', labelKey: 'Get Started' }}
        loading={false}
        logo='/logo.png'
        statusLoading={false}
        statusReady
        systemName='New API'
        version='2.4.1'
      />
    )

    await user.click(screen.getByRole('button', { name: 'Copy API endpoint' }))

    expect(copyToClipboard).toHaveBeenCalledOnce()
    expect(copyToClipboard).toHaveBeenCalledWith('https://gateway.example.com')
  })

  it('routes same-origin documentation paths through the application router', () => {
    render(
      <ProductHero
        docsUrl='/docs/getting-started'
        entryPoint={{ route: '/sign-up', labelKey: 'Get Started' }}
        loading={false}
        logo='/logo.png'
        statusLoading={false}
        statusReady
        systemName='New API'
        version='2.4.1'
      />
    )

    expect(screen.getByRole('button', { name: 'Docs' })).toHaveAttribute(
      'data-router-link',
      'true'
    )
  })

  it('opens external documentation links in an isolated tab', () => {
    render(
      <ProductHero
        docsUrl='https://docs.example.com/start'
        entryPoint={{ route: '/sign-up', labelKey: 'Get Started' }}
        loading={false}
        logo='/logo.png'
        statusLoading={false}
        statusReady
        systemName='New API'
        version='2.4.1'
      />
    )

    const docsLink = screen.getByRole('button', { name: 'Docs' })

    expect(docsLink).not.toHaveAttribute('data-router-link')
    expect(docsLink).toHaveAttribute('target', '_blank')
    expect(docsLink).toHaveAttribute('rel', 'noopener noreferrer')
  })
})

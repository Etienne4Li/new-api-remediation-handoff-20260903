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
import { describe, expect, it, vi } from 'vitest'

import { ProductHero } from '../product-hero'

vi.mock('@tanstack/react-router', () => ({
  Link: ({
    children,
    to,
    ...props
  }: React.AnchorHTMLAttributes<HTMLAnchorElement> & { to: string }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

describe('ProductHero responsive layout', () => {
  it('uses compact spacing while leaving a preview of the next section', () => {
    render(
      <ProductHero
        description='Private gateway for the Lietio team'
        docsUrl='/docs'
        entryPoint={{ route: '/sign-up', labelKey: 'Get Started' }}
        loading={false}
        logo='/logo.png'
        statusLoading={false}
        statusReady
        systemName='Lietio API'
        version='2.4.1'
      />
    )

    const hero = screen.getByRole('region', { name: 'Lietio API' })

    expect(hero).toHaveClass(
      'flex',
      'min-h-[calc(100svh-8rem)]',
      'overflow-hidden',
      'pt-10',
      'sm:pt-12',
      'lg:min-h-[calc(100dvh-4rem)]'
    )
    expect(hero).not.toHaveClass(
      'sticky',
      'lg:sticky',
      'lg:h-[calc(100svh-4rem)]'
    )
  })

  it('keeps the gateway endpoint and primary action visible on phones', () => {
    render(
      <ProductHero
        description='Private gateway for the Lietio team'
        docsUrl='/docs'
        entryPoint={{ route: '/sign-up', labelKey: 'Get Started' }}
        loading={false}
        logo='/logo.png'
        statusLoading={false}
        statusReady
        systemName='Lietio API'
        version='2.4.1'
      />
    )

    expect(screen.getByRole('heading', { name: 'Lietio API' })).toBeVisible()
    expect(
      screen.getByText('Private gateway for the Lietio team')
    ).toBeVisible()
    expect(screen.getByText(/Online\s+Version 2\.4\.1/)).toHaveClass('sr-only')
    expect(screen.getByRole('button', { name: 'Get Started' })).toHaveAttribute(
      'href',
      '/sign-up'
    )
  })
})

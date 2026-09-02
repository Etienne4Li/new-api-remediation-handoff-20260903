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
import type { AnchorHTMLAttributes, ReactNode } from 'react'
import { describe, expect, test, vi } from 'vitest'

import { Footer } from '../footer'

vi.mock('@tanstack/react-router', () => ({
  Link: ({
    to,
    children,
    ...props
  }: AnchorHTMLAttributes<HTMLAnchorElement> & {
    to: string
    children?: ReactNode
  }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

const systemConfig = vi.hoisted(() => ({
  footerHtml: '',
}))

vi.mock('@/hooks/use-system-config', () => ({
  useSystemConfig: () => ({
    systemName: 'New API',
    logo: '/logo.png',
    footerHtml: systemConfig.footerHtml,
    demoSiteEnabled: false,
  }),
}))

vi.mock('@/hooks/use-status', () => ({
  useStatus: () => ({
    status: null,
  }),
}))

describe('Footer custom HTML', () => {
  test('does not render active content or dangerous links from configured HTML', () => {
    systemConfig.footerHtml =
      '<p onclick="alert(1)">Welcome</p><script>alert(2)</script><iframe src="https://evil.example"></iframe><img src="x" onerror="alert(3)"><a href="javascript:alert(4)">bad</a>'

    const { container } = render(<Footer />)

    expect(screen.getByText('Welcome')).toBeInTheDocument()
    const customFooter = container.querySelector('.custom-footer')
    expect(customFooter).not.toBeNull()
    expect(customFooter?.querySelector('script')).toBeNull()
    expect(customFooter?.querySelector('iframe')).toBeNull()
    expect(customFooter?.querySelector('[onclick]')).toBeNull()
    expect(customFooter?.querySelector('[onerror]')).toBeNull()
    const unsafeLink = customFooter?.querySelector('a')
    expect(unsafeLink).not.toBeNull()
    expect(unsafeLink).not.toHaveAttribute('href')
  })

  test('keeps basic formatting and safe links', () => {
    systemConfig.footerHtml =
      '<strong>Welcome</strong><a href="https://example.com" target="_blank">Docs</a>'

    const { container } = render(<Footer />)

    expect(screen.getByText('Welcome')).toBeInTheDocument()
    const link = screen.getByRole('link', { name: 'Docs' })
    expect(link).toHaveAttribute('href', 'https://example.com')
    expect(link).not.toHaveAttribute('target')
    expect(container.querySelector('strong')).toHaveTextContent('Welcome')
  })
})

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

import { SkipToMain } from '@/components/skip-to-main'
import { SidebarInset, SidebarProvider } from '@/components/ui/sidebar'

import { Header } from '../header'
import { Main } from '../main'
import { NavGroup } from '../nav-group'
import { TopNav } from '../top-nav'

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
  useLocation: ({
    select,
  }: {
    select: (location: { href: string }) => string
  }) => select({ href: '/dashboard' }),
}))

describe('authenticated shell layout', () => {
  test('keeps the sticky header legible over scrolling content', () => {
    render(
      <SidebarProvider>
        <Header>Header content</Header>
      </SidebarProvider>
    )

    const header = screen.getByRole('banner')
    expect(header).toHaveClass('border-b', 'backdrop-blur-md')
    expect(header.firstElementChild).toHaveClass('@container/app-header')
    expect(screen.getByRole('button', { name: 'Toggle Sidebar' })).toHaveClass(
      'md:hidden'
    )
  })

  test('lets the authenticated brand own the sidebar control', () => {
    render(
      <SidebarProvider>
        <Header showSidebarTrigger={false}>Brand control</Header>
      </SidebarProvider>
    )

    expect(
      screen.queryByRole('button', { name: 'Toggle Sidebar' })
    ).not.toBeInTheDocument()
  })

  test('keeps one main landmark and connects the skip link to it', () => {
    render(
      <SidebarProvider>
        <SkipToMain />
        <SidebarInset id='content' tabIndex={-1}>
          <Main>Page workspace</Main>
        </SidebarInset>
      </SidebarProvider>
    )

    const main = screen.getByRole('main')
    expect(main).toHaveAttribute('id', 'content')
    expect(main).toHaveAttribute('tabindex', '-1')
    expect(screen.getByRole('link', { name: 'Skip to Main' })).toHaveAttribute(
      'href',
      '#content'
    )
    expect(main.querySelector('main')).toBeNull()
  })

  test('exposes both container-responsive navigation modes', () => {
    render(
      <TopNav
        links={[
          { title: 'Dashboard', href: '/dashboard' },
          {
            title: 'Documentation',
            href: 'https://docs.example.com',
            external: true,
          },
        ]}
      />
    )

    const menuButton = screen.getByRole('button', {
      name: 'Toggle navigation menu',
    })
    expect(menuButton.parentElement).toHaveClass('@7xl/app-header:hidden')
    expect(screen.getByRole('navigation')).toHaveClass('@7xl/app-header:flex')
  })

  test('does not reserve header space when navigation is empty', () => {
    const { container } = render(<TopNav links={[]} />)

    expect(container).toBeEmptyDOMElement()
  })

  test('keeps sidebar groups compact while preserving their navigation links', () => {
    render(
      <SidebarProvider>
        <NavGroup
          id='workspace'
          title='Workspace'
          items={[{ title: 'Dashboard', url: '/dashboard' }]}
        />
      </SidebarProvider>
    )

    const label = screen.getByText('Workspace')
    expect(label).toHaveClass('h-7', 'font-semibold', 'uppercase')
    expect(label.closest('[data-slot="sidebar-group"]')).toHaveClass(
      'px-2',
      'py-1.5'
    )
    expect(screen.getByRole('link', { name: 'Dashboard' })).toHaveAttribute(
      'href',
      '/dashboard'
    )
  })
})

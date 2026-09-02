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
import type { AnchorHTMLAttributes, HTMLAttributes, ReactNode } from 'react'
import { describe, expect, test, vi } from 'vitest'

import { BrandStatement } from '../brand-statement'
import { OperatingInformation } from '../operating-information'
import { OperatingRules } from '../operating-rules'

vi.mock('@tanstack/react-router', () => ({
  Link: (
    props: AnchorHTMLAttributes<HTMLAnchorElement> & {
      children: ReactNode
      to: string
    }
  ) => (
    <a href={props.to} className={props.className}>
      {props.children}
    </a>
  ),
}))

vi.mock('@/components/animate-in-view', () => ({
  AnimateInView: (props: HTMLAttributes<HTMLDivElement>) => (
    <div className={props.className}>{props.children}</div>
  ),
}))

vi.mock('@/features/dashboard/hooks/use-status-data', () => ({
  useApiInfo: () => ({ items: [], loading: false }),
}))

vi.mock('@/hooks/use-status', () => ({
  useStatus: () => ({ status: null }),
}))

vi.mock('motion/react', () => ({
  motion: { span: 'span' },
  useReducedMotion: () => true,
  useScroll: () => ({ scrollYProgress: 0 }),
  useTransform: () => 0,
}))

describe('home editorial section layout', () => {
  test('keeps the operating introduction and responsive information matrix', () => {
    render(<OperatingInformation docsUrl='/docs' />)

    const heading = screen.getByRole('heading', {
      name: 'Clear routes, current prices, public references',
    })
    const section = heading.closest('section')
    const introduction = heading.parentElement
    const matrix = screen.getAllByRole('article')[0]?.parentElement

    expect(section).toHaveClass('md:py-32')
    expect(introduction).toHaveClass('max-w-lg', 'sm:mb-16')
    expect(matrix).toHaveClass(
      'md:grid-cols-3',
      'md:gap-px',
      'md:bg-border/40',
      'md:overflow-hidden',
      'md:border',
      'rounded-xl'
    )
    expect(screen.getAllByRole('article')).toHaveLength(6)
    expect(
      screen.getByText('Public API routes are not configured yet')
    ).toBeInTheDocument()
    expect(screen.getByText('No public reports available')).toBeInTheDocument()
    expect(
      screen.queryByText('Evidence that helps us investigate')
    ).not.toBeInTheDocument()
    expect(screen.queryByText('Useful evidence')).not.toBeInTheDocument()
    expect(
      screen.queryByText('Cannot be verified alone')
    ).not.toBeInTheDocument()
  })

  test('renders three independent 448px rule cards on desktop', () => {
    render(<OperatingRules />)

    const heading = screen.getByRole('heading', {
      name: /Clear boundaries, current (?:practices|rules)/,
    })
    const section = heading.closest('section')
    const cards = [
      'Capacity protection',
      'Fair use',
      'Incidents and corrections',
    ].map((name) => screen.getByRole('heading', { name }).parentElement)
    const cardGrid = cards[0]?.parentElement

    expect(section).toHaveClass('border-t', 'bg-muted/30', 'md:py-32')
    expect(cardGrid).toHaveClass('md:grid-cols-3', 'md:gap-8')
    expect(cards).toHaveLength(3)
    for (const card of cards) {
      expect(card).toHaveClass('md:min-h-[28rem]', 'rounded-xl', 'md:p-8')
    }
  })

  test('renders the statement as word-level spans with a serif gateway accent', () => {
    render(<BrandStatement />)

    const statement = document.querySelector('section p')
    expect(statement).not.toBeNull()
    expect(statement?.closest('section')).toHaveClass('isolate')
    expect(statement?.closest('section')).not.toHaveClass('bg-background')
    const statementText = statement?.textContent?.replaceAll(/\s+/g, ' ').trim()

    expect(statementText).toBe('Every model. One gateway. No theater.')
    expect(statement).toHaveClass(
      'text-[clamp(2.5rem,6.5vw,5.75rem)]',
      'text-center',
      'text-balance'
    )
    expect(
      [...(statement?.querySelectorAll('.font-serif') ?? [])].map(
        (word) => word.textContent
      )
    ).toEqual(['One', 'gateway.'])
  })
})

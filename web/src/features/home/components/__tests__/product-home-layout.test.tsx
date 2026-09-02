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

import { ProductHome } from '../product-home'

vi.mock('@/components/layout/components/footer', () => ({
  Footer: () => <footer data-testid='footer' />,
}))

vi.mock('@/features/auth/lib/system-status-brand', () => ({
  getSystemVersion: () => null,
}))

vi.mock('@/hooks/use-status', () => ({
  useStatus: () => ({ loading: false, status: null }),
}))

vi.mock('@/hooks/use-system-config', () => ({
  useSystemConfig: () => ({
    loading: false,
    logo: '/lietio-mark.svg',
    systemName: 'Lietio',
  }),
}))

vi.mock('../product-hero', () => ({
  ProductHero: () => <section data-testid='product-hero' />,
}))

vi.mock('../home-signal-field', () => ({
  HomeSignalField: () => <canvas data-testid='home-signal-field' />,
}))

vi.mock('../sections/operating-information', () => ({
  OperatingInformation: () => <section data-testid='operating-information' />,
}))

vi.mock('../sections/operating-rules', () => ({
  OperatingRules: () => <section data-testid='operating-rules' />,
}))

vi.mock('../sections/brand-statement', () => ({
  BrandStatement: () => <section data-testid='brand-statement' />,
}))

vi.mock('../sections/request-journey', () => ({
  RequestJourney: () => <section data-testid='request-journey' />,
}))

vi.mock('../sections/brand-reveal', () => ({
  BrandReveal: () => <div data-testid='brand-reveal' />,
}))

describe('ProductHome scroll layers', () => {
  it('keeps the animated field behind the hero, information, rules, and statement', () => {
    render(<ProductHome isAuthenticated={false} />)

    const motionStage = screen.getByTestId('home-motion-stage')
    const motionLayer = screen.getByTestId('home-motion-layer')
    const motionBackground = screen.getByTestId('home-motion-background')
    const motionContent = screen.getByTestId('home-motion-content')
    const stage = screen.getByTestId('home-hero-stage')
    const rules = screen.getByTestId('operating-rules')
    const statement = screen.getByTestId('brand-statement')

    expect(motionStage).toContainElement(motionLayer)
    expect(motionLayer).toContainElement(motionBackground)
    expect(motionBackground).toContainElement(
      screen.getByTestId('home-signal-field')
    )
    expect(motionLayer).toHaveClass('absolute', 'inset-0')
    expect(motionBackground).toHaveClass('sticky', 'top-0', 'h-dvh')
    expect(motionBackground).not.toHaveClass('-mb-[100svh]', 'h-svh')
    expect(motionStage).toContainElement(motionContent)
    expect(motionContent).toContainElement(stage)
    expect(motionContent).toContainElement(rules)
    expect(motionContent).toContainElement(statement)
    expect(stage).toContainElement(screen.getByTestId('product-hero'))
    expect(stage).toContainElement(screen.getByTestId('operating-information'))
    expect(stage).not.toContainElement(rules)
    expect(stage.nextElementSibling).toBe(rules)
    expect(rules.nextElementSibling).toBe(statement)
    expect(motionStage.nextElementSibling).toBe(
      screen.getByTestId('request-journey')
    )
  })
})

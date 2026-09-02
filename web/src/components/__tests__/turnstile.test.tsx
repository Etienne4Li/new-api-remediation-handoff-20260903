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
import { render, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { Turnstile } from '../turnstile'

const originalResizeObserver = globalThis.ResizeObserver
const originalGetBoundingClientRect =
  HTMLElement.prototype.getBoundingClientRect

function setContainerWidth(width: number) {
  Object.defineProperty(HTMLElement.prototype, 'getBoundingClientRect', {
    configurable: true,
    value: () => ({
      bottom: 0,
      height: 0,
      left: 0,
      right: width,
      top: 0,
      width,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    }),
  })
}

afterEach(() => {
  delete window.turnstile
  Object.defineProperty(globalThis, 'ResizeObserver', {
    configurable: true,
    value: originalResizeObserver,
  })
  Object.defineProperty(HTMLElement.prototype, 'getBoundingClientRect', {
    configurable: true,
    value: originalGetBoundingClientRect,
  })
})

describe('Turnstile', () => {
  test('uses the compact widget when the auth form is narrower than 300px', async () => {
    setContainerWidth(246)
    const renderWidget = vi.fn<
      (element: HTMLElement, options: Record<string, unknown>) => string
    >(() => 'compact-widget')
    const removeWidget = vi.fn()
    window.turnstile = { render: renderWidget, remove: removeWidget }

    const view = render(
      <Turnstile siteKey='site-key' onVerify={() => undefined} />
    )

    await waitFor(() => expect(renderWidget).toHaveBeenCalledTimes(1))
    expect(renderWidget.mock.calls[0]?.[1]).toMatchObject({
      sitekey: 'site-key',
      size: 'compact',
    })
    expect(view.container.firstElementChild).toHaveClass(
      'w-full',
      'min-w-0',
      'max-w-full',
      'justify-center'
    )

    view.unmount()
    expect(removeWidget).toHaveBeenCalledWith('compact-widget')
  })

  test('uses the flexible widget when its container can fit 300px', async () => {
    setContainerWidth(316)
    const renderWidget = vi.fn<
      (element: HTMLElement, options: Record<string, unknown>) => string
    >(() => 'flexible-widget')
    window.turnstile = { render: renderWidget, remove: vi.fn() }

    render(<Turnstile siteKey='site-key' onVerify={() => undefined} />)

    await waitFor(() => expect(renderWidget).toHaveBeenCalledTimes(1))
    expect(renderWidget.mock.calls[0]?.[1]).toMatchObject({
      sitekey: 'site-key',
      size: 'flexible',
    })
  })
})

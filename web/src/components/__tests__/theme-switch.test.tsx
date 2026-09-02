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
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { ThemeProvider } from '@/context/theme-provider'

import { ThemeSwitch } from '../theme-switch'

const defaultMatchMedia = window.matchMedia

function renderThemeSwitch() {
  return render(
    <ThemeProvider defaultTheme='light' storageKey='theme-switch-test'>
      <ThemeSwitch />
    </ThemeProvider>
  )
}

async function openThemeMenu() {
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Toggle theme' }))
  return user
}

async function chooseDarkTheme() {
  const user = await openThemeMenu()
  await user.click(await screen.findByRole('menuitem', { name: 'Dark' }))
}

afterEach(() => {
  Object.defineProperty(document, 'startViewTransition', {
    configurable: true,
    value: undefined,
  })
  Object.defineProperty(document.documentElement, 'animate', {
    configurable: true,
    value: undefined,
  })
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: defaultMatchMedia,
  })
  document.documentElement.classList.remove('light', 'dark')
})

describe('ThemeSwitch', () => {
  test('animates the new theme outward from the trigger position', async () => {
    let rootThemeAfterUpdateReturned: string | null = null
    const animate = vi.fn<
      (
        keyframes: Keyframe[] | PropertyIndexedKeyframes | null,
        options?: number | KeyframeAnimationOptions
      ) => Animation
    >(() => ({}) as Animation)
    const startViewTransition = vi.fn<(update: () => void) => ViewTransition>(
      (update) => {
        update()
        rootThemeAfterUpdateReturned =
          document.documentElement.classList.contains('dark') ? 'dark' : null
        return {
          finished: Promise.resolve(),
          ready: Promise.resolve(),
          skipTransition: vi.fn(),
          updateCallbackDone: Promise.resolve(),
        } as unknown as ViewTransition
      }
    )
    Object.defineProperty(document, 'startViewTransition', {
      configurable: true,
      value: startViewTransition,
    })
    Object.defineProperty(document.documentElement, 'animate', {
      configurable: true,
      value: animate,
    })

    renderThemeSwitch()
    const user = await openThemeMenu()
    fireEvent.pointerDown(
      screen.getByRole('button', { name: 'Toggle theme' }),
      { clientX: 48, clientY: 64 }
    )
    await user.click(await screen.findByRole('menuitem', { name: 'Dark' }))

    await waitFor(() => expect(animate).toHaveBeenCalledTimes(1))
    expect(startViewTransition).toHaveBeenCalledTimes(1)
    expect(rootThemeAfterUpdateReturned).toBe('dark')
    expect(animate.mock.calls[0]?.[0]).toEqual({
      clipPath: [
        'circle(0px at 48px 64px)',
        expect.stringMatching(/^circle\(.+px at 48px 64px\)$/),
      ],
    })
    expect(animate.mock.calls[0]?.[1]).toMatchObject({
      duration: 620,
      pseudoElement: '::view-transition-new(root)',
    })
  })

  test('switches immediately when reduced motion is requested', async () => {
    const startViewTransition = vi.fn()
    Object.defineProperty(document, 'startViewTransition', {
      configurable: true,
      value: startViewTransition,
    })
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: (query: string): MediaQueryList => ({
        ...defaultMatchMedia(query),
        matches: query === '(prefers-reduced-motion: reduce)',
      }),
    })

    renderThemeSwitch()
    await chooseDarkTheme()

    await waitFor(() => expect(document.documentElement).toHaveClass('dark'))
    expect(startViewTransition).not.toHaveBeenCalled()
  })
})

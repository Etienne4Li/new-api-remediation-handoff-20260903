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

/**
 * Helpers for the "a Button used as a card must grow with its content"
 * contract.
 *
 * `buttonVariants` gives every `size='default'` button a fixed `h-8`, and a
 * `min-h-*` on the call site cannot undo it: they are different CSS
 * properties, so tailwind-merge keeps both. The box then sits at its floor and
 * anything taller — a second line, a discount row, a longer translation —
 * spills past the border instead of pushing it open. `justify-center` (also a
 * base class) splits that overflow across the top and bottom edges.
 *
 * A Button that stacks content therefore has to opt out of the size variant's
 * height with `h-auto`. `min-h-*` may stay: a floor is fine, a fixed height is
 * not.
 *
 * jsdom does not lay out, so overflow itself is not observable in a unit test.
 * These helpers pin the class contract instead — re-locking the height is the
 * only way the overflow can come back.
 */

/** `h-*` / `size-*` values that do not pin the box to a fixed height. */
const CONTENT_DRIVEN_HEIGHTS = new Set([
  'auto',
  'full',
  'fit',
  'min',
  'max',
  'screen',
])

/**
 * Classes on `className` that lock the element's own block height.
 *
 * Skips arbitrary variants that reach into descendants (`[&_svg…]:size-4` from
 * the Button base sizes the icon, not the button) and keeps `min-h-*` /
 * `max-h-*`, which constrain rather than fix the height.
 */
export function lockedHeightClasses(className: string): string[] {
  return className
    .split(/\s+/)
    .filter(Boolean)
    .filter((token) => {
      if (token.includes('[&')) return false
      // Drop any variant prefix: `sm:`, `dark:`, `data-[state=open]:` …
      const utility = token.slice(token.lastIndexOf(':') + 1)
      const match = /^(?:h|size)-(.+)$/.exec(utility)
      return match !== null && !CONTENT_DRIVEN_HEIGHTS.has(match[1])
    })
}

/** Whether `className` opts out of the size variant's fixed height. */
export function optsOutOfFixedHeight(className: string): boolean {
  return className.split(/\s+/).includes('h-auto')
}

/**
 * Describe how `className` violates the contract, or `null` if it holds.
 *
 * Returns a message rather than asserting so the same check can back both a
 * DOM assertion and the repo-wide source scan.
 */
export function describeHeightLock(className: string): string | null {
  const locked = lockedHeightClasses(className)
  if (locked.length > 0) {
    return `locks its height with ${locked.map((c) => `\`${c}\``).join(', ')} — content taller than that overflows the border instead of growing the box`
  }
  if (!optsOutOfFixedHeight(className)) {
    return "has no `h-auto`, so the size variant's fixed `h-8` survives (`min-h-*` cannot override it — different CSS property)"
  }
  return null
}

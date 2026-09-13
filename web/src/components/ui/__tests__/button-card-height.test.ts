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
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, test } from 'vitest'

import {
  describeHeightLock,
  lockedHeightClasses,
} from '@/test-utils/height-contract'
import { scanButtonCards } from '@/test-utils/scan-button-cards'

const SRC = resolve(dirname(fileURLToPath(import.meta.url)), '../../..')

/**
 * Buttons that hold a fixed height on purpose, because their content cannot
 * grow. Add an entry only with a reason that survives a translation change.
 */
const INTENTIONALLY_FIXED = new Map<string, string>([
  [
    'features/profile/components/checkin-calendar-card.tsx',
    // The check-in dot beside the number is `absolute`, so the single
    // `<span>{dayNum}</span>` is the only child in flow. A uniform square is
    // the point of a calendar grid.
    'check-in calendar day cell: square grid cell holding a day number',
  ],
])

describe('lockedHeightClasses', () => {
  test('flags fixed heights and ignores floors, ceilings and escapes', () => {
    expect(lockedHeightClasses('h-8 min-h-16 max-h-40 h-auto')).toEqual(['h-8'])
    expect(lockedHeightClasses('sm:h-10 dark:size-9')).toEqual([
      'sm:h-10',
      'dark:size-9',
    ])
    expect(lockedHeightClasses('h-[72px] size-10')).toEqual([
      'h-[72px]',
      'size-10',
    ])
    expect(lockedHeightClasses('h-full size-full h-fit min-h-11')).toEqual([])
    // The Button base sizes descendant icons; that is not this box's height.
    expect(lockedHeightClasses("[&_svg:not([class*='size-'])]:size-4")).toEqual(
      []
    )
  })

  test('rejects the class list that shipped the overflow', () => {
    // Verbatim from the card that overflowed in production: `min-h-*` alone,
    // no `h-auto`, so the variant's `h-8` survived tailwind-merge.
    expect(
      describeHeightLock(
        'flex min-h-16 flex-col items-start rounded-lg px-3 py-2.5 text-left whitespace-normal sm:min-h-[72px] sm:p-4'
      )
    ).toMatch(/no `h-auto`/)

    // …and still rejects it if someone "fixes" it by raising the floor.
    expect(
      describeHeightLock('flex min-h-24 flex-col sm:min-h-[96px]')
    ).toMatch(/no `h-auto`/)

    expect(describeHeightLock('flex h-auto min-h-16 flex-col')).toBeNull()
  })
})

describe('no Button is used as a card with a locked height', () => {
  test('every stacking Button lets its content set the height', () => {
    const unexpected = scanButtonCards(SRC).filter(
      (finding) => !INTENTIONALLY_FIXED.has(finding.file)
    )

    // Rendered as text so a failure names the file, the line and the fix.
    expect(
      unexpected.map(
        (f) =>
          `${f.file}:${f.line} — ${f.problem}, but its content stacks (${f.stacksBecause.join('; ')}). Add \`h-auto\` and keep \`min-h-*\` as the floor.`
      )
    ).toEqual([])
  })

  test('the allowlist stays honest', () => {
    // If one of these stops being flagged, the entry is dead and should go —
    // otherwise the allowlist slowly turns into a place to hide regressions.
    const flagged = new Set(scanButtonCards(SRC).map((f) => f.file))
    for (const file of INTENTIONALLY_FIXED.keys()) {
      expect(flagged, `${file} is allowlisted but no longer flagged`).toContain(
        file
      )
    }
  })
})

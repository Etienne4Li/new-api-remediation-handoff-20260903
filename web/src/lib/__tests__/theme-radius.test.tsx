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
import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { useThemeRadiusPx } from '../theme-radius'

const originalRequestAnimationFrame = window.requestAnimationFrame
const originalCancelAnimationFrame = window.cancelAnimationFrame

afterEach(() => {
  window.requestAnimationFrame = originalRequestAnimationFrame
  window.cancelAnimationFrame = originalCancelAnimationFrame
  vi.restoreAllMocks()
})

describe('useThemeRadiusPx', () => {
  test('remeasures the CSS radius when the refresh key changes', () => {
    let nextFrameId = 1
    let resolvedRadius = '8px'
    const frames = new Map<number, FrameRequestCallback>()
    window.requestAnimationFrame = vi.fn((callback: FrameRequestCallback) => {
      const frameId = nextFrameId
      nextFrameId += 1
      frames.set(frameId, callback)
      return frameId
    })
    window.cancelAnimationFrame = vi.fn((frameId: number) => {
      frames.delete(frameId)
    })
    vi.spyOn(window, 'getComputedStyle').mockImplementation(
      () => ({ borderTopLeftRadius: resolvedRadius }) as CSSStyleDeclaration
    )
    const runNextFrame = () => {
      const nextFrame = frames.entries().next().value as
        | [number, FrameRequestCallback]
        | undefined
      if (!nextFrame) return
      frames.delete(nextFrame[0])
      nextFrame[1](performance.now())
    }

    const { result, rerender } = renderHook(
      ({ refreshKey }) => useThemeRadiusPx('--radius-md', refreshKey),
      { initialProps: { refreshKey: 'default:medium' } }
    )

    act(runNextFrame)
    expect(result.current).toBe(8)

    resolvedRadius = '12px'
    rerender({ refreshKey: 'default:large' })
    act(runNextFrame)

    expect(result.current).toBe(12)
  })
})

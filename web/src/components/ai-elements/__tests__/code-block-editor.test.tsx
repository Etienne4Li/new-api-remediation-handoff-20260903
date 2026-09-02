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
import { cleanup, fireEvent, render } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { CodeBlockEditor } from '../code-block'

afterEach(() => {
  cleanup()
})

function editorTree(
  value: string,
  onKeyDown: (event: globalThis.KeyboardEvent) => void = () => undefined
) {
  // A fresh inline onKeyDown per call mirrors PlaygroundMessageEditor, which
  // recreates its handler on every keystroke-driven render.
  return (
    <CodeBlockEditor
      ariaLabel='Edit message'
      language='markdown'
      onChange={() => undefined}
      onKeyDown={onKeyDown}
      value={value}
    />
  )
}

describe('CodeBlockEditor', () => {
  test('keeps the same editor instance when value and onKeyDown change on rerender', () => {
    const { rerender } = render(editorTree('h'))

    const contentBefore = document.querySelector('.cm-content')
    expect(contentBefore).not.toBeNull()

    rerender(editorTree('hi'))

    const contentAfter = document.querySelector('.cm-content')
    // If the EditorView were torn down and rebuilt, the content node would be
    // replaced and the cursor would reset to the document start, making typed
    // characters pile up at the beginning (text appears right-to-left).
    expect(contentAfter).toBe(contentBefore)
    expect(contentAfter?.textContent).toContain('hi')
  })

  test('calls the latest keydown handler after rerender without rebuilding the editor', () => {
    const firstHandler = vi.fn()
    const latestHandler = vi.fn()
    const { rerender } = render(editorTree('hello', firstHandler))
    const contentBefore = document.querySelector('.cm-content')

    rerender(editorTree('hello', latestHandler))
    const contentAfter = document.querySelector('.cm-content')
    expect(contentAfter).toBe(contentBefore)
    if (contentAfter == null) {
      throw new Error('CodeMirror content element was not rendered')
    }

    fireEvent.keyDown(contentAfter, { key: 'Enter' })

    expect(firstHandler).not.toHaveBeenCalled()
    expect(latestHandler).toHaveBeenCalledOnce()
  })
})

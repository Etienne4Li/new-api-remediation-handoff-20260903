/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.
*/
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { ImageDialog } from '../image-dialog'

const { getMock } = vi.hoisted(() => ({
  getMock: vi.fn(),
}))

vi.mock('@/lib/api', () => ({
  api: { get: getMock },
}))

describe('ImageDialog authenticated media loading', () => {
  const createObjectURL = vi.fn(() => 'blob:midjourney-preview')
  const revokeObjectURL = vi.fn()

  beforeEach(() => {
    getMock.mockReset()
    createObjectURL.mockClear()
    revokeObjectURL.mockClear()
    Object.defineProperty(URL, 'createObjectURL', {
      configurable: true,
      value: createObjectURL,
    })
    Object.defineProperty(URL, 'revokeObjectURL', {
      configurable: true,
      value: revokeObjectURL,
    })
    if (!HTMLElement.prototype.getAnimations) {
      Object.defineProperty(HTMLElement.prototype, 'getAnimations', {
        configurable: true,
        value: () => [],
      })
    }
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('fetches same-origin Midjourney images through the authenticated API client', async () => {
    getMock.mockResolvedValue({
      data: new Blob(['png-bytes'], { type: 'image/png' }),
    })

    const rendered = render(
      <ImageDialog
        imageUrl='/mj/image/task-42?rand=123'
        taskId='task-42'
        open
        onOpenChange={() => undefined}
      />
    )

    await waitFor(() => {
      expect(getMock).toHaveBeenCalledWith('/mj/image/task-42?rand=123', {
        responseType: 'blob',
        skipErrorHandler: true,
      })
    })
    await waitFor(() => {
      expect(screen.getByAltText('Generated image')).toHaveAttribute(
        'src',
        'blob:midjourney-preview'
      )
    })

    fireEvent.load(screen.getByAltText('Generated image'))
    expect(rendered.getByAltText('Generated image')).toBeVisible()
  })

  test('does not send the API token to third-party image URLs', async () => {
    render(
      <ImageDialog
        imageUrl='https://cdn.example.test/image.png'
        open
        onOpenChange={() => undefined}
      />
    )

    await waitFor(() => expect(getMock).not.toHaveBeenCalled())
    await waitFor(() => {
      expect(screen.getByAltText('Generated image')).toHaveAttribute(
        'src',
        'https://cdn.example.test/image.png'
      )
    })
  })
})

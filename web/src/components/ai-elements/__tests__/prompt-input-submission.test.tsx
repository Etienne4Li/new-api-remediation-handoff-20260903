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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import {
  PromptInput,
  PromptInputAttachments,
  PromptInputTextarea,
  type PromptInputProps,
} from '../prompt-input'

const originalCreateObjectURL = URL.createObjectURL
const originalRevokeObjectURL = URL.revokeObjectURL

function renderPromptInput(onSubmit: PromptInputProps['onSubmit']) {
  return render(
    <PromptInput onSubmit={onSubmit}>
      <PromptInputTextarea aria-label='Message' name='message' />
      <PromptInputAttachments>
        {(file) => <span>{file.filename}</span>}
      </PromptInputAttachments>
      <button type='submit'>Send</button>
    </PromptInput>
  )
}

beforeEach(() => {
  Object.defineProperty(URL, 'createObjectURL', {
    configurable: true,
    value: vi.fn(() => 'blob:prompt-input-test'),
  })
  Object.defineProperty(URL, 'revokeObjectURL', {
    configurable: true,
    value: vi.fn(),
  })
})

afterEach(() => {
  Object.defineProperty(URL, 'createObjectURL', {
    configurable: true,
    value: originalCreateObjectURL,
  })
  Object.defineProperty(URL, 'revokeObjectURL', {
    configurable: true,
    value: originalRevokeObjectURL,
  })
})

describe('prompt input submission', () => {
  test('handles attachment conversion failure without submitting or clearing the retry state', async () => {
    const user = userEvent.setup()
    const onSubmit = vi.fn<PromptInputProps['onSubmit']>()
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockRejectedValue(new Error('blob conversion failed'))
    renderPromptInput(onSubmit)

    const attachment = new File(['image'], 'retry.png', {
      type: 'image/png',
    })
    await user.upload(screen.getByLabelText('Upload files'), attachment)
    expect(screen.getByText('retry.png')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Send' }))

    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith('blob:prompt-input-test')
    )
    expect(onSubmit).not.toHaveBeenCalled()
    expect(screen.getByText('retry.png')).toBeInTheDocument()
  })
})

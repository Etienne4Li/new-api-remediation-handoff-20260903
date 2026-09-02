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
import type { ComponentType, ReactNode } from 'react'
import { beforeEach, describe, expect, test, vi } from 'vitest'

import { AnnouncementsSection } from '../announcements-section'
import { FAQSection } from '../faq-section'
import { UptimeKumaSection } from '../uptime-kuma-section'

const { mutateAsyncMock } = vi.hoisted(() => ({
  mutateAsyncMock: vi.fn(),
}))

vi.mock('../../hooks/use-update-option', () => ({
  useUpdateOption: () => ({
    isPending: false,
    mutateAsync: mutateAsyncMock,
  }),
}))

vi.mock('@/components/data-table/static/static-row-actions', () => ({
  StaticRowActions: ({ onDelete }: { onDelete: () => void }) => (
    <button data-testid='remove-row' onClick={onDelete} type='button'>
      Remove row
    </button>
  ),
}))

type SlotProps = {
  children?: ReactNode
}

type DialogMockProps = SlotProps & {
  footer?: ReactNode
  open: boolean
}

vi.mock('@/components/dialog', () => ({
  Dialog: ({ children, footer, open }: DialogMockProps) =>
    open ? (
      <>
        {children}
        {footer}
      </>
    ) : null,
}))

type AlertDialogProps = SlotProps & {
  open: boolean
}

type AlertDialogActionProps = SlotProps & {
  onClick?: () => void
}

vi.mock('@/components/ui/alert-dialog', () => ({
  AlertDialog: ({ children, open }: AlertDialogProps) =>
    open ? <div>{children}</div> : null,
  AlertDialogAction: ({ children, onClick }: AlertDialogActionProps) => (
    <button onClick={onClick} type='button'>
      {children}
    </button>
  ),
  AlertDialogCancel: ({ children }: SlotProps) => (
    <button type='button'>{children}</button>
  ),
  AlertDialogContent: ({ children }: SlotProps) => <div>{children}</div>,
  AlertDialogDescription: ({ children }: SlotProps) => <p>{children}</p>,
  AlertDialogFooter: ({ children }: SlotProps) => <div>{children}</div>,
  AlertDialogHeader: ({ children }: SlotProps) => <div>{children}</div>,
  AlertDialogTitle: ({ children }: SlotProps) => <h2>{children}</h2>,
}))

type ContentSectionProps = {
  data: string
  enabled: boolean
}

type ContentSectionCase = {
  entry: Record<string, unknown>
  label: string
  name: string
  remoteEntry: Record<string, unknown>
  Section: ComponentType<ContentSectionProps>
}

const contentSections: ContentSectionCase[] = [
  {
    name: 'announcements',
    Section: AnnouncementsSection,
    label: 'Draft announcement',
    remoteEntry: {
      id: 2,
      content: 'Remote announcement',
      publishDate: '2026-01-02T00:00:00.000Z',
      type: 'default',
    },
    entry: {
      id: 1,
      content: 'Draft announcement',
      publishDate: '2026-01-01T00:00:00.000Z',
      type: 'default',
    },
  },
  {
    name: 'FAQ',
    Section: FAQSection,
    label: 'Draft question',
    remoteEntry: {
      id: 2,
      question: 'Remote question',
      answer: 'Remote answer',
    },
    entry: {
      id: 1,
      question: 'Draft question',
      answer: 'Draft answer',
    },
  },
  {
    name: 'Uptime Kuma groups',
    Section: UptimeKumaSection,
    label: 'Draft group',
    remoteEntry: {
      id: 2,
      categoryName: 'Remote group',
      url: 'https://status.example.org',
      slug: 'remote-group',
    },
    entry: {
      id: 1,
      categoryName: 'Draft group',
      url: 'https://status.example.com',
      slug: 'draft-group',
    },
  },
]

beforeEach(() => {
  mutateAsyncMock.mockReset()
  mutateAsyncMock.mockResolvedValue({ success: true, message: '' })
})

describe.each(contentSections)(
  '$name settings drafts',
  ({ entry, label, remoteEntry, Section }) => {
    test('keeps a local deletion when a stale settings refresh arrives', async () => {
      const initialData = JSON.stringify([entry])
      const { rerender } = render(<Section data={initialData} enabled />)

      expect(await screen.findByText(label)).toBeInTheDocument()

      fireEvent.click(screen.getByTestId('remove-row'))
      fireEvent.click(screen.getByRole('button', { name: 'Delete' }))

      await waitFor(() => {
        expect(screen.queryByText(label)).not.toBeInTheDocument()
      })
      expect(
        screen.getByRole('button', { name: 'Save Settings' })
      ).toBeEnabled()

      rerender(<Section data={JSON.stringify([entry, remoteEntry])} enabled />)

      expect(screen.queryByText(label)).not.toBeInTheDocument()
      expect(
        screen.getByRole('button', { name: 'Save Settings' })
      ).toBeEnabled()
    })

    test('retains a local draft when saving returns an unsuccessful response', async () => {
      mutateAsyncMock.mockResolvedValueOnce({
        success: false,
        message: 'failed',
      })
      const {
        Section: ContentSection,
        entry: contentEntry,
        label: contentLabel,
      } = { Section, entry, label }

      render(<ContentSection data={JSON.stringify([contentEntry])} enabled />)
      expect(await screen.findByText(contentLabel)).toBeInTheDocument()

      fireEvent.click(screen.getByTestId('remove-row'))
      fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
      fireEvent.click(screen.getByRole('button', { name: 'Save Settings' }))

      await waitFor(() => {
        expect(mutateAsyncMock).toHaveBeenCalledOnce()
      })

      expect(screen.queryByText(contentLabel)).not.toBeInTheDocument()
      expect(
        screen.getByRole('button', { name: 'Save Settings' })
      ).toBeEnabled()
    })
  }
)

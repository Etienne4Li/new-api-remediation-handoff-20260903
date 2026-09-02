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
import dayjs from 'dayjs'
import {
  ArrowLeft,
  CheckCircle2,
  Clock3,
  RotateCcw,
  Send,
  X,
} from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'

import {
  ticketCategoryLabel,
  ticketPriorityLabel,
  ticketPriorityVariant,
  ticketStatusLabel,
  ticketStatusVariant,
} from '../constants'
import type { SupportTicketDetail, TicketStatus } from '../types'

type TicketDetailProps = {
  detail: SupportTicketDetail
  currentUserId: number
  isAdmin: boolean
  isReplying: boolean
  isUpdatingStatus: boolean
  onBack: () => void
  onReply: (content: string) => Promise<void>
  onStatus: (status: TicketStatus) => Promise<void>
}

function formatTicketTime(timestamp: number): string {
  return dayjs.unix(timestamp).format('YYYY-MM-DD HH:mm')
}

export function TicketDetail(props: TicketDetailProps) {
  const { t } = useTranslation()
  const [reply, setReply] = useState('')
  const [closeConfirmOpen, setCloseConfirmOpen] = useState(false)
  const ticket = props.detail.ticket
  const isClosed = ticket.status === 'closed'

  useEffect(() => {
    void Promise.resolve().then(() => setReply(''))
  }, [ticket.id])

  const submitReply = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const content = reply.trim()
    if (!content || props.isReplying || isClosed) return
    try {
      await props.onReply(content)
      setReply('')
    } catch {
      // Keep the draft available for retry; the mutation owns the toast.
    }
  }

  return (
    <section className='bg-background flex min-h-0 flex-1 flex-col'>
      <header className='flex shrink-0 items-start gap-3 border-b px-3 py-3 sm:px-5 sm:py-4'>
        <Button
          className='md:hidden'
          variant='ghost'
          size='icon-sm'
          aria-label={t('Back to tickets')}
          onClick={props.onBack}
        >
          <ArrowLeft />
        </Button>
        <div className='min-w-0 flex-1'>
          <div className='flex flex-wrap items-center gap-2'>
            <h3 className='min-w-0 flex-1 truncate text-base font-semibold'>
              {ticket.title}
            </h3>
            <Badge variant={ticketStatusVariant(ticket.status)}>
              {ticketStatusLabel(t, ticket.status)}
            </Badge>
            <Badge variant={ticketPriorityVariant(ticket.priority)}>
              {ticketPriorityLabel(t, ticket.priority)}
            </Badge>
          </div>
          <p className='text-muted-foreground mt-1 flex flex-wrap gap-x-2 text-xs'>
            <span>#{ticket.id}</span>
            <span aria-hidden='true'>·</span>
            <span>{ticketCategoryLabel(t, ticket.category)}</span>
            {props.isAdmin && ticket.username ? (
              <>
                <span aria-hidden='true'>·</span>
                <span>{ticket.username}</span>
              </>
            ) : null}
            <span aria-hidden='true'>·</span>
            <span>{formatTicketTime(ticket.updated_time)}</span>
          </p>
        </div>
        <div className='flex shrink-0 items-center gap-2'>
          {props.isAdmin ? (
            <Select
              value={ticket.status}
              onValueChange={(value) => {
                const nextStatus = value as TicketStatus
                if (nextStatus === 'closed') {
                  setCloseConfirmOpen(true)
                  return
                }
                void props.onStatus(nextStatus)
              }}
              disabled={props.isUpdatingStatus}
            >
              <SelectTrigger
                size='sm'
                className='hidden w-32 sm:flex'
                aria-label={t('Ticket status')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {(['open', 'in_progress', 'resolved', 'closed'] as const).map(
                  (status) => (
                    <SelectItem key={status} value={status}>
                      {ticketStatusLabel(t, status)}
                    </SelectItem>
                  )
                )}
              </SelectContent>
            </Select>
          ) : null}
          {isClosed ? (
            <Button
              variant='outline'
              size='sm'
              disabled={props.isUpdatingStatus}
              onClick={() => void props.onStatus('open')}
            >
              <RotateCcw />
              <span className='hidden sm:inline'>{t('Reopen')}</span>
            </Button>
          ) : (
            <Button
              variant='ghost'
              size='icon-sm'
              aria-label={t('Close ticket')}
              disabled={props.isUpdatingStatus}
              onClick={() => setCloseConfirmOpen(true)}
            >
              <X />
            </Button>
          )}
        </div>
      </header>

      <ScrollArea className='min-h-0 flex-1'>
        <div className='mx-auto flex w-full max-w-3xl flex-col gap-4 px-3 py-5 sm:px-6'>
          {props.detail.messages.map((message) => {
            const isMine = message.author_id === props.currentUserId
            const isAdminMessage = message.author_role >= 10
            return (
              <div
                key={message.id}
                className={`flex ${isMine ? 'justify-end' : 'justify-start'}`}
              >
                <article
                  className={`max-w-[min(88%,42rem)] rounded-xl border px-3 py-2.5 text-sm shadow-xs ${
                    isMine
                      ? 'border-primary/20 bg-primary/8'
                      : 'bg-muted/45 border-border'
                  }`}
                >
                  <div className='mb-1 flex items-center gap-2 text-xs'>
                    <span className='font-medium'>
                      {isMine
                        ? t('You')
                        : message.author_name ||
                          (isAdminMessage ? t('Support team') : t('User'))}
                    </span>
                    {isAdminMessage ? (
                      <Badge variant='secondary'>{t('Staff')}</Badge>
                    ) : null}
                    <time
                      className='text-muted-foreground'
                      dateTime={new Date(
                        message.created_time * 1000
                      ).toISOString()}
                    >
                      {formatTicketTime(message.created_time)}
                    </time>
                  </div>
                  <p className='leading-6 break-words whitespace-pre-wrap'>
                    {message.content}
                  </p>
                </article>
              </div>
            )
          })}
        </div>
      </ScrollArea>

      <footer className='bg-background shrink-0 border-t px-3 py-3 sm:px-5'>
        {isClosed ? (
          <div className='text-muted-foreground flex items-center justify-center gap-2 py-2 text-sm'>
            <CheckCircle2 className='size-4' />
            {t(
              'This ticket is closed. Reopen it to continue the conversation.'
            )}
          </div>
        ) : (
          <form
            className='mx-auto flex max-w-3xl items-end gap-2'
            onSubmit={submitReply}
          >
            <Textarea
              value={reply}
              onChange={(event) => setReply(event.target.value)}
              className='max-h-36 min-h-10 resize-none'
              placeholder={t('Write a reply...')}
              aria-label={t('Reply')}
              disabled={props.isReplying}
              onKeyDown={(event) => {
                if (event.key === 'Enter' && (event.metaKey || event.ctrlKey)) {
                  event.preventDefault()
                  event.currentTarget.form?.requestSubmit()
                }
              }}
            />
            <Button
              type='submit'
              size='icon'
              aria-label={t('Send reply')}
              disabled={props.isReplying || reply.trim().length === 0}
            >
              {props.isReplying ? (
                <Clock3 className='animate-pulse' />
              ) : (
                <Send />
              )}
            </Button>
          </form>
        )}
      </footer>

      <ConfirmDialog
        open={closeConfirmOpen}
        onOpenChange={setCloseConfirmOpen}
        title={t('Close ticket')}
        desc={t(
          'Are you sure you want to close this ticket? You can reopen it later.'
        )}
        confirmText={t('Close ticket')}
        isLoading={props.isUpdatingStatus}
        handleConfirm={() => {
          void props.onStatus('closed').finally(() => {
            setCloseConfirmOpen(false)
          })
        }}
      />
    </section>
  )
}

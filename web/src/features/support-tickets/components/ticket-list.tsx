import dayjs from 'dayjs'
import { ChevronLeft, ChevronRight, MessageSquare, Search } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ScrollArea } from '@/components/ui/scroll-area'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'

import {
  ticketPriorityLabel,
  ticketPriorityVariant,
  ticketStatusLabel,
  ticketStatusVariant,
} from '../constants'
import type { SupportTicket, TicketStatus } from '../types'

type TicketListProps = {
  tickets: SupportTicket[]
  selectedTicketId: number | null
  keyword: string
  status: TicketStatus | 'all'
  page: number
  pageSize: number
  total: number
  isLoading: boolean
  onKeywordChange: (keyword: string) => void
  onStatusChange: (status: TicketStatus | 'all') => void
  onPageChange: (page: number) => void
  onSelect: (id: number) => void
}

function listTimestamp(timestamp: number): string {
  return dayjs.unix(timestamp).format('MM-DD HH:mm')
}

export function TicketList(props: TicketListProps) {
  const { t } = useTranslation()
  const totalPages = Math.max(1, Math.ceil(props.total / props.pageSize))
  const ticketResults = (() => {
    if (props.isLoading) {
      return (
        <div className='grid gap-px'>
          {Array.from({ length: 6 }, (_, index) => (
            <div key={index} className='space-y-2 border-b p-4'>
              <Skeleton className='h-4 w-4/5' />
              <Skeleton className='h-3 w-2/3' />
              <Skeleton className='h-5 w-1/2' />
            </div>
          ))}
        </div>
      )
    }

    if (props.tickets.length === 0) {
      return (
        <div className='text-muted-foreground flex min-h-52 flex-col items-center justify-center gap-2 px-6 text-center'>
          <MessageSquare className='size-8 opacity-60' />
          <p className='text-foreground text-sm font-medium'>
            {t('No tickets found')}
          </p>
          <p className='text-xs'>
            {t('Try a different filter or create a ticket.')}
          </p>
        </div>
      )
    }

    return (
      <ul aria-label={t('Support tickets')}>
        {props.tickets.map((ticket) => {
          const selected = ticket.id === props.selectedTicketId
          return (
            <li key={ticket.id} className='border-b'>
              <button
                type='button'
                aria-current={selected ? 'page' : undefined}
                className={`focus-visible:ring-ring/50 w-full px-3 py-3 text-left transition-colors outline-none focus-visible:ring-3 focus-visible:ring-inset ${
                  selected ? 'bg-accent' : 'hover:bg-muted/60'
                }`}
                onClick={() => props.onSelect(ticket.id)}
              >
                <div className='flex items-start gap-2'>
                  <div className='min-w-0 flex-1'>
                    <p className='truncate text-sm font-medium'>
                      {ticket.title}
                    </p>
                    <p className='text-muted-foreground mt-0.5 truncate text-xs'>
                      #{ticket.id}
                      {ticket.username ? ` · ${ticket.username}` : ''}
                    </p>
                  </div>
                  <time
                    className='text-muted-foreground shrink-0 text-[11px]'
                    dateTime={new Date(
                      ticket.updated_time * 1000
                    ).toISOString()}
                  >
                    {listTimestamp(ticket.updated_time)}
                  </time>
                </div>
                <div className='mt-2 flex items-center gap-1.5'>
                  <Badge variant={ticketStatusVariant(ticket.status)}>
                    {ticketStatusLabel(t, ticket.status)}
                  </Badge>
                  <Badge variant={ticketPriorityVariant(ticket.priority)}>
                    {ticketPriorityLabel(t, ticket.priority)}
                  </Badge>
                  <span className='text-muted-foreground ml-auto flex items-center gap-1 text-xs'>
                    <MessageSquare className='size-3' />
                    {ticket.message_count}
                  </span>
                </div>
              </button>
            </li>
          )
        })}
      </ul>
    )
  })()

  return (
    <aside className='bg-muted/15 flex min-h-0 w-full flex-col md:w-[min(38%,24rem)] md:min-w-72 md:border-r'>
      <div className='grid shrink-0 gap-2 border-b p-3'>
        <div className='relative'>
          <Search className='text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2' />
          <Input
            value={props.keyword}
            onChange={(event) => props.onKeywordChange(event.target.value)}
            className='bg-background pl-8'
            placeholder={t('Search tickets...')}
            aria-label={t('Search tickets')}
          />
        </div>
        <Select
          value={props.status}
          onValueChange={(value) =>
            props.onStatusChange(value as TicketStatus | 'all')
          }
        >
          <SelectTrigger className='bg-background w-full' size='sm'>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value='all'>{t('All statuses')}</SelectItem>
            {(['open', 'in_progress', 'resolved', 'closed'] as const).map(
              (status) => (
                <SelectItem key={status} value={status}>
                  {ticketStatusLabel(t, status)}
                </SelectItem>
              )
            )}
          </SelectContent>
        </Select>
      </div>

      <ScrollArea className='min-h-0 flex-1'>{ticketResults}</ScrollArea>

      <footer className='bg-background flex h-11 shrink-0 items-center justify-between border-t px-3'>
        <span className='text-muted-foreground text-xs'>
          {t('{{count}} tickets', { count: props.total })}
        </span>
        <div className='flex items-center gap-1'>
          <Button
            variant='ghost'
            size='icon-xs'
            aria-label={t('Previous page')}
            disabled={props.page <= 1}
            onClick={() => props.onPageChange(props.page - 1)}
          >
            <ChevronLeft />
          </Button>
          <span className='min-w-12 text-center text-xs tabular-nums'>
            {props.page}/{totalPages}
          </span>
          <Button
            variant='ghost'
            size='icon-xs'
            aria-label={t('Next page')}
            disabled={props.page >= totalPages}
            onClick={() => props.onPageChange(props.page + 1)}
          >
            <ChevronRight />
          </Button>
        </div>
      </footer>
    </aside>
  )
}

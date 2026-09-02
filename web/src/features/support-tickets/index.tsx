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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Inbox, Plus, RefreshCw } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useDebounce, useMediaQuery } from '@/hooks'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { getTicket, getTickets, replyToTicket, updateTicketStatus } from './api'
import { CreateTicketDialog } from './components/create-ticket-dialog'
import { TicketDetail } from './components/ticket-detail'
import { TicketList } from './components/ticket-list'
import { ticketErrorMessage } from './lib/ticket-error'
import type { SupportTicketDetail, TicketStatus } from './types'

const PAGE_SIZE = 20

export function SupportTickets() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const user = useAuthStore((state) => state.auth.user)
  const isMobile = useMediaQuery('(max-width: 767px)')
  const isAdmin = (user?.role ?? ROLE.GUEST) >= ROLE.ADMIN
  const scope = isAdmin ? 'admin' : 'self'

  const [createOpen, setCreateOpen] = useState(false)
  const [selectedTicketId, setSelectedTicketId] = useState<number | null>(null)
  const [keyword, setKeyword] = useState('')
  const [status, setStatus] = useState<TicketStatus | 'all'>('all')
  const [page, setPage] = useState(1)
  const debouncedKeyword = useDebounce(keyword, 350)

  const listKey = useMemo(
    () => [
      'support-tickets',
      scope,
      'list',
      page,
      PAGE_SIZE,
      debouncedKeyword,
      status,
    ],
    [debouncedKeyword, page, scope, status]
  )

  const ticketsQuery = useQuery({
    queryKey: listKey,
    queryFn: () =>
      getTickets({
        page,
        pageSize: PAGE_SIZE,
        keyword: debouncedKeyword,
        status: status === 'all' ? undefined : status,
      }),
    placeholderData: (previousData) => previousData,
  })

  const ticketItems = ticketsQuery.data?.items
  const tickets = useMemo(() => ticketItems ?? [], [ticketItems])
  const detailKey = [
    'support-tickets',
    scope,
    'detail',
    selectedTicketId,
  ] as const
  const detailQuery = useQuery({
    queryKey: detailKey,
    queryFn: () => getTicket(selectedTicketId as number),
    enabled: selectedTicketId !== null,
    refetchInterval: selectedTicketId === null ? false : 30_000,
  })

  useEffect(() => {
    if (ticketsQuery.error) {
      toast.error(
        ticketErrorMessage(ticketsQuery.error, t('Failed to load tickets'))
      )
    }
  }, [t, ticketsQuery.error])

  useEffect(() => {
    if (isMobile || selectedTicketId !== null || tickets.length === 0) return
    // eslint-disable-next-line react/set-state-in-effect -- Persist the first async desktop result across pagination and viewport changes.
    setSelectedTicketId(tickets[0].id)
  }, [isMobile, selectedTicketId, tickets])

  const replyMutation = useMutation({
    mutationFn: ({ id, content }: { id: number; content: string }) =>
      replyToTicket(id, content),
    onSuccess: (detail) => {
      queryClient.setQueryData(
        ['support-tickets', scope, 'detail', detail.ticket.id],
        detail
      )
      void queryClient.invalidateQueries({
        queryKey: ['support-tickets', scope, 'list'],
      })
    },
    onError: (error) => {
      toast.error(ticketErrorMessage(error, t('Failed to send reply')))
    },
  })

  const statusMutation = useMutation({
    mutationFn: ({
      id,
      nextStatus,
    }: {
      id: number
      nextStatus: TicketStatus
    }) => updateTicketStatus(id, nextStatus),
    onSuccess: (detail) => {
      queryClient.setQueryData(
        ['support-tickets', scope, 'detail', detail.ticket.id],
        detail
      )
      void queryClient.invalidateQueries({
        queryKey: ['support-tickets', scope, 'list'],
      })
      toast.success(t('Ticket status updated'))
    },
    onError: (error) => {
      toast.error(ticketErrorMessage(error, t('Failed to update ticket')))
    },
  })

  const handleCreated = (detail: SupportTicketDetail) => {
    queryClient.setQueryData(
      ['support-tickets', scope, 'detail', detail.ticket.id],
      detail
    )
    void queryClient.invalidateQueries({
      queryKey: ['support-tickets', scope, 'list'],
    })
    setSelectedTicketId(detail.ticket.id)
    setPage(1)
    toast.success(t('Ticket created'))
  }

  const detailContent = (() => {
    if (selectedTicketId === null) {
      return (
        <div className='text-muted-foreground flex min-h-0 flex-1 flex-col items-center justify-center gap-2 p-8 text-center'>
          <Inbox className='size-9 opacity-60' />
          <p className='text-foreground text-sm font-medium'>
            {t('Select a ticket')}
          </p>
        </div>
      )
    }
    if (detailQuery.isLoading) {
      return (
        <div className='flex min-h-0 flex-1 flex-col'>
          <div className='space-y-2 border-b p-5'>
            <Skeleton className='h-5 w-2/3' />
            <Skeleton className='h-3 w-1/3' />
          </div>
          <div className='flex-1 space-y-5 p-6'>
            <Skeleton className='h-24 w-4/5' />
            <Skeleton className='ml-auto h-20 w-3/5' />
          </div>
        </div>
      )
    }
    if (!detailQuery.data) {
      return (
        <div className='text-muted-foreground flex min-h-0 flex-1 flex-col items-center justify-center gap-3 p-8 text-center'>
          <p className='text-foreground text-sm font-medium'>
            {t('Unable to load this ticket')}
          </p>
          <Button variant='outline' onClick={() => void detailQuery.refetch()}>
            <RefreshCw />
            {t('Retry')}
          </Button>
        </div>
      )
    }
    return (
      <TicketDetail
        detail={detailQuery.data}
        currentUserId={user?.id ?? 0}
        isAdmin={isAdmin}
        isReplying={replyMutation.isPending}
        isUpdatingStatus={statusMutation.isPending}
        onBack={() => setSelectedTicketId(null)}
        onReply={async (content) => {
          await replyMutation.mutateAsync({ id: selectedTicketId, content })
        }}
        onStatus={async (nextStatus) => {
          try {
            await statusMutation.mutateAsync({
              id: selectedTicketId,
              nextStatus,
            })
          } catch {
            // The mutation owns the error toast.
          }
        }}
      />
    )
  })()

  return (
    <>
      <SectionPageLayout fixedContent variant='editorial' density='compact'>
        <SectionPageLayout.Title>
          {isAdmin ? t('Ticket management') : t('Support tickets')}
        </SectionPageLayout.Title>
        <SectionPageLayout.Actions>
          <Button onClick={() => setCreateOpen(true)}>
            <Plus />
            {t('New ticket')}
          </Button>
        </SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          <div className='bg-background flex h-full min-h-[28rem] overflow-hidden rounded-lg border'>
            {!isMobile || selectedTicketId === null ? (
              <TicketList
                tickets={tickets}
                selectedTicketId={selectedTicketId}
                keyword={keyword}
                status={status}
                page={page}
                pageSize={PAGE_SIZE}
                total={ticketsQuery.data?.total ?? 0}
                isLoading={ticketsQuery.isLoading}
                onKeywordChange={(value) => {
                  setKeyword(value)
                  setPage(1)
                }}
                onStatusChange={(value) => {
                  setStatus(value)
                  setPage(1)
                }}
                onPageChange={setPage}
                onSelect={setSelectedTicketId}
              />
            ) : null}
            {!isMobile || selectedTicketId !== null ? detailContent : null}
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>

      <CreateTicketDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        onCreated={handleCreated}
      />
    </>
  )
}

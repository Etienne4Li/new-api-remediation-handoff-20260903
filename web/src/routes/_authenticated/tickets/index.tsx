import { createFileRoute } from '@tanstack/react-router'

import { SupportTickets } from '@/features/support-tickets'

export const Route = createFileRoute('/_authenticated/tickets/')({
  component: SupportTickets,
})

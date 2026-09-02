import type { TFunction } from 'i18next'

import type { TicketCategory, TicketPriority, TicketStatus } from './types'

export function ticketCategoryLabel(
  t: TFunction,
  category: TicketCategory
): string {
  const labels: Record<TicketCategory, string> = {
    account: t('Account'),
    billing: t('Billing'),
    api: t('API usage'),
    other: t('Other'),
  }
  return labels[category]
}

export function ticketPriorityLabel(
  t: TFunction,
  priority: TicketPriority
): string {
  const labels: Record<TicketPriority, string> = {
    low: t('Low'),
    normal: t('Normal'),
    high: t('High'),
    urgent: t('Urgent'),
  }
  return labels[priority]
}

export function ticketStatusLabel(t: TFunction, status: TicketStatus): string {
  const labels: Record<TicketStatus, string> = {
    open: t('Open'),
    in_progress: t('In progress'),
    resolved: t('Resolved'),
    closed: t('Closed'),
  }
  return labels[status]
}

export function ticketStatusVariant(
  status: TicketStatus
): 'default' | 'secondary' | 'warning' | 'destructive' | 'outline' {
  switch (status) {
    case 'open':
      return 'default'
    case 'in_progress':
      return 'warning'
    case 'resolved':
      return 'secondary'
    case 'closed':
      return 'outline'
  }
}

export function ticketPriorityVariant(
  priority: TicketPriority
): 'default' | 'secondary' | 'warning' | 'destructive' | 'outline' {
  switch (priority) {
    case 'urgent':
      return 'destructive'
    case 'high':
      return 'warning'
    case 'normal':
      return 'secondary'
    case 'low':
      return 'outline'
  }
}

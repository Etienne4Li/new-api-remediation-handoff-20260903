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
const TICKET_CATEGORIES = ['account', 'billing', 'api', 'other'] as const
export type TicketCategory = (typeof TICKET_CATEGORIES)[number]

const TICKET_PRIORITIES = ['low', 'normal', 'high', 'urgent'] as const
export type TicketPriority = (typeof TICKET_PRIORITIES)[number]

const TICKET_STATUSES = ['open', 'in_progress', 'resolved', 'closed'] as const
export type TicketStatus = (typeof TICKET_STATUSES)[number]

export type SupportTicket = {
  id: number
  user_id: number
  username: string
  title: string
  category: TicketCategory
  priority: TicketPriority
  status: TicketStatus
  created_time: number
  updated_time: number
  last_reply_time: number
  last_reply_by: 'user' | 'admin'
  message_count: number
}

type SupportTicketMessage = {
  id: number
  ticket_id: number
  author_id: number
  author_role: number
  author_name: string
  content: string
  created_time: number
}

export type SupportTicketDetail = {
  ticket: SupportTicket
  messages: SupportTicketMessage[]
}

export type TicketPage = {
  items: SupportTicket[]
  total: number
  page: number
  page_size: number
}

export type TicketListParams = {
  page: number
  pageSize: number
  keyword?: string
  status?: TicketStatus
  priority?: TicketPriority
  category?: TicketCategory
}

export type CreateTicketInput = {
  title: string
  category: TicketCategory
  priority: TicketPriority
  content: string
}

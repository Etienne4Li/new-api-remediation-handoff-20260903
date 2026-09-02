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
import { api } from '@/lib/api'

import type {
  CreateTicketInput,
  SupportTicketDetail,
  TicketListParams,
  TicketPage,
  TicketStatus,
} from './types'

type ApiEnvelope<T> = {
  success: boolean
  message?: string
  data?: T
}

const ticketRequestConfig = {
  skipBusinessError: true,
  skipErrorHandler: true,
} as const

function unwrap<T>(response: ApiEnvelope<T>): T {
  if (!response.success || response.data === undefined) {
    throw new Error(response.message || 'Unable to complete the request')
  }
  return response.data
}

export async function getTickets(
  params: TicketListParams
): Promise<TicketPage> {
  const query = new URLSearchParams({
    p: String(params.page),
    page_size: String(params.pageSize),
  })
  if (params.keyword?.trim()) query.set('keyword', params.keyword.trim())
  if (params.status) query.set('status', params.status)
  if (params.priority) query.set('priority', params.priority)
  if (params.category) query.set('category', params.category)

  const response = await api.get<ApiEnvelope<TicketPage>>(
    `/api/ticket/?${query.toString()}`,
    ticketRequestConfig
  )
  return unwrap(response.data)
}

export async function getTicket(id: number): Promise<SupportTicketDetail> {
  const response = await api.get<ApiEnvelope<SupportTicketDetail>>(
    `/api/ticket/${id}`,
    ticketRequestConfig
  )
  return unwrap(response.data)
}

export async function createTicket(
  input: CreateTicketInput
): Promise<SupportTicketDetail> {
  const response = await api.post<ApiEnvelope<SupportTicketDetail>>(
    '/api/ticket/',
    input,
    ticketRequestConfig
  )
  return unwrap(response.data)
}

export async function replyToTicket(
  id: number,
  content: string
): Promise<SupportTicketDetail> {
  const response = await api.post<ApiEnvelope<SupportTicketDetail>>(
    `/api/ticket/${id}/messages`,
    { content },
    ticketRequestConfig
  )
  return unwrap(response.data)
}

export async function updateTicketStatus(
  id: number,
  status: TicketStatus
): Promise<SupportTicketDetail> {
  const response = await api.patch<ApiEnvelope<SupportTicketDetail>>(
    `/api/ticket/${id}/status`,
    { status },
    ticketRequestConfig
  )
  return unwrap(response.data)
}

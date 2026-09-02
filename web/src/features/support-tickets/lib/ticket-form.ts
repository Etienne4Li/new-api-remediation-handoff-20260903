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
import { z } from 'zod'

function unicodeLength(value: string): number {
  return [...value].length
}

export const ticketFormSchema = z.object({
  title: z
    .string()
    .trim()
    .refine((value) => unicodeLength(value) >= 5, {
      message: 'Ticket title must be at least 5 characters',
    })
    .refine((value) => unicodeLength(value) <= 120, {
      message: 'Ticket title must be at most 120 characters',
    }),
  category: z.enum(['account', 'billing', 'api', 'other']),
  priority: z.enum(['low', 'normal', 'high', 'urgent']),
  content: z
    .string()
    .trim()
    .refine((value) => unicodeLength(value) >= 1, {
      message: 'Ticket message cannot be empty',
    })
    .refine((value) => unicodeLength(value) <= 5000, {
      message: 'Ticket message must be at most 5000 characters',
    }),
})

export type TicketFormValues = z.infer<typeof ticketFormSchema>

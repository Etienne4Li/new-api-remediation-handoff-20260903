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

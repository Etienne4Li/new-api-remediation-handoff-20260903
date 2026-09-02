import { zodResolver } from '@hookform/resolvers/zod'
import { Plus } from 'lucide-react'
import { useEffect } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'

import { createTicket } from '../api'
import { ticketCategoryLabel, ticketPriorityLabel } from '../constants'
import { ticketErrorMessage } from '../lib/ticket-error'
import { ticketFormSchema, type TicketFormValues } from '../lib/ticket-form'
import type { SupportTicketDetail } from '../types'

type CreateTicketDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: (detail: SupportTicketDetail) => void
}

export function CreateTicketDialog(props: CreateTicketDialogProps) {
  const { t } = useTranslation()
  const form = useForm<TicketFormValues>({
    resolver: zodResolver(ticketFormSchema),
    defaultValues: {
      title: '',
      category: 'api',
      priority: 'normal',
      content: '',
    },
  })

  useEffect(() => {
    if (props.open) {
      form.reset()
    }
  }, [form, props.open])

  const onSubmit = async (values: TicketFormValues) => {
    try {
      const detail = await createTicket(values)
      props.onCreated(detail)
      props.onOpenChange(false)
    } catch (error) {
      toast.error(ticketErrorMessage(error, t('Failed to create ticket')))
    }
  }

  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      <DialogContent className='max-h-[min(720px,calc(100vh-2rem))] max-w-xl overflow-y-auto'>
        <DialogHeader>
          <DialogTitle className='flex items-center gap-2'>
            <Plus className='size-4' />
            {t('New support ticket')}
          </DialogTitle>
          <DialogDescription>
            {t('Describe the issue and our team will follow up here.')}
          </DialogDescription>
        </DialogHeader>

        <Form {...form}>
          <form className='grid gap-4' onSubmit={form.handleSubmit(onSubmit)}>
            <FormField
              control={form.control}
              name='title'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Subject')}</FormLabel>
                  <FormControl>
                    <Input
                      {...field}
                      autoComplete='off'
                      placeholder={t('What do you need help with?')}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />

            <div className='grid gap-4 sm:grid-cols-2'>
              <FormField
                control={form.control}
                name='category'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Category')}</FormLabel>
                    <Select value={field.value} onValueChange={field.onChange}>
                      <FormControl>
                        <SelectTrigger className='w-full'>
                          <SelectValue />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent>
                        {(['account', 'billing', 'api', 'other'] as const).map(
                          (category) => (
                            <SelectItem key={category} value={category}>
                              {ticketCategoryLabel(t, category)}
                            </SelectItem>
                          )
                        )}
                      </SelectContent>
                    </Select>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='priority'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Priority')}</FormLabel>
                    <Select value={field.value} onValueChange={field.onChange}>
                      <FormControl>
                        <SelectTrigger className='w-full'>
                          <SelectValue />
                        </SelectTrigger>
                      </FormControl>
                      <SelectContent>
                        {(['low', 'normal', 'high', 'urgent'] as const).map(
                          (priority) => (
                            <SelectItem key={priority} value={priority}>
                              {ticketPriorityLabel(t, priority)}
                            </SelectItem>
                          )
                        )}
                      </SelectContent>
                    </Select>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>

            <FormField
              control={form.control}
              name='content'
              render={({ field }) => (
                <FormItem>
                  <FormLabel>{t('Description')}</FormLabel>
                  <FormControl>
                    <Textarea
                      {...field}
                      className='min-h-36 resize-y'
                      placeholder={t(
                        'Include the request, error message, and relevant details.'
                      )}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />

            <DialogFooter>
              <Button
                type='button'
                variant='outline'
                onClick={() => props.onOpenChange(false)}
              >
                {t('Cancel')}
              </Button>
              <Button type='submit' disabled={form.formState.isSubmitting}>
                <Plus />
                {form.formState.isSubmitting
                  ? t('Creating...')
                  : t('Create ticket')}
              </Button>
            </DialogFooter>
          </form>
        </Form>
      </DialogContent>
    </Dialog>
  )
}

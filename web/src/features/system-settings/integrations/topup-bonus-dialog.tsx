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
import { zodResolver } from '@hookform/resolvers/zod'
import { useEffect, useMemo } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'

// The ratio is capped at 1 for the same reason the backend drops anything
// larger: it is a fraction, and reading a fat-fingered "2" as 200% would give
// away two extra top-ups.
const createTopupBonusDialogSchema = (t: (key: string) => string) =>
  z.object({
    amount: z
      .number()
      .positive(t('Amount must be greater than 0'))
      .int(t('Amount must be a whole number')),
    bonusRatio: z
      .number()
      .positive(t('Bonus ratio must be greater than 0'))
      .max(1, t('Bonus ratio must be ≤ 1')),
  })

type TopupBonusDialogFormValues = z.infer<
  ReturnType<typeof createTopupBonusDialogSchema>
>

const TOPUP_BONUS_FORM_ID = 'topup-bonus-form'

export type TopupBonusData = {
  amount: number
  bonusRatio: number
}

type TopupBonusDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSave: (data: TopupBonusData) => void
  editData?: TopupBonusData | null
}

export function TopupBonusDialog({
  open,
  onOpenChange,
  onSave,
  editData,
}: TopupBonusDialogProps) {
  const { t } = useTranslation()
  const isEditMode = !!editData
  const topupBonusDialogSchema = createTopupBonusDialogSchema(t)

  const form = useForm<TopupBonusDialogFormValues>({
    resolver: zodResolver(topupBonusDialogSchema),
    defaultValues: {
      amount: 0,
      bonusRatio: 0.01,
    },
  })

  const bonusRatio = form.watch('bonusRatio')
  const amount = form.watch('amount')

  const bonusPercentage = useMemo(() => {
    if (!bonusRatio || bonusRatio <= 0) return 0
    return Number.parseFloat((bonusRatio * 100).toFixed(2))
  }, [bonusRatio])

  const grantedAmount = useMemo(() => {
    if (!amount || amount <= 0 || !bonusRatio || bonusRatio <= 0) return 0
    return Number.parseFloat((amount * bonusRatio).toFixed(2))
  }, [amount, bonusRatio])

  useEffect(() => {
    if (editData) {
      form.reset(editData)
    } else {
      form.reset({
        amount: 0,
        bonusRatio: 0.01,
      })
    }
  }, [editData, form, open])

  const handleSubmit = (values: TopupBonusDialogFormValues) => {
    onSave({
      amount: values.amount,
      bonusRatio: values.bonusRatio,
    })
    form.reset()
    onOpenChange(false)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={isEditMode ? t('Edit bonus tier') : t('Add bonus tier')}
      description={t(
        'Set a bonus ratio for a specific recharge amount threshold.'
      )}
      contentClassName='sm:max-w-[500px]'
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => onOpenChange(false)}
          >
            {t('Cancel')}
          </Button>
          <Button type='submit' form={TOPUP_BONUS_FORM_ID}>
            {isEditMode ? t('Update') : t('Add')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        <form
          id={TOPUP_BONUS_FORM_ID}
          onSubmit={form.handleSubmit(handleSubmit)}
          className='space-y-4'
        >
          <FormField
            control={form.control}
            name='amount'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Recharge Amount')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    step='1'
                    min='1'
                    placeholder={t('e.g., 100')}
                    {...field}
                    onChange={(e) =>
                      field.onChange(Number.parseInt(e.target.value) || 0)
                    }
                    disabled={isEditMode}
                  />
                </FormControl>
                <FormDescription>
                  {isEditMode
                    ? t('Amount cannot be changed when editing.')
                    : t('Minimum recharge amount to qualify for this bonus.')}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='bonusRatio'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Bonus Ratio')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    step='0.005'
                    min='0.001'
                    max='1'
                    placeholder={t('e.g., 0.02')}
                    {...field}
                    onChange={(e) =>
                      field.onChange(Number.parseFloat(e.target.value) || 0)
                    }
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'Share of the recharge granted as extra balance (0.02 = 2% extra)'
                  )}
                  {bonusPercentage > 0 && (
                    <span className='ml-1 font-medium text-green-600 dark:text-green-400'>
                      = +{bonusPercentage}%
                      {grantedAmount > 0 && ` (${amount} → +${grantedAmount})`}
                    </span>
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
        </form>
      </Form>
    </Dialog>
  )
}

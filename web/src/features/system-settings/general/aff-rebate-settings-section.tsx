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
import { useForm, type Resolver } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { z } from 'zod'

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
import { Switch } from '@/components/ui/switch'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'

// The percentage is capped at 100 so a misconfiguration can never rebate more
// than the invitee actually paid.
const schema = z.object({
  AffRebateEnabled: z.boolean(),
  AffRebatePercent: z.coerce.number().int().min(0).max(100),
  AffRebateMaxTimes: z.coerce.number().int().min(0),
})

type Values = z.infer<typeof schema>

export function AffRebateSettingsSection({
  defaultValues,
}: {
  defaultValues: Values
}) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()

  const form = useForm<Values>({
    resolver: zodResolver(schema) as unknown as Resolver<Values>,
    defaultValues: {
      AffRebateEnabled: defaultValues.AffRebateEnabled,
      AffRebatePercent: defaultValues.AffRebatePercent,
      AffRebateMaxTimes: defaultValues.AffRebateMaxTimes,
    },
  })

  const { isDirty, isSubmitting } = form.formState
  const enabled = form.watch('AffRebateEnabled')

  async function onSubmit(values: Values) {
    const updates: Array<{ key: string; value: string }> = []

    if (values.AffRebateEnabled !== defaultValues.AffRebateEnabled) {
      updates.push({
        key: 'AffRebateEnabled',
        value: String(values.AffRebateEnabled),
      })
    }
    if (values.AffRebatePercent !== defaultValues.AffRebatePercent) {
      updates.push({
        key: 'AffRebatePercent',
        value: String(values.AffRebatePercent),
      })
    }
    if (values.AffRebateMaxTimes !== defaultValues.AffRebateMaxTimes) {
      updates.push({
        key: 'AffRebateMaxTimes',
        value: String(values.AffRebateMaxTimes),
      })
    }

    if (updates.length === 0) {
      toast.info(t('No changes to save'))
      return
    }

    for (const update of updates) {
      await updateOption.mutateAsync(update)
    }

    form.reset(values)
  }

  return (
    <SettingsSection title={t('Referral Rebate Settings')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)} autoComplete='off'>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending || isSubmitting}
            isSaveDisabled={!isDirty}
            saveLabel='Save referral rebate settings'
          />
          <FormField
            control={form.control}
            name='AffRebateEnabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Enable referral top-up rebate')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Credit the inviter a share of every rebate-eligible top-up made by the users they invited. Rebates land as invitation quota that can only be transferred to balance, never withdrawn.'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                    disabled={updateOption.isPending || isSubmitting}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />

          {enabled && (
            <div className='grid gap-6 sm:grid-cols-2'>
              <FormField
                control={form.control}
                name='AffRebatePercent'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Rebate percentage')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        min={0}
                        max={100}
                        placeholder='5'
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Percentage of the amount actually paid that the inviter receives'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='AffRebateMaxTimes'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Rebated top-ups per invitee')}</FormLabel>
                    <FormControl>
                      <Input type='number' min={0} placeholder='3' {...field} />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'How many successful top-ups per invited user earn a rebate'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />
            </div>
          )}
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}

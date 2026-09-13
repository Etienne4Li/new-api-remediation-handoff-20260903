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
import { Switch } from '@/components/ui/switch'

import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'
import { TopupBonusVisualEditor } from '../integrations/topup-bonus-visual-editor'
import {
  formatJsonForEditor,
  normalizeJsonForComparison,
} from '../integrations/utils'

const schema = z.object({
  TopupBonusEnabled: z.boolean(),
  TopupBonus: z.string().superRefine((value, ctx) => {
    const trimmed = value.trim()
    if (trimmed === '') {
      return
    }
    let parsed: unknown
    try {
      parsed = JSON.parse(trimmed)
    } catch {
      ctx.addIssue({ code: 'custom', message: 'Invalid JSON format' })
      return
    }
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      ctx.addIssue({ code: 'custom', message: 'Invalid JSON format' })
    }
  }),
})

type Values = z.infer<typeof schema>

export function TopupBonusSettingsSection({
  defaultValues,
}: {
  defaultValues: Values
}) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()

  const initial: Values = {
    TopupBonusEnabled: defaultValues.TopupBonusEnabled,
    TopupBonus: formatJsonForEditor(defaultValues.TopupBonus),
  }

  const form = useForm<Values>({
    resolver: zodResolver(schema) as unknown as Resolver<Values>,
    defaultValues: initial,
  })

  const { isDirty, isSubmitting } = form.formState
  const enabled = form.watch('TopupBonusEnabled')

  async function onSubmit(values: Values) {
    const updates: Array<{ key: string; value: string }> = []

    if (values.TopupBonusEnabled !== initial.TopupBonusEnabled) {
      updates.push({
        key: 'payment_setting.topup_bonus_enabled',
        value: String(values.TopupBonusEnabled),
      })
    }
    // Compare parsed rather than raw so re-indentation alone is not a change.
    if (
      normalizeJsonForComparison(values.TopupBonus.trim()) !==
      normalizeJsonForComparison(initial.TopupBonus.trim())
    ) {
      updates.push({
        key: 'payment_setting.topup_bonus',
        value:
          values.TopupBonus.trim() === '' ? '{}' : values.TopupBonus.trim(),
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
    <SettingsSection title={t('Top-up Bonus Settings')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)} autoComplete='off'>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending || isSubmitting}
            isSaveDisabled={!isDirty}
            saveLabel='Save top-up bonus settings'
          />
          <FormField
            control={form.control}
            name='TopupBonusEnabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Enable top-up bonus')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Credit extra balance on top of a qualifying top-up. The user still pays the full amount; the bonus is added to what they receive. This is independent of the amount discount, which lowers the price instead.'
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
            <FormField
              control={form.control}
              name='TopupBonus'
              render={({ field }) => (
                <FormItem>
                  <FormControl>
                    <TopupBonusVisualEditor
                      value={field.value}
                      onChange={field.onChange}
                    />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )}
            />
          )}
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}

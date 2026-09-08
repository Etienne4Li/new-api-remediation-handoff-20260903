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

import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Textarea } from '@/components/ui/textarea'
import { MAX_HEADER_NAV_CUSTOM_LINKS } from '@/hooks/use-top-nav-links'

import { SettingsForm } from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'
import {
  linesToLinks,
  linksToLines,
  serializeHeaderNavCustomLinks,
} from './header-custom-links'

const schema = z.object({
  lines: z.string().optional(),
})

type FormValues = z.infer<typeof schema>

type HeaderCustomLinksSectionProps = {
  /** Raw `HeaderNavCustomLinks` option value (stringified JSON array). */
  defaultValue: string
}

export function HeaderCustomLinksSection({
  defaultValue,
}: HeaderCustomLinksSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()
  const initialLines = useMemo(() => linksToLines(defaultValue), [defaultValue])

  const form = useForm<FormValues>({
    resolver: zodResolver(schema),
    defaultValues: { lines: initialLines },
  })

  useEffect(() => {
    form.reset({ lines: initialLines })
  }, [initialLines, form])

  const currentLines = form.watch('lines') ?? ''
  const preview = useMemo(() => linesToLinks(currentLines), [currentLines])

  const onSubmit = async (values: FormValues) => {
    const { links, invalid } = linesToLinks(values.lines ?? '')
    if (invalid.length > 0) {
      form.setError('lines', {
        message: t('Some lines are invalid: {{lines}}', {
          lines: invalid.join(' ; '),
        }),
      })
      return
    }
    const serialized = serializeHeaderNavCustomLinks(links)
    if (serialized === (defaultValue ?? '')) {
      return
    }
    await updateOption.mutateAsync({
      key: 'HeaderNavCustomLinks',
      value: serialized,
    })
  }

  return (
    <SettingsSection title={t('Header custom links')}>
      <Form {...form}>
        <SettingsForm onSubmit={form.handleSubmit(onSubmit)}>
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
            saveLabel='Save custom links'
          />
          <FormField
            control={form.control}
            name='lines'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Extra links shown after the built-in modules')}</FormLabel>
                <FormControl>
                  <Textarea
                    rows={5}
                    placeholder={'生图 | https://im.lietio.com\n服务状态 | /status | auth'}
                    {...field}
                  />
                </FormControl>
                <FormDescription>
                  {t(
                    'One link per line: Title | URL. Add "| auth" to require sign-in. Absolute URLs open in a new tab; up to {{max}} links.',
                    { max: MAX_HEADER_NAV_CUSTOM_LINKS }
                  )}
                  {preview.links.length > 0 && (
                    <span className='mt-1 block'>
                      {t('Preview')}:{' '}
                      {preview.links.map((link) => link.title).join(' · ')}
                    </span>
                  )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}

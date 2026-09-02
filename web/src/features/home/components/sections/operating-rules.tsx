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
import { CircleGauge, ReceiptText, Scale } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { AnimateInView } from '@/components/animate-in-view'

export function OperatingRules() {
  const { t } = useTranslation()
  const rules = [
    {
      icon: CircleGauge,
      title: t('Capacity protection'),
      lead: t('Steady access matters more than an inflated promise.'),
      items: [
        t(
          'Routing follows the capacity and health signals available to the current configuration.'
        ),
        t('Concurrency and response quality may change under heavy load.'),
        t('When no upstream is configured, the interface says so directly.'),
      ],
    },
    {
      icon: Scale,
      title: t('Fair use'),
      lead: t('A dependable passage for work, not an unlimited pipe.'),
      items: [
        t('Access remains subject to account quotas and administrator policy.'),
        t('Rate limits may apply when traffic threatens shared capacity.'),
        t('Every limit shown in the console comes from the live system state.'),
      ],
    },
    {
      icon: ReceiptText,
      title: t('Incidents and corrections'),
      lead: t('A correction begins with a request that can be traced.'),
      items: [
        t('Request logs preserve the identifiers needed for investigation.'),
        t('Pricing and usage records remain the billing source of truth.'),
        t('Unconfigured policies are presented as unconfigured, not assumed.'),
      ],
    },
  ]

  return (
    <section className='border-border/40 bg-muted/30 relative z-10 border-t px-4 py-16 sm:px-6 sm:py-20 md:py-32 lg:px-8'>
      <div className='mx-auto max-w-[80rem]'>
        <AnimateInView className='mb-12 max-w-xl sm:mb-16'>
          <h2 className='text-3xl leading-tight font-bold md:text-4xl'>
            {t('Clear boundaries, current rules')}
          </h2>
          <p className='text-muted-foreground mt-3 text-sm leading-relaxed'>
            {t(
              'These operational rules summarize current practice; the User Agreement controls if terms differ.'
            )}
          </p>
        </AnimateInView>

        <div className='grid gap-4 sm:gap-6 md:grid-cols-3 md:gap-8'>
          {rules.map((rule, index) => (
            <AnimateInView
              key={rule.title}
              delay={index * 120}
              animation='fade-in'
              className='border-border/40 bg-background flex min-h-[21rem] flex-col rounded-xl border p-5 sm:p-6 md:min-h-[28rem] md:p-8'
            >
              <div className='border-border/50 bg-muted/30 text-muted-foreground mb-5 flex size-11 items-center justify-center rounded-lg border'>
                <rule.icon
                  aria-hidden='true'
                  className='size-5'
                  strokeWidth={1.5}
                />
              </div>
              <h3 className='text-base font-semibold'>{rule.title}</h3>
              <p className='text-muted-foreground mt-1.5 text-xs leading-relaxed'>
                {rule.lead}
              </p>
              <ul className='mt-5 space-y-3'>
                {rule.items.map((item) => (
                  <li
                    key={item}
                    className='text-foreground/80 flex items-start gap-2.5 text-sm leading-relaxed'
                  >
                    <span
                      aria-hidden='true'
                      className='bg-muted-foreground/30 mt-1.5 size-1.5 shrink-0 rounded-full'
                    />
                    <span>{item}</span>
                  </li>
                ))}
              </ul>
            </AnimateInView>
          ))}
        </div>
      </div>
    </section>
  )
}

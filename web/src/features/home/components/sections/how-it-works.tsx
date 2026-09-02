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
import { BarChart3, Settings, Zap } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { AnimateInView } from '@/components/animate-in-view'

export function HowItWorks() {
  const { t } = useTranslation()

  const steps = [
    {
      num: '1',
      title: t('Configure'),
      desc: t(
        'Add your API keys, set up channels and configure access permissions'
      ),
      icon: (
        <Settings aria-hidden='true' className='size-4' strokeWidth={1.7} />
      ),
      detail: t('Configure upstream providers and routing.'),
    },
    {
      num: '2',
      title: t('Connect'),
      desc: t(
        'Connect through OpenAI, Claude, Gemini, and other compatible API routes'
      ),
      icon: <Zap aria-hidden='true' className='size-4' strokeWidth={1.7} />,
      detail: t('Verify routing with Playground or your client'),
    },
    {
      num: '3',
      title: t('Monitor'),
      desc: t('Track usage, costs and performance with real-time analytics'),
      icon: (
        <BarChart3 aria-hidden='true' className='size-4' strokeWidth={1.7} />
      ),
      detail: t('Detailed request logs for investigations.'),
    },
  ]

  return (
    <section className='border-border/60 relative z-10 border-t px-4 py-16 sm:px-6 md:py-20'>
      <div className='mx-auto max-w-6xl'>
        <AnimateInView
          className='border-border/60 mb-8 grid gap-3 border-b pb-7 md:grid-cols-[minmax(0,0.7fr)_minmax(0,1.3fr)] md:items-end md:gap-10'
          animation='fade-in'
        >
          <p className='text-muted-foreground font-mono text-[10px] font-semibold tracking-[0.14em] uppercase'>
            {t('A simple rhythm')}
          </p>
          <h2 className='max-w-2xl text-2xl leading-tight font-semibold md:text-3xl'>
            {t('Three small steps, then onward')}
          </h2>
        </AnimateInView>

        <div className='border-border/60 grid border-y md:grid-cols-[minmax(0,0.7fr)_minmax(0,1.3fr)]'>
          {steps.map((step, index) => (
            <AnimateInView
              key={step.num}
              delay={index * 90}
              animation='fade-up'
              className={`grid min-w-0 gap-4 p-5 sm:p-6 md:grid-cols-[minmax(7rem,0.45fr)_minmax(0,1fr)] md:gap-8 ${index < steps.length - 1 ? 'border-border/60 border-b' : ''}`}
            >
              <div className='flex items-start gap-3'>
                <span className='text-primary font-mono text-xs tabular-nums'>
                  0{step.num}
                </span>
                <div className='text-primary border-border/70 flex size-7 shrink-0 items-center justify-center border'>
                  {step.icon}
                </div>
              </div>
              <div className='min-w-0'>
                <h3 className='text-sm font-semibold'>{step.title}</h3>
                <p className='text-muted-foreground mt-1 text-sm leading-relaxed'>
                  {step.desc}
                </p>
                <p className='text-muted-foreground border-border/40 mt-3 border-t pt-2 font-mono text-[10px]'>
                  {step.detail}
                </p>
              </div>
            </AnimateInView>
          ))}
        </div>
      </div>
    </section>
  )
}

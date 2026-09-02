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
import { Link } from '@tanstack/react-router'
import { ArrowRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { AnimateInView } from '@/components/animate-in-view'
import { Button } from '@/components/ui/button'

import type { HomeEntryPoint } from '../../lib/home-entry'

interface CTAProps {
  className?: string
  entryPoint: HomeEntryPoint
}

export function CTA(props: CTAProps) {
  const { t } = useTranslation()

  return (
    <section
      className={`border-border/60 relative z-10 border-t px-4 py-14 sm:px-6 md:py-16 ${props.className ?? ''}`}
    >
      <AnimateInView
        className='border-border/60 mx-auto grid max-w-6xl items-center gap-6 border-y py-7 md:grid-cols-[minmax(0,1fr)_auto] md:gap-10'
        animation='fade-in'
      >
        <div className='min-w-0'>
          <p className='text-muted-foreground mb-2 font-mono text-[10px] font-semibold tracking-[0.14em] uppercase'>
            {t('First API request')}
          </p>
          <h2 className='text-xl leading-tight font-semibold md:text-2xl'>
            {t('Give your ideas a clear way forward')}
          </h2>
          <p className='text-muted-foreground mt-2 max-w-2xl text-sm leading-relaxed'>
            {t(
              'Keep the path between your application and its models simple, observable, and yours.'
            )}
          </p>
        </div>
        <div className='flex flex-wrap items-center gap-2 md:justify-end'>
          <Button
            className='h-9 rounded-none px-4 text-xs font-semibold'
            render={<Link to={props.entryPoint.route} />}
          >
            {t(props.entryPoint.labelKey)}
            <ArrowRight aria-hidden='true' className='ml-1.5 size-3.5' />
          </Button>
          <Button
            variant='outline'
            className='border-border/70 h-9 rounded-none px-4 text-xs font-semibold'
            render={<Link to='/pricing' />}
          >
            {t('View Pricing')}
          </Button>
        </div>
      </AnimateInView>
    </section>
  )
}

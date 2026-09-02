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
import {
  Code,
  DollarSign,
  Gauge,
  Globe,
  HeartHandshake,
  Shield,
  Users,
  Zap,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { AnimateInView } from '@/components/animate-in-view'

interface FeaturesProps {
  className?: string
}

export function Features(props: FeaturesProps) {
  const { t } = useTranslation()

  const features = [
    {
      id: 'fast',
      num: '01',
      title: t('Lightning Fast'),
      desc: t(
        'Optimized network architecture ensures millisecond response times'
      ),
      icon: <Zap aria-hidden='true' className='size-4' />,
      meta: [t('compatible API routes'), '/v1/'],
    },
    {
      id: 'secure',
      num: '02',
      title: t('Secure & Reliable'),
      desc: t(
        'Enterprise-grade security with comprehensive permission management'
      ),
      icon: <Shield aria-hidden='true' className='size-4' />,
      meta: [t('Access Policy (JSON)'), t('Request Limits')],
    },
    {
      id: 'global',
      num: '03',
      title: t('Global Coverage'),
      desc: t('Multi-region deployment for stable global access'),
      icon: <Globe aria-hidden='true' className='size-4' />,
      meta: [t('Deployment Region *'), t('Load Balancing')],
    },
    {
      id: 'developer',
      num: '04',
      title: t('Developer Friendly'),
      desc: t('Compatible API routes for common AI application workflows'),
      icon: <Code aria-hidden='true' className='size-4' />,
      meta: [t('Configure routes'), t('Docs')],
    },
  ]

  const additionalFeatures = [
    {
      icon: <Gauge aria-hidden='true' className='size-4' strokeWidth={1.7} />,
      title: t('High Performance'),
      desc: t('Support for high concurrency with automatic load balancing'),
    },
    {
      icon: (
        <DollarSign aria-hidden='true' className='size-4' strokeWidth={1.7} />
      ),
      title: t('Transparent Billing'),
      desc: t('Pay-as-you-go with real-time usage monitoring'),
    },
    {
      icon: <Users aria-hidden='true' className='size-4' strokeWidth={1.7} />,
      title: t('Team Collaboration'),
      desc: t('Multi-user management with flexible permission allocation'),
    },
    {
      icon: (
        <HeartHandshake
          aria-hidden='true'
          className='size-4'
          strokeWidth={1.7}
        />
      ),
      title: t('Open Source'),
      desc: t('Community driven, self-hosted, and extensible'),
    },
  ]

  return (
    <section
      className={`border-border/60 relative z-10 border-t px-4 py-16 sm:px-6 md:py-20 ${props.className ?? ''}`}
    >
      <div className='mx-auto max-w-6xl'>
        <AnimateInView
          className='border-border/60 mb-8 grid gap-3 border-b pb-7 md:grid-cols-[minmax(0,0.7fr)_minmax(0,1.3fr)] md:items-end md:gap-10'
          animation='fade-in'
        >
          <p className='text-muted-foreground font-mono text-[10px] font-semibold tracking-[0.14em] uppercase'>
            {t('The shape of the work')}
          </p>
          <h2 className='max-w-2xl text-2xl leading-tight font-semibold md:text-3xl'>
            {t('Made for the quiet work,')} {t('steady as ideas grow')}
          </h2>
        </AnimateInView>

        <div className='border-border/60 grid border-y md:grid-cols-2'>
          {features.map((feature, index) => {
            const isLastMobile = index === features.length - 1
            const isLastDesktopRow = index >= features.length - 2
            return (
              <AnimateInView
                key={feature.id}
                delay={index * 70}
                animation='fade-up'
                className={`min-w-0 p-5 sm:p-6 ${!isLastMobile ? 'border-border/60 border-b' : ''} ${!isLastDesktopRow ? 'md:border-b' : ''} ${index % 2 === 0 ? 'md:border-border/60 md:border-r' : ''}`}
              >
                <div className='flex items-start gap-3'>
                  <div className='text-primary border-border/70 flex size-8 shrink-0 items-center justify-center border'>
                    {feature.icon}
                  </div>
                  <div className='min-w-0'>
                    <div className='mb-1 flex items-baseline gap-2'>
                      <span className='text-primary font-mono text-[10px] tabular-nums'>
                        {feature.num}
                      </span>
                      <h3 className='text-sm font-semibold'>{feature.title}</h3>
                    </div>
                    <p className='text-muted-foreground text-sm leading-relaxed'>
                      {feature.desc}
                    </p>
                  </div>
                </div>
                <div className='border-border/40 mt-4 flex flex-wrap gap-x-4 gap-y-1 border-t pt-3'>
                  {feature.meta.map((item) => (
                    <span
                      key={item}
                      className='text-muted-foreground font-mono text-[10px]'
                    >
                      {item}
                    </span>
                  ))}
                </div>
              </AnimateInView>
            )
          })}
        </div>

        <div className='border-border/60 mt-10 grid border-y md:grid-cols-2'>
          {additionalFeatures.map((feature, index) => {
            const isLastMobile = index === additionalFeatures.length - 1
            const isLastDesktopRow = index >= additionalFeatures.length - 2
            return (
              <AnimateInView
                key={feature.title}
                delay={index * 70}
                animation='fade-up'
                className={`flex min-w-0 items-start gap-3 p-5 sm:p-6 ${!isLastMobile ? 'border-border/60 border-b' : ''} ${!isLastDesktopRow ? 'md:border-b' : ''} ${index % 2 === 0 ? 'md:border-border/60 md:border-r' : ''}`}
              >
                <div className='text-primary border-border/70 flex size-8 shrink-0 items-center justify-center border'>
                  {feature.icon}
                </div>
                <div className='min-w-0'>
                  <h3 className='text-sm font-semibold'>{feature.title}</h3>
                  <p className='text-muted-foreground mt-1 text-xs leading-relaxed'>
                    {feature.desc}
                  </p>
                </div>
              </AnimateInView>
            )
          })}
        </div>
      </div>
    </section>
  )
}

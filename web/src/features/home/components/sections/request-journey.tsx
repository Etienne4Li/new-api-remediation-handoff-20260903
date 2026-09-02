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
import { AppWindow, Route } from 'lucide-react'
import { motion, useReducedMotion } from 'motion/react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { useSystemConfig } from '@/hooks/use-system-config'
import { cn } from '@/lib/utils'

interface JourneyStageProps {
  description: string
  title: string
}

function JourneyStage(props: JourneyStageProps) {
  return (
    <div className='text-center'>
      <h3 className='text-xl font-semibold text-white md:text-2xl'>
        {props.title}
      </h3>
      <p className='mx-auto mt-2 max-w-md text-sm leading-relaxed text-white/55'>
        {props.description}
      </p>
    </div>
  )
}

function clamp01(value: number) {
  return Math.min(1, Math.max(0, value))
}

function fadeWindow(
  value: number,
  start: number,
  fadeInEnd: number,
  holdEnd: number,
  fadeOutEnd = 1,
  finalOpacity = 0
) {
  if (value <= start) return 0
  if (value <= fadeInEnd) {
    return clamp01((value - start) / (fadeInEnd - start))
  }
  if (value <= holdEnd) return 1
  if (value <= fadeOutEnd) {
    return 1 - clamp01((value - holdEnd) / (fadeOutEnd - holdEnd))
  }
  return finalOpacity
}

export function RequestJourney() {
  const { t } = useTranslation()
  const { systemName, logo } = useSystemConfig()
  const reducedMotion = useReducedMotion()
  const sectionRef = useRef<HTMLElement>(null)
  const [desktop, setDesktop] = useState(false)
  const [scrollProgress, setScrollProgress] = useState(0)

  useEffect(() => {
    const media = window.matchMedia('(min-width: 1024px)')
    const update = () => setDesktop(media.matches)
    update()
    media.addEventListener('change', update)
    return () => media.removeEventListener('change', update)
  }, [])

  const interactive = desktop && !reducedMotion

  useEffect(() => {
    if (!interactive) {
      return
    }

    const updateProgress = () => {
      const section = sectionRef.current
      if (!section) return

      const scrollableDistance = Math.max(
        section.offsetHeight - window.innerHeight,
        1
      )
      const nextProgress = clamp01(
        -section.getBoundingClientRect().top / scrollableDistance
      )

      setScrollProgress((current) =>
        Math.abs(current - nextProgress) > 0.002 ? nextProgress : current
      )
    }

    updateProgress()
    window.addEventListener('scroll', updateProgress, { passive: true })
    window.addEventListener('resize', updateProgress)
    return () => {
      window.removeEventListener('scroll', updateProgress)
      window.removeEventListener('resize', updateProgress)
    }
  }, [interactive])

  const contentOpacity = interactive ? clamp01(scrollProgress / 0.05) : 1
  const railProgress = interactive ? clamp01((scrollProgress - 0.06) / 0.84) : 1
  const sendOpacity = interactive
    ? fadeWindow(scrollProgress, 0.04, 0.09, 0.3, 0.35)
    : 1
  const routeOpacity = interactive
    ? fadeWindow(scrollProgress, 0.36, 0.41, 0.6, 0.65)
    : 0
  const observeOpacity = interactive
    ? fadeWindow(scrollProgress, 0.68, 0.73, 1, 1, 1)
    : 0
  const gatewayGlow = interactive
    ? fadeWindow(scrollProgress, 0.72, 0.78, 0.9, 0.95)
    : 0.35
  const stages = [
    {
      title: t('Send'),
      description: t(
        'The application calls a compatible endpoint with its own key and request parameters.'
      ),
    },
    {
      title: t('Route'),
      description: t(
        'The gateway selects only from upstream channels present in the current configuration.'
      ),
    },
    {
      title: t('Observe'),
      description: t(
        'Available latency, token, cost, and error details are written to real request records.'
      ),
    },
  ]

  return (
    <section
      ref={sectionRef}
      className={cn('relative z-10', interactive && 'lg:h-[340svh]')}
    >
      <div
        className={cn(
          'relative overflow-hidden bg-[#060a13] text-white',
          interactive &&
            'sticky top-0 flex h-svh flex-col justify-center rounded-t-[2.5rem] shadow-[0_-32px_90px_-24px_rgba(0,0,0,0.6)]'
        )}
      >
        <div
          aria-hidden='true'
          className='pointer-events-none absolute inset-0'
          style={{
            background:
              'radial-gradient(circle at 50% 42%, color-mix(in oklab, var(--home-accent) 16%, transparent) 0, transparent 46%)',
          }}
        />
        <div
          aria-hidden='true'
          className='pointer-events-none absolute inset-0 opacity-[0.06]'
          style={{
            backgroundImage:
              'linear-gradient(to right, #fff 1px, transparent 1px), linear-gradient(to bottom, #fff 1px, transparent 1px)',
            backgroundSize: '3.25rem 3.25rem',
            maskImage:
              'radial-gradient(ellipse 70% 60% at 50% 42%, black 20%, transparent 100%)',
          }}
        />

        <div
          className='relative mx-auto w-full max-w-[80rem] px-4 py-20 sm:px-6 lg:px-8 lg:py-0'
          style={{ opacity: contentOpacity }}
        >
          <div className='flex flex-wrap items-end justify-between gap-6'>
            <h2 className='max-w-[28rem] text-3xl leading-tight font-bold md:text-4xl'>
              {t('Follow one request')}
            </h2>
            <dl className='flex items-center gap-8 font-mono'>
              <div>
                <dt className='text-[10px] font-semibold text-white/45 uppercase'>
                  {t('Latency')}
                </dt>
                <dd className='mt-1 text-2xl font-semibold text-white tabular-nums md:text-3xl'>
                  —<span className='ml-1 text-sm text-white/45'>ms</span>
                </dd>
              </div>
              <div>
                <dt className='text-[10px] font-semibold text-white/45 uppercase'>
                  {t('Tokens')}
                </dt>
                <dd className='mt-1 text-2xl font-semibold text-[var(--home-accent)] tabular-nums md:text-3xl'>
                  —
                </dd>
              </div>
            </dl>
          </div>

          <div className='mt-14 flex items-center gap-3 sm:gap-4 lg:mt-20'>
            <div className='flex shrink-0 items-center gap-2 rounded-lg border border-white/15 bg-white/5 px-3 py-2.5 sm:px-4'>
              <AppWindow aria-hidden='true' className='size-4 text-white/60' />
              <span className='text-xs font-semibold text-white/85 sm:text-sm'>
                {t('Your app')}
              </span>
            </div>

            <div className='relative h-px flex-1 bg-white/15'>
              <motion.span
                aria-hidden='true'
                animate={{ scaleX: railProgress }}
                className='absolute inset-0 origin-left bg-[var(--home-accent)]'
                transition={{ duration: 0.2 }}
              />
            </div>

            <div className='relative shrink-0'>
              <motion.span
                aria-hidden='true'
                animate={{
                  opacity: gatewayGlow,
                  scale: 0.9 + gatewayGlow * 0.4,
                }}
                className='absolute -inset-3 rounded-2xl border border-[var(--home-accent)]'
                transition={{ duration: 0.2 }}
              />
              <div className='relative flex items-center gap-2.5 rounded-lg border border-white/20 bg-white/[0.07] px-4 py-3 shadow-[0_0_40px_color-mix(in_oklab,var(--home-accent)_18%,transparent)]'>
                {logo ? (
                  <img
                    src={logo}
                    alt=''
                    aria-hidden='true'
                    className='size-6 object-contain grayscale sm:size-7'
                  />
                ) : (
                  <Route aria-hidden='true' className='size-5 text-white/70' />
                )}
                <span className='text-xs font-bold sm:text-sm'>
                  {systemName}
                </span>
              </div>
            </div>

            <div className='relative h-px flex-1 bg-white/15'>
              <motion.span
                aria-hidden='true'
                animate={{ scaleX: railProgress }}
                className='absolute inset-0 origin-left bg-[var(--home-accent)]'
                transition={{ duration: 0.2 }}
              />
            </div>

            <div className='flex w-20 shrink-0 flex-col gap-2 sm:w-28'>
              {['OpenAI', 'Claude', 'Gemini'].map((provider, index) => {
                const active = interactive
                  ? clamp01((scrollProgress - [0.52, 0.58, 0.64][index]) / 0.05)
                  : 1
                return (
                  <div
                    key={provider}
                    className='rounded-lg border px-3 py-2 text-center text-xs font-semibold'
                    style={{
                      borderColor: `color-mix(in oklab, var(--home-accent) ${15 + active * 85}%, rgba(255,255,255,0.15))`,
                      background: `color-mix(in oklab, var(--home-accent) ${active * 14}%, transparent)`,
                      color: `rgba(255,255,255,${0.4 + active * 0.55})`,
                    }}
                  >
                    {provider}
                  </div>
                )
              })}
            </div>
          </div>

          {interactive ? (
            <div className='relative mt-14 h-28 lg:mt-20'>
              <motion.div
                animate={{ opacity: sendOpacity, y: sendOpacity ? 0 : 20 }}
                className='absolute inset-0'
                transition={{ duration: 0.2 }}
              >
                <JourneyStage {...stages[0]} />
              </motion.div>
              <motion.div
                animate={{ opacity: routeOpacity, y: routeOpacity ? 0 : 20 }}
                className='absolute inset-0'
                transition={{ duration: 0.2 }}
              >
                <JourneyStage {...stages[1]} />
              </motion.div>
              <motion.div
                animate={{
                  opacity: observeOpacity,
                  y: observeOpacity ? 0 : 20,
                }}
                className='absolute inset-0'
                transition={{ duration: 0.2 }}
              >
                <JourneyStage {...stages[2]} />
              </motion.div>
            </div>
          ) : (
            <div className='mt-12 space-y-8'>
              {stages.map((stage, index) => (
                <div key={stage.title} className='flex items-start gap-4'>
                  <span className='font-mono text-xs font-semibold text-[var(--home-accent)]'>
                    {String(index + 1).padStart(2, '0')}
                  </span>
                  <div>
                    <h3 className='text-base font-semibold text-white'>
                      {stage.title}
                    </h3>
                    <p className='mt-1 text-sm leading-relaxed text-white/55'>
                      {stage.description}
                    </p>
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </section>
  )
}

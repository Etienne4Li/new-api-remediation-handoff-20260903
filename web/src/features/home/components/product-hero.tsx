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
import { ArrowRight, BookOpen, Check, Copy } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'

import { isExternalHomeLink, type HomeEntryPoint } from '../lib/home-entry'

interface ProductHeroProps {
  apiEndpoint?: string
  /** Backwards-compatible supporting copy used by the focused layout tests. */
  description?: string
  docsUrl: string
  entryPoint: HomeEntryPoint
  loading: boolean
  logo: string
  statusLoading: boolean
  statusReady: boolean
  systemName: string
  version: string | null
}

const COMPATIBLE_ENDPOINTS = [
  'chat/completions',
  'images/generations',
  'embeddings',
  'audio/speech',
  'models',
] as const

function getStatusLabel(
  t: (key: string) => string,
  statusLoading: boolean,
  statusReady: boolean
) {
  if (statusLoading) return t('Loading...')
  if (statusReady) return t('Online')
  return t('Ready')
}

export function ProductHero(props: ProductHeroProps) {
  const { t } = useTranslation()
  const { copiedText, copyToClipboard } = useCopyToClipboard()
  const [endpointIndex, setEndpointIndex] = useState(0)
  const statusLabel = getStatusLabel(t, props.statusLoading, props.statusReady)
  const endpointCopied = Boolean(
    props.apiEndpoint && copiedText === props.apiEndpoint
  )
  const docsIsExternal = isExternalHomeLink(
    props.docsUrl,
    typeof window === 'undefined' ? 'http://localhost' : window.location.origin
  )

  useEffect(() => {
    const intervalId = window.setInterval(() => {
      setEndpointIndex((current) => (current + 1) % COMPATIBLE_ENDPOINTS.length)
    }, 2600)

    return () => window.clearInterval(intervalId)
  }, [])

  return (
    <section
      aria-labelledby='product-home-title'
      className='home-entry-hero home-entry-started relative isolate z-10 flex min-h-[calc(100svh-8rem)] overflow-hidden px-4 pt-10 pb-10 sm:px-6 sm:pt-12 lg:min-h-[calc(100dvh-4rem)] lg:px-8 lg:pt-10 lg:pb-10'
    >
      <div className='sr-only' aria-live='polite'>
        {statusLabel}
        {props.version ? ` ${t('Version')} ${props.version}` : ''}
      </div>

      <div className='relative z-10 mx-auto flex min-h-0 w-full max-w-[80rem] flex-1 flex-col justify-end'>
        <div className='home-entry-content home-reference-content flex w-full flex-col'>
          <div className='text-muted-foreground mb-5 inline-flex items-center gap-2 font-mono text-[10px] font-semibold tracking-[0.18em] uppercase'>
            <span className='relative flex size-2' aria-hidden='true'>
              <span className='absolute inline-flex size-full animate-ping rounded-full bg-[var(--home-accent)] opacity-60' />
              <span className='relative inline-flex size-2 rounded-full bg-[var(--home-accent)]' />
            </span>
            <span>{t('Compatible API endpoints')}</span>
          </div>

          <div className='w-full max-w-[44rem] sm:w-fit'>
            <div className='border-border/60 bg-background/55 box-border flex max-w-full flex-col items-stretch overflow-hidden border shadow-[0_1px_0_color-mix(in_oklab,var(--background)_68%,transparent)_inset] backdrop-blur-md sm:h-14 sm:flex-row'>
              <div className='flex h-12 min-w-0 items-center gap-2 px-3 sm:h-auto sm:px-4'>
                <code className='text-foreground/88 block min-w-0 flex-1 truncate overflow-hidden bg-transparent py-1 font-mono text-[12px] whitespace-nowrap sm:max-w-[24rem] sm:flex-none'>
                  {props.apiEndpoint || t('Not configured')}
                </code>
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  className='border-border/70 hover:bg-foreground/5 h-11 shrink-0 rounded-none border bg-transparent px-3 font-mono text-[10px] font-semibold tracking-[0.14em] uppercase sm:h-8'
                  title={endpointCopied ? t('Copied!') : t('Copy API endpoint')}
                  aria-label={
                    endpointCopied ? t('Copied!') : t('Copy API endpoint')
                  }
                  disabled={!props.apiEndpoint}
                  onClick={() => {
                    if (props.apiEndpoint) {
                      void copyToClipboard(props.apiEndpoint)
                    }
                  }}
                >
                  {endpointCopied ? (
                    <Check aria-hidden='true' className='size-3.5' />
                  ) : (
                    <Copy aria-hidden='true' className='size-3.5' />
                  )}
                  <span className='hidden sm:inline'>
                    {endpointCopied ? t('Copied!') : t('Copy')}
                  </span>
                </Button>
              </div>
              <div className='border-border/60 bg-foreground/[0.025] flex h-11 w-full shrink-0 items-center border-t px-3 font-mono text-[12px] sm:h-auto sm:w-[clamp(8.5rem,24vw,14rem)] sm:border-t-0 sm:border-l sm:px-4'>
                <span className='text-muted-foreground mr-1 shrink-0'>
                  /v1/
                </span>
                <span className='text-foreground/80 min-w-0 truncate'>
                  {COMPATIBLE_ENDPOINTS[endpointIndex]}
                </span>
                <span className='terminal-demo-blink ml-0.5 inline-block w-[0.55em] text-[var(--home-accent)]'>
                  |
                </span>
              </div>
            </div>
          </div>

          <div className='mt-7 grid min-w-0 items-end gap-8 sm:mt-9 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] lg:gap-12'>
            <div className='min-w-0'>
              <h1
                id='product-home-title'
                aria-label={props.systemName}
                className='max-w-[46rem] text-[clamp(3.35rem,7.5vw,6rem)] leading-[0.9] font-semibold tracking-[-0.028em] text-balance'
              >
                <span className='inline-flex max-w-full items-center gap-[0.1em]'>
                  {props.loading ? (
                    <Skeleton className='bg-foreground/10 h-20 w-64 max-w-full sm:h-24' />
                  ) : (
                    <span className='min-w-0 [overflow-wrap:anywhere]'>
                      {props.systemName}
                    </span>
                  )}
                  {!props.loading && (
                    <img
                      src={props.logo}
                      alt=''
                      aria-hidden='true'
                      className='relative mt-[0.05em] size-[0.76em] shrink-0 object-contain grayscale'
                    />
                  )}
                </span>
                <span className='text-foreground/64 mt-3 block text-[0.58em] leading-[1] tracking-[-0.018em]'>
                  {t('One gateway for supported AI models')}
                </span>
              </h1>
            </div>

            <div className='mb-1 max-w-[34rem] text-left sm:ml-auto sm:text-right lg:w-[34rem] lg:justify-self-end'>
              <p className='text-foreground/88 text-[1.35rem] leading-[1.22] font-semibold text-pretty sm:text-[1.5rem] md:text-[1.62rem] lg:text-[1.72rem]'>
                <span className='block'>
                  {props.description ??
                    t('One gateway for supported model APIs.')}
                </span>
                <span className='mt-1 block'>
                  {t('Transparent pricing with capacity-aware routing.')}
                </span>
                <span className='mt-1 block'>
                  {t(
                    'OpenAI-compatible endpoints are available; some clients need additional settings.'
                  )}
                </span>
              </p>
              <div className='mt-7 flex flex-wrap items-center justify-start gap-3 sm:justify-end'>
                <Button
                  className='group h-14 min-w-[12.5rem] rounded-none px-8 text-base font-semibold sm:h-16 sm:min-w-[14rem]'
                  render={<Link to={props.entryPoint.route} />}
                >
                  {t(props.entryPoint.labelKey)}
                  <ArrowRight
                    aria-hidden='true'
                    className='ml-2 size-5 transition-transform group-hover:translate-x-0.5'
                  />
                </Button>
                <Button
                  variant='ghost'
                  className='group h-14 min-w-[9.75rem] rounded-none px-7 text-base font-semibold sm:h-16 sm:min-w-[10.5rem]'
                  render={
                    docsIsExternal ? (
                      <a
                        href={props.docsUrl}
                        target='_blank'
                        rel='noopener noreferrer'
                      />
                    ) : (
                      <Link to={props.docsUrl} />
                    )
                  }
                >
                  <BookOpen aria-hidden='true' className='size-5' />
                  {t('Docs')}
                </Button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  )
}

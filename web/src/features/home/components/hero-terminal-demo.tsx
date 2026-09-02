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
  Braces,
  Check,
  CircleDot,
  MessageSquareText,
  Radio,
  Sparkles,
  Workflow,
  type LucideIcon,
} from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

type RouteTone = 'emerald' | 'amber' | 'blue' | 'violet'

interface GatewayRoute {
  id: string
  label: string
  endpoint: string
  icon: LucideIcon
  tone: RouteTone
  request: readonly string[]
  response: readonly string[]
}

const ROUTES: readonly GatewayRoute[] = [
  {
    id: 'openai-chat',
    label: 'OpenAI',
    endpoint: '/v1/chat/completions',
    icon: MessageSquareText,
    tone: 'emerald',
    request: [
      'POST /v1/chat/completions',
      'Authorization: Bearer sk-****',
      '{ "model": "your-model",',
      '  "messages": [{ "role": "user", "content": "..." }],',
      '  "stream": true }',
    ],
    response: ['HTTP 200', 'content-type: text/event-stream'],
  },
  {
    id: 'responses',
    label: 'Responses',
    endpoint: '/v1/responses',
    icon: Workflow,
    tone: 'amber',
    request: [
      'POST /v1/responses',
      'Authorization: Bearer sk-****',
      '{ "model": "your-model",',
      '  "input": "...",',
      '  "stream": true }',
    ],
    response: ['HTTP 200', 'event: response.output_text.delta'],
  },
  {
    id: 'claude',
    label: 'Claude',
    endpoint: '/v1/messages',
    icon: Braces,
    tone: 'blue',
    request: [
      'POST /v1/messages',
      'x-api-key: sk-****',
      'anthropic-version: 2023-06-01',
      '{ "model": "your-model",',
      '  "messages": [{ "role": "user", "content": "..." }] }',
    ],
    response: ['HTTP 200', 'content-type: application/json'],
  },
  {
    id: 'gemini',
    label: 'Gemini',
    endpoint: '/v1beta/models',
    icon: Sparkles,
    tone: 'violet',
    request: [
      'POST /v1beta/models/{model}:generateContent',
      'x-goog-api-key: sk-****',
      '{ "contents": [{',
      '  "role": "user",',
      '  "parts": [{ "text": "..." }] }] }',
    ],
    response: ['HTTP 200', 'content-type: application/json'],
  },
]

const TONE_CLASSES: Record<
  RouteTone,
  { icon: string; selected: string; dot: string }
> = {
  emerald: {
    icon: 'text-emerald-600 dark:text-emerald-400',
    selected: 'border-emerald-500/40 bg-emerald-500/8',
    dot: 'bg-emerald-500',
  },
  amber: {
    icon: 'text-amber-600 dark:text-amber-400',
    selected: 'border-amber-500/40 bg-amber-500/8',
    dot: 'bg-amber-500',
  },
  blue: {
    icon: 'text-blue-600 dark:text-blue-400',
    selected: 'border-blue-500/40 bg-blue-500/8',
    dot: 'bg-blue-500',
  },
  violet: {
    icon: 'text-violet-600 dark:text-violet-400',
    selected: 'border-violet-500/40 bg-violet-500/8',
    dot: 'bg-violet-500',
  },
}

interface HeroTerminalDemoProps {
  className?: string
  statusLoading?: boolean
  statusReady?: boolean
  systemName?: string
  version?: string | null
}

function getGatewayStatusLabel(
  t: (key: string) => string,
  statusLoading?: boolean,
  statusReady?: boolean
) {
  if (statusLoading) return t('Loading...')
  if (statusReady) return t('Online')
  return t('Ready')
}

function getRequestLineClass(index: number) {
  if (index === 0) return 'text-emerald-700 dark:text-emerald-300'
  if (index === 1) return 'text-blue-700 dark:text-blue-300'
  return 'text-foreground/65'
}

function getVersionLabel(version: string) {
  return version.startsWith('v') ? version : `v${version}`
}

export function HeroTerminalDemo(props: HeroTerminalDemoProps) {
  const { t } = useTranslation()
  const [activeRouteId, setActiveRouteId] = useState(ROUTES[0].id)
  const activeRoute =
    ROUTES.find((route) => route.id === activeRouteId) ?? ROUTES[0]
  const statusLabel = getGatewayStatusLabel(
    t,
    props.statusLoading,
    props.statusReady
  )

  return (
    <section
      aria-label={t('API Requests')}
      data-slot='gateway-console'
      className={cn(
        'bg-card border-border/80 min-w-0 overflow-hidden rounded-lg border shadow-lg shadow-black/5',
        'dark:border-white/10 dark:bg-[#0d1110] dark:shadow-black/30',
        props.className
      )}
    >
      <header className='border-border/70 bg-muted/35 flex h-10 min-w-0 items-center gap-2 border-b px-3 dark:border-white/10 dark:bg-white/[0.03]'>
        <CircleDot className='size-3.5 shrink-0 text-emerald-500' />
        <span className='min-w-0 truncate text-xs font-semibold'>
          {props.systemName || t('API Access')}
        </span>
        <span className='text-muted-foreground hidden text-[10px] sm:inline'>
          /
        </span>
        <span className='text-muted-foreground hidden text-[10px] sm:inline'>
          {t('Routing & Overrides')}
        </span>
        <div className='ml-auto flex shrink-0 items-center gap-2'>
          {props.version ? (
            <code className='text-muted-foreground hidden text-[10px] sm:block'>
              {getVersionLabel(props.version)}
            </code>
          ) : null}
          <span className='border-border/70 bg-background/80 flex items-center gap-1.5 rounded-md border px-2 py-1 text-[10px] font-medium dark:border-white/10 dark:bg-white/[0.04]'>
            <span
              className={cn(
                'size-1.5 rounded-full',
                props.statusLoading ? 'bg-amber-500' : 'bg-emerald-500'
              )}
            />
            {statusLabel}
          </span>
        </div>
      </header>

      <div className='grid min-w-0 md:grid-cols-[188px_minmax(0,1fr)]'>
        <nav
          aria-label={t('compatible API routes')}
          className='border-border/70 grid min-w-0 grid-cols-4 gap-1 border-b p-2 md:block md:border-r md:border-b-0 md:p-3 dark:border-white/10'
        >
          <div className='text-muted-foreground mb-2 hidden items-center justify-between px-1 text-[10px] font-semibold uppercase md:flex'>
            <span>{t('Routes')}</span>
            <span>{ROUTES.length}</span>
          </div>
          {ROUTES.map((route) => {
            const Icon = route.icon
            const routeTone = TONE_CLASSES[route.tone]
            const selected = activeRoute.id === route.id

            return (
              <button
                key={route.id}
                type='button'
                aria-pressed={selected}
                onClick={() => setActiveRouteId(route.id)}
                className={cn(
                  'hover:bg-muted/60 flex h-9 min-w-0 items-center gap-2 rounded-md border border-transparent px-2 text-left transition-colors md:mb-1 md:h-12 md:w-full',
                  selected && routeTone.selected
                )}
              >
                <Icon
                  aria-hidden='true'
                  className={cn(
                    'hidden size-3.5 shrink-0 sm:block',
                    routeTone.icon
                  )}
                />
                <span className='min-w-0'>
                  <span className='block truncate text-[11px] font-semibold md:text-xs'>
                    {route.label}
                  </span>
                  <code className='text-muted-foreground hidden truncate text-[9px] md:block'>
                    {route.endpoint}
                  </code>
                </span>
                <span
                  aria-hidden='true'
                  className={cn(
                    'ml-auto hidden size-1.5 shrink-0 rounded-full md:block',
                    routeTone.dot
                  )}
                />
              </button>
            )
          })}
        </nav>

        <div className='min-w-0'>
          <div className='border-border/70 flex h-10 min-w-0 items-center gap-2 border-b px-3 dark:border-white/10'>
            <span className='rounded-md bg-blue-500/10 px-1.5 py-0.5 font-mono text-[9px] font-semibold text-blue-700 dark:text-blue-300'>
              POST
            </span>
            <code className='min-w-0 truncate text-[10px] sm:text-xs'>
              {activeRoute.endpoint}
            </code>
            <span className='text-muted-foreground ml-auto flex shrink-0 items-center gap-1 text-[9px]'>
              <Radio aria-hidden='true' className='size-3' />
              SSE
            </span>
          </div>

          <div className='grid h-[112px] min-w-0 grid-rows-[1fr_40px] font-mono sm:h-[190px] sm:grid-rows-[1fr_48px] lg:h-[240px]'>
            <div className='min-w-0 overflow-hidden px-3 py-2.5 sm:px-4 sm:py-3'>
              <div className='text-muted-foreground mb-1.5 flex items-center justify-between font-sans text-[9px] font-semibold uppercase'>
                <span>{t('Request')}</span>
                <span>JSON</span>
              </div>
              <div className='space-y-0.5 text-[9px] leading-4 sm:text-[11px] sm:leading-5'>
                {activeRoute.request.map((line, index) => (
                  <div
                    key={`${activeRoute.id}-${line}`}
                    className={cn(
                      'max-w-full truncate',
                      getRequestLineClass(index)
                    )}
                  >
                    {line}
                  </div>
                ))}
              </div>
            </div>

            <div className='border-border/70 bg-muted/25 flex min-w-0 items-center gap-2 overflow-hidden border-t px-3 dark:border-white/10 dark:bg-white/[0.02]'>
              <span className='text-muted-foreground font-sans text-[9px] font-semibold uppercase'>
                {t('Response')}
              </span>
              <span className='flex shrink-0 items-center gap-1 font-sans text-[9px] font-semibold text-emerald-600 dark:text-emerald-400'>
                <Check aria-hidden='true' className='size-3' />
                {activeRoute.response[0]}
              </span>
              <code className='text-muted-foreground min-w-0 truncate text-[9px]'>
                {activeRoute.response[1]}
              </code>
            </div>
          </div>
        </div>
      </div>

      <footer className='border-border/70 bg-muted/20 grid grid-cols-3 border-t dark:border-white/10 dark:bg-white/[0.02]'>
        {[t('Authentication'), t('Streaming'), t('Billing')].map((label) => (
          <div
            key={label}
            className='border-border/60 flex min-w-0 items-center justify-center gap-1.5 border-r px-1 py-2 last:border-r-0 sm:py-2.5 dark:border-white/10'
          >
            <Check
              aria-hidden='true'
              className='size-3 shrink-0 text-emerald-500'
            />
            <span className='truncate text-[9px] font-medium sm:text-[10px]'>
              {label}
            </span>
          </div>
        ))}
      </footer>
    </section>
  )
}

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
import {
  ArrowUpRight,
  BadgeCheck,
  CircleDollarSign,
  FileCheck2,
  MapPin,
  MessageSquareText,
  Route,
  TicketCheck,
} from 'lucide-react'
import type { CSSProperties, ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { AnimateInView } from '@/components/animate-in-view'
import { Skeleton } from '@/components/ui/skeleton'
import type { SystemStatus } from '@/features/auth/types'
import { useApiInfo } from '@/features/dashboard/hooks/use-status-data'
import { useStatus } from '@/hooks/use-status'
import { cn } from '@/lib/utils'

interface PublicReport {
  description: string
  id: string
  publishedDate: string
  provider: string
  sort: number
  status: string
  title: string
  url: string
}

interface OperatingInformationProps {
  apiEndpoint?: string
  docsUrl: string
}

type FlowChipStyle = CSSProperties & { '--flow-stagger': string }

const REQUEST_FORMATS = [
  'OpenAI',
  'Claude',
  'Gemini',
  'DeepSeek',
  'Qwen',
  'GLM',
] as const

function getStatusValue(status: SystemStatus | null, key: string): unknown {
  if (!status) return undefined
  if (key in status) return status[key]
  return status.data?.[key]
}

function getStatusString(status: SystemStatus | null, key: string): string {
  const value = getStatusValue(status, key)
  if (typeof value === 'string') return value.trim()
  return typeof value === 'number' && Number.isFinite(value)
    ? String(value)
    : ''
}

function getPublicReports(value: unknown): PublicReport[] {
  const source = (() => {
    if (Array.isArray(value)) return value
    if (typeof value !== 'string' || !value.trim()) return []
    try {
      const parsed = JSON.parse(value) as unknown
      return Array.isArray(parsed) ? parsed : []
    } catch {
      return []
    }
  })()

  return source
    .flatMap((candidate, index) => {
      if (!candidate || typeof candidate !== 'object') return []
      const report = candidate as Record<string, unknown>
      const provider =
        typeof report.provider === 'string' ? report.provider.trim() : ''
      const title = typeof report.title === 'string' ? report.title.trim() : ''
      const url = typeof report.url === 'string' ? report.url.trim() : ''
      if (report.enabled === false || !provider || !title || !url) return []

      try {
        const parsedUrl = new URL(url)
        if (!['http:', 'https:'].includes(parsedUrl.protocol)) return []
      } catch {
        return []
      }

      return [
        {
          description:
            typeof report.description === 'string'
              ? report.description.trim()
              : '',
          id: String(report.id ?? index),
          publishedDate:
            typeof report.publishedDate === 'string'
              ? report.publishedDate.trim()
              : '',
          provider,
          sort:
            typeof report.sort === 'number' && Number.isFinite(report.sort)
              ? report.sort
              : index,
          status:
            report.status === 'reference' || report.status === 'archived'
              ? report.status
              : 'verified',
          title,
          url,
        },
      ]
    })
    .sort((left, right) =>
      left.sort !== right.sort
        ? left.sort - right.sort
        : left.title.localeCompare(right.title)
    )
}

function ReportStatusBadge(props: { status: string }) {
  const { t } = useTranslation()
  let styles = {
    badge: 'bg-success/15 text-foreground',
    dot: 'bg-success',
    label: t('Verified'),
  }

  if (props.status === 'reference') {
    styles = {
      badge: 'bg-info/15 text-foreground',
      dot: 'bg-info',
      label: t('Reference'),
    }
  } else if (props.status === 'archived') {
    styles = {
      badge: 'bg-muted text-muted-foreground',
      dot: 'bg-muted-foreground',
      label: t('Archived'),
    }
  }

  return (
    <span
      className={`${styles.badge} flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[10px] font-semibold`}
    >
      <span
        aria-hidden='true'
        className={`${styles.dot} size-1.5 rounded-full`}
      />
      {styles.label}
    </span>
  )
}

function InformationCell(props: {
  children: ReactNode
  className?: string
  description: string
  icon: ReactNode
  title: string
}) {
  return (
    <article
      className={cn(
        'bg-background group border-border/40 relative min-w-0 rounded-xl border p-5 transition-colors duration-300 sm:p-6 md:rounded-none md:border-0 md:p-8',
        props.className
      )}
      onPointerMove={(event) => {
        const rect = event.currentTarget.getBoundingClientRect()
        event.currentTarget.style.setProperty(
          '--spot-x',
          `${event.clientX - rect.left}px`
        )
        event.currentTarget.style.setProperty(
          '--spot-y',
          `${event.clientY - rect.top}px`
        )
      }}
    >
      <div
        aria-hidden='true'
        className='pointer-events-none absolute inset-0 opacity-0 transition-opacity duration-300 group-hover:opacity-100'
        style={{
          background:
            'radial-gradient(20rem circle at var(--spot-x, 50%) var(--spot-y, 50%), color-mix(in oklch, var(--home-accent) 9%, transparent), transparent 70%)',
        }}
      />
      <div className='mb-3 flex items-center gap-3'>
        <span className='border-border/40 bg-muted flex size-7 shrink-0 items-center justify-center rounded-md border'>
          {props.icon}
        </span>
        <h3 className='text-sm font-semibold'>{props.title}</h3>
      </div>
      <p className='text-muted-foreground text-sm leading-relaxed'>
        {props.description}
      </p>
      {props.children}
    </article>
  )
}

export function OperatingInformation(props: OperatingInformationProps) {
  const { t } = useTranslation()
  const { status } = useStatus()
  const { items: apiInfo, loading } = useApiInfo()
  const reports =
    getStatusValue(status, 'third_party_reports_enabled') === false
      ? []
      : getPublicReports(getStatusValue(status, 'third_party_reports'))
  const telegramName = getStatusString(status, 'telegram_bot_name')
  const qqGroup = getStatusString(status, 'after_sales_qq_group')
  const supportConfigured = Boolean(telegramName || qqGroup)
  const primaryEndpoint = props.apiEndpoint?.trim() ?? ''

  return (
    <section className='relative z-10 px-4 py-16 sm:px-6 sm:py-20 md:py-32 lg:rounded-t-[2.5rem] lg:px-8 lg:shadow-[0_-24px_70px_-20px_rgba(0,0,0,0.28)]'>
      <div className='mx-auto max-w-[80rem]'>
        <AnimateInView className='mb-12 max-w-lg sm:mb-16'>
          <h2 className='text-2xl leading-tight font-bold tracking-tight sm:text-3xl md:text-4xl'>
            {t('Clear routes, current prices, public references')}
          </h2>
          <p className='text-muted-foreground mt-3 text-sm leading-relaxed'>
            {t(
              'Configured facts stay visible and missing data stays plainly empty.'
            )}
          </p>
        </AnimateInView>

        <div className='md:border-border/40 md:bg-border/40 grid gap-3 rounded-xl sm:gap-px md:grid-cols-3 md:grid-rows-[21rem_18.75rem_26.25rem] md:gap-px md:overflow-hidden md:border'>
          <InformationCell
            className='md:col-span-2'
            title={t('One API, familiar request formats')}
            description={t(
              'Compatible request formats are routed only through upstream channels configured by the administrator.'
            )}
            icon={
              <Route
                aria-hidden='true'
                className='size-4 text-[color:var(--home-accent-soft)]'
              />
            }
          >
            <div className='mt-5 select-none'>
              <div className='flex items-center gap-3'>
                <span className='border-border/40 bg-muted/30 flex shrink-0 items-center gap-2 rounded-lg border px-3 py-2 font-mono text-[11px] font-semibold'>
                  <Route
                    aria-hidden='true'
                    className='size-3.5 text-[color:var(--home-accent)]'
                  />
                  {t('Your request')}
                </span>
                <span className='bg-border/50 relative h-px flex-1 overflow-hidden'>
                  <span className='bg-border/50 absolute inset-0' />
                  <span className='flow-dash absolute inset-0 bg-[var(--home-accent)]' />
                </span>
              </div>
            </div>

            {loading ? (
              <div className='mt-4 grid grid-cols-3 gap-2'>
                {REQUEST_FORMATS.map((format) => (
                  <Skeleton key={format} className='h-9 rounded-lg' />
                ))}
              </div>
            ) : (
              <div className='mt-4 grid grid-cols-3 gap-2'>
                {REQUEST_FORMATS.map((format, index) => (
                  <div
                    key={format}
                    className='flow-chip border-border/30 bg-muted/20 text-muted-foreground flex min-h-9 items-center justify-center rounded-lg border px-2 py-2 text-center text-[11px]'
                    style={
                      { '--flow-stagger': `${index * 0.45}s` } as FlowChipStyle
                    }
                  >
                    {format}
                  </div>
                ))}
              </div>
            )}
            <p className='text-muted-foreground sr-only mt-3 font-mono text-[10px]'>
              {apiInfo.length > 0
                ? t('{{count}} public API routes configured', {
                    count: apiInfo.length,
                  })
                : t('Public API routes are not configured yet')}
            </p>
          </InformationCell>

          <InformationCell
            title={t('Third-party reports')}
            description={t(
              'Independent references appear here when configured.'
            )}
            icon={
              <FileCheck2
                aria-hidden='true'
                className='size-4 text-[color:var(--home-accent-soft)]'
              />
            }
          >
            {reports.length > 0 ? (
              <div className='mt-4 space-y-2'>
                {reports.slice(0, 3).map((report) => (
                  <a
                    key={report.id}
                    href={report.url}
                    target='_blank'
                    rel='noopener noreferrer'
                    className='border-border/40 bg-muted/20 hover:bg-muted/30 group/report flex items-start justify-between gap-3 rounded-lg border p-3 transition-colors'
                  >
                    <span className='min-w-0 space-y-1.5'>
                      <span className='flex min-w-0 flex-wrap items-center gap-2'>
                        <span className='text-muted-foreground max-w-32 truncate font-mono text-[10px] font-semibold tracking-[0.14em]'>
                          {report.provider}
                        </span>
                        <ReportStatusBadge status={report.status} />
                      </span>
                      <span className='text-foreground block truncate text-sm leading-tight font-semibold'>
                        {report.title}
                      </span>
                      {report.description && (
                        <span className='text-muted-foreground block truncate text-[11px] leading-relaxed'>
                          {report.description}
                        </span>
                      )}
                    </span>
                    <span className='text-muted-foreground group-hover/report:text-foreground mt-0.5 shrink-0 text-[11px] whitespace-nowrap transition-colors'>
                      {t('View report')} ↗
                    </span>
                  </a>
                ))}
                {reports.length > 3 && (
                  <span className='text-muted-foreground mt-1 block text-[11px] font-medium'>
                    {t('View all reports')} ({reports.length})
                  </span>
                )}
              </div>
            ) : (
              <div className='border-border/40 bg-muted/20 text-muted-foreground mt-4 flex min-h-28 items-center justify-center rounded-lg border px-4 text-center text-sm'>
                {t('No public reports available')}
              </div>
            )}
          </InformationCell>

          <InformationCell
            className='md:col-span-2'
            title={t('Transparent pricing')}
            description={t(
              'Model prices and multipliers come from the live pricing page and actual usage records.'
            )}
            icon={
              <CircleDollarSign
                aria-hidden='true'
                className='size-4 text-emerald-500'
              />
            }
          >
            <dl className='mt-5 space-y-2.5'>
              {[
                [t('Model prices'), t('Live pricing page')],
                [t('Quota conversion'), t('Shown before purchase')],
                [t('Usage records'), t('Measured per request')],
              ].map(([label, value]) => (
                <div
                  key={label}
                  className='flex items-center justify-between gap-3'
                >
                  <dt className='text-muted-foreground text-xs'>{label}</dt>
                  <dd className='text-xs font-semibold'>{value}</dd>
                </div>
              ))}
            </dl>
            <div className='border-border/40 bg-muted/20 text-muted-foreground mt-3 rounded-lg border px-3 py-2.5 text-[11px] leading-relaxed'>
              {t(
                'Billing follows the current pricing page, purchase terms, and per-request usage record.'
              )}
            </div>
            <Link
              to='/pricing'
              className='text-muted-foreground hover:text-foreground mt-3 inline-flex items-center gap-1.5 text-[11px] font-semibold transition-colors'
            >
              {t('View current pricing')}
              <ArrowUpRight aria-hidden='true' className='size-3.5' />
            </Link>
          </InformationCell>

          <InformationCell
            title={t('Upstream transparency')}
            description={t(
              'Only information exposed by the current system configuration is shown.'
            )}
            icon={
              <BadgeCheck
                aria-hidden='true'
                className='size-4 text-[color:var(--home-accent-soft)]'
              />
            }
          >
            <div className='mt-5 space-y-2'>
              {[
                apiInfo.length > 0
                  ? t('Published API route information configured')
                  : t('No public API route information configured'),
                reports.length > 0
                  ? t('Third-party reports when available')
                  : t('No third-party reports configured'),
                t('Availability may change'),
              ].map((label) => (
                <div key={label} className='flex items-center gap-2.5'>
                  <span className='flex size-5 shrink-0 items-center justify-center rounded-full bg-[color-mix(in_oklch,var(--home-accent)_15%,transparent)]'>
                    <BadgeCheck
                      aria-hidden='true'
                      className='size-3 text-[color:var(--home-accent)]'
                    />
                  </span>
                  <span className='text-foreground/80 text-xs'>{label}</span>
                </div>
              ))}
            </div>
          </InformationCell>

          <InformationCell
            title={t('Regional access options')}
            description={t(
              'Availability and latency depend on the endpoints published by the administrator.'
            )}
            icon={
              <MapPin aria-hidden='true' className='size-4 text-violet-500' />
            }
          >
            <div className='mt-5 space-y-2'>
              {[
                primaryEndpoint
                  ? t('Primary endpoint: {{endpoint}}', {
                      endpoint: primaryEndpoint,
                    })
                  : t('Primary endpoint is not configured'),
                apiInfo.length > 0
                  ? t('{{count}} published route(s) listed', {
                      count: apiInfo.length,
                    })
                  : t('No published routes listed'),
                t('Regional availability may change'),
              ].map((label) => (
                <div key={label} className='flex items-center gap-2.5'>
                  <span className='flex size-5 shrink-0 items-center justify-center rounded-full bg-violet-500/15'>
                    <MapPin
                      aria-hidden='true'
                      className='size-3 text-violet-400'
                    />
                  </span>
                  <span className='text-foreground/80 min-w-0 truncate text-xs'>
                    {label}
                  </span>
                </div>
              ))}
            </div>
          </InformationCell>

          <InformationCell
            className='md:col-span-2'
            title={t('On-site ticket support')}
            description={t(
              supportConfigured
                ? 'On-site tickets and configured feedback channels are available.'
                : 'In-app tickets are available; no external feedback channel is configured.'
            )}
            icon={
              <TicketCheck
                aria-hidden='true'
                className='size-4 text-amber-500'
              />
            }
          >
            <div className='mt-5 flex flex-wrap gap-2'>
              <span className='border-border/40 bg-muted/20 inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-[11px] font-medium'>
                <MessageSquareText
                  aria-hidden='true'
                  className='size-3.5 text-amber-500'
                />
                {t('In-app tickets')}
              </span>
              {telegramName && (
                <a
                  href={`https://t.me/${telegramName.replace(/^@/, '')}`}
                  target='_blank'
                  rel='noopener noreferrer'
                  className='border-border/40 bg-muted/20 inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-[11px] font-medium'
                >
                  {t('Telegram')}
                  <ArrowUpRight aria-hidden='true' className='size-3' />
                </a>
              )}
              {qqGroup && (
                <span className='border-border/40 bg-muted/20 inline-flex items-center gap-1.5 rounded-lg border px-2.5 py-1 text-[11px] font-medium'>
                  {t('QQ group')} {qqGroup}
                </span>
              )}
              <a
                href={props.docsUrl}
                target={props.docsUrl.startsWith('http') ? '_blank' : undefined}
                rel={
                  props.docsUrl.startsWith('http')
                    ? 'noopener noreferrer'
                    : undefined
                }
                className='text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 px-2 py-1 text-[11px] font-semibold transition-colors'
              >
                {t('Documentation')}
                <ArrowUpRight aria-hidden='true' className='size-3' />
              </a>
            </div>
          </InformationCell>
        </div>
      </div>
    </section>
  )
}

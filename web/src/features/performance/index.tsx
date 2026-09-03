/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.
*/
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import {
  Activity,
  AlertTriangle,
  ArrowUpDown,
  ChevronDown,
  ChevronRight,
  ChevronUp,
  CircleDashed,
  Database,
  Gauge,
  GripVertical,
  HeartPulse,
  RefreshCw,
  Timer,
} from 'lucide-react'
import { useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import {
  StaticDataTable,
  staticDataTableClassNames,
} from '@/components/data-table'
import {
  sideDrawerContentClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { SectionPageLayout } from '@/components/layout'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import {
  getPerfMetrics,
  getPerfMetricsSummary,
  updatePerfGroupOrder,
} from '@/features/performance-metrics/api'
import {
  formatLatency,
  formatThroughput,
  formatUptimePct,
  getSuccessRateTextClass,
} from '@/features/performance-metrics/lib/format'
import type { PerfModelSummary } from '@/features/performance-metrics/types'
import { getUserGroups } from '@/lib/api'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

import {
  aggregatePerformanceGroups,
  getCacheHitRate,
  getOverallSuccessRate,
  getModelGroupRows,
  getPerformanceStatus,
  getRecentStatusSeries,
  mergeConfiguredPerformanceGroups,
  orderPerformanceGroups,
  type AggregatedPerformanceGroup,
  type ModelPerformanceDetail,
  type PerformanceGroupConfig,
  type PerformanceStatus,
} from './lib/aggregate'

const REFRESH_INTERVAL_MS = 60_000
const LOADING_CARD_KEYS = [
  'loading-a',
  'loading-b',
  'loading-c',
  'loading-d',
  'loading-e',
  'loading-f',
] as const
const WINDOW_OPTIONS = [
  { hours: 24, label: '24 Hours' },
  { hours: 24 * 7, label: '7 Days' },
] as const

type WindowHours = (typeof WINDOW_OPTIONS)[number]['hours']
type StatusFilter = PerformanceStatus | 'all'

const STATUS_FILTERS: Array<{
  value: StatusFilter
  label: string
}> = [
  { value: 'all', label: 'All' },
  { value: 'healthy', label: 'Healthy' },
  { value: 'degraded', label: 'Degraded performance recently' },
  { value: 'critical', label: 'Warning' },
  { value: 'empty', label: 'No data' },
]

function formatCount(value: number | undefined): string {
  if (value == null || !Number.isFinite(value)) return '—'
  return Intl.NumberFormat().format(value)
}

function formatCacheHitRate(value: number, unavailableLabel: string): string {
  return Number.isFinite(value) ? formatUptimePct(value) : unavailableLabel
}

function getWorkspaceCacheHitRate(
  groups: AggregatedPerformanceGroup[]
): number {
  const inputTokens = groups.reduce((sum, group) => sum + group.inputTokens, 0)
  if (inputTokens <= 0) return Number.NaN
  const cacheReadTokens = groups.reduce(
    (sum, group) => sum + group.cacheReadTokens,
    0
  )
  return (cacheReadTokens / inputTokens) * 100
}

function formatGroupRatio(value: number | string | undefined): string | null {
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) return null
    return `×${new Intl.NumberFormat(undefined, {
      maximumFractionDigits: 2,
    }).format(value)}`
  }
  if (typeof value !== 'string' || value.trim() === '') return null
  return value.trim() === '自动' ? value.trim() : `×${value.trim()}`
}

function statusClasses(status: PerformanceStatus) {
  switch (status) {
    case 'healthy':
      return {
        dot: 'bg-emerald-500',
        text: 'text-emerald-600 dark:text-emerald-400',
        segment: 'bg-emerald-500',
      }
    case 'degraded':
      return {
        dot: 'bg-amber-500',
        text: 'text-amber-600 dark:text-amber-400',
        segment: 'bg-amber-500',
      }
    case 'critical':
      return {
        dot: 'bg-red-500',
        text: 'text-red-600 dark:text-red-400',
        segment: 'bg-red-500',
      }
    default:
      return {
        dot: 'bg-muted-foreground/50',
        text: 'text-muted-foreground',
        segment: 'bg-muted-foreground/30',
      }
  }
}

function statusLabel(t: (key: string) => string, status: PerformanceStatus) {
  switch (status) {
    case 'healthy':
      return t('Healthy')
    case 'degraded':
      return t('Degraded performance recently')
    case 'critical':
      return t('Warning')
    default:
      return t('No data')
  }
}

function average(values: number[]): number {
  const valid = values.filter((value) => Number.isFinite(value) && value > 0)
  if (valid.length === 0) return Number.NaN
  return valid.reduce((sum, value) => sum + value, 0) / valid.length
}

function weightedAverage(
  groups: AggregatedPerformanceGroup[],
  select: (group: AggregatedPerformanceGroup) => number
): number {
  let weightedSum = 0
  let weight = 0
  for (const group of groups) {
    const value = select(group)
    if (!Number.isFinite(value) || value <= 0 || group.requestCount <= 0) {
      continue
    }
    weightedSum += value * group.requestCount
    weight += group.requestCount
  }
  return weight > 0
    ? weightedSum / weight
    : average(groups.map((group) => select(group)))
}

function getSeriesStatus(
  value: number,
  fallback: PerformanceStatus
): PerformanceStatus {
  return getPerformanceStatus(value, Number.isFinite(value)) === 'empty'
    ? fallback
    : getPerformanceStatus(value)
}

function SummaryMetric(props: {
  icon: typeof Activity
  label: string
  value: React.ReactNode
  tone?: string
}) {
  const Icon = props.icon
  return (
    <div className='bg-card min-w-0 rounded-lg border px-3 py-3 sm:px-4'>
      <div className='text-muted-foreground flex items-center gap-1.5 text-[11px] font-medium'>
        <Icon className='size-3.5' aria-hidden='true' />
        <span className='truncate'>{props.label}</span>
      </div>
      <div
        className={cn(
          'text-foreground mt-1 font-mono text-lg font-semibold tabular-nums sm:text-xl',
          props.tone
        )}
      >
        {props.value}
      </div>
    </div>
  )
}

function StatusSegments(props: {
  group: AggregatedPerformanceGroup
  fallback: PerformanceStatus
}) {
  const points = getRecentStatusSeries(props.group)
  const values =
    points.length > 0
      ? points
      : [{ ts: 0, successRate: props.group.successRate }]
  return (
    <div className='flex h-1.5 min-w-0 gap-0.5' aria-hidden='true'>
      {values.map((point) => {
        const status = getSeriesStatus(point.successRate, props.fallback)
        return (
          <span
            key={point.ts}
            className={cn(
              'min-w-0 flex-1 rounded-full',
              statusClasses(status).segment
            )}
          />
        )
      })}
    </div>
  )
}

function PerformanceGroupCard(props: {
  group: AggregatedPerformanceGroup
  onOpen: () => void
  reorder?: {
    onMoveUp?: () => void
    onMoveDown?: () => void
    onDragStart: () => void
    onDrop: () => void
  }
}) {
  const { t } = useTranslation()
  const status = getPerformanceStatus(
    props.group.successRate,
    props.group.modelNames.length > 0
  )
  const colors = statusClasses(status)
  const ratio = formatGroupRatio(props.group.ratio)

  const baseClassName =
    'group bg-card hover:bg-muted/30 focus-visible:ring-ring/50 w-full text-left transition-colors focus-visible:ring-3 focus-visible:outline-none'

  const content = (
    <Card className='group-hover:border-primary/40 h-full rounded-lg shadow-none transition-colors'>
      <CardHeader className='gap-3 pb-0'>
        <div className='flex items-start justify-between gap-3'>
          <div className='min-w-0'>
            <div className='flex min-w-0 items-center gap-2'>
              <CardTitle className='min-w-0 truncate font-mono text-sm font-semibold'>
                {props.group.group}
              </CardTitle>
              {ratio && (
                <span className='text-muted-foreground shrink-0 font-mono text-[11px]'>
                  {ratio}
                </span>
              )}
            </div>
            {props.group.description && (
              <p className='text-muted-foreground mt-1 line-clamp-2 text-xs leading-snug'>
                {props.group.description}
              </p>
            )}
            <div className='text-muted-foreground mt-1 flex items-center gap-1.5 text-xs'>
              <span
                className={cn('size-1.5 rounded-full', colors.dot)}
                aria-hidden='true'
              />
              <span className={cn('truncate', colors.text)}>
                {statusLabel(t, status)}
              </span>
              <span className='text-muted-foreground/60'>·</span>
              <span className='shrink-0'>
                {props.group.modelNames.length > 0
                  ? `${props.group.modelNames.length} ${t('Models')}`
                  : t('No data')}
              </span>
              {props.group.requestCount > 0 && (
                <>
                  <span className='text-muted-foreground/60'>·</span>
                  <span className='shrink-0'>
                    {formatCount(props.group.requestCount)} {t('Requests')}
                  </span>
                </>
              )}
            </div>
          </div>
          {props.reorder ? (
            <div
              className='flex shrink-0 items-center gap-0.5'
              onClick={(e) => e.stopPropagation()}
            >
              <GripVertical
                className='text-muted-foreground/60 size-4'
                aria-hidden='true'
              />
              <button
                type='button'
                aria-label={t('Move up')}
                disabled={!props.reorder.onMoveUp}
                onClick={(e) => {
                  e.stopPropagation()
                  props.reorder?.onMoveUp?.()
                }}
                className='text-muted-foreground/60 hover:text-foreground disabled:text-muted-foreground/30 focus-visible:ring-ring/50 inline-flex size-6 items-center justify-center rounded-md transition-colors focus-visible:ring-2 focus-visible:outline-none'
              >
                <ChevronUp className='size-3.5' aria-hidden='true' />
              </button>
              <button
                type='button'
                aria-label={t('Move down')}
                disabled={!props.reorder.onMoveDown}
                onClick={(e) => {
                  e.stopPropagation()
                  props.reorder?.onMoveDown?.()
                }}
                className='text-muted-foreground/60 hover:text-foreground disabled:text-muted-foreground/30 focus-visible:ring-ring/50 inline-flex size-6 items-center justify-center rounded-md transition-colors focus-visible:ring-2 focus-visible:outline-none'
              >
                <ChevronDown className='size-3.5' aria-hidden='true' />
              </button>
            </div>
          ) : (
            <ChevronRight
              className='text-muted-foreground/60 mt-0.5 size-4 shrink-0'
              aria-hidden='true'
            />
          )}
        </div>
        <StatusSegments group={props.group} fallback={status} />
      </CardHeader>
      <CardContent className='grid grid-cols-2 gap-x-4 gap-y-3 pt-0'>
        <MetricValue
          label={t('Success rate')}
          value={formatUptimePct(props.group.successRate)}
          tone={getSuccessRateTextClass(props.group.successRate)}
        />
        <MetricValue
          label={t('Average latency')}
          value={formatLatency(props.group.avgLatencyMs)}
        />
        <MetricValue
          label={t('Average TTFT')}
          value={formatLatency(props.group.avgTtftMs)}
        />
        <MetricValue
          label={t('Throughput')}
          value={formatThroughput(props.group.avgTps)}
        />
        <MetricValue
          label={t('Hit Rate')}
          value={formatCacheHitRate(
            props.group.cacheHitRate,
            t('Not available')
          )}
        />
        <MetricValue
          label={t('Input Tokens')}
          value={formatCount(
            props.group.inputTokens > 0 ? props.group.inputTokens : undefined
          )}
        />
      </CardContent>
    </Card>
  )

  if (props.reorder) {
    // In reorder mode the card hosts its own buttons, so the wrapper must not
    // be a <button> (nested interactive elements are invalid HTML).
    const reorder = props.reorder
    return (
      <div
        data-testid={`performance-group-${props.group.group}`}
        draggable
        onDragStart={reorder.onDragStart}
        onDragOver={(e) => e.preventDefault()}
        onDrop={(e) => {
          e.preventDefault()
          reorder.onDrop()
        }}
        className={cn(baseClassName, 'cursor-grab')}
      >
        {content}
      </div>
    )
  }

  return (
    <button
      type='button'
      onClick={props.onOpen}
      aria-label={`${t('Open')} ${props.group.group} ${t('Details').toLowerCase()}`}
      data-testid={`performance-group-${props.group.group}`}
      className={baseClassName}
    >
      {content}
    </button>
  )
}

function MetricValue(props: { label: string; value: string; tone?: string }) {
  return (
    <div className='min-w-0'>
      <div className='text-muted-foreground truncate text-[10px] font-medium tracking-wide uppercase'>
        {props.label}
      </div>
      <div
        className={cn(
          'mt-0.5 font-mono text-sm font-semibold tabular-nums',
          props.tone
        )}
      >
        {props.value}
      </div>
    </div>
  )
}

function PerformanceTrend(props: {
  group: AggregatedPerformanceGroup
  periodLabel: string
}) {
  const { t } = useTranslation()
  const points = getRecentStatusSeries(props.group, 24)
  if (points.length === 0) return null

  return (
    <section className='min-w-0'>
      <div className='mb-2 flex items-center gap-2'>
        <HeartPulse
          className='text-muted-foreground size-3.5'
          aria-hidden='true'
        />
        <h3 className='text-sm font-semibold'>{t('Trend')}</h3>
        <span className='text-muted-foreground text-xs'>
          {t('Success rate')} · {props.periodLabel}
        </span>
      </div>
      <div
        className='bg-muted/20 flex h-20 min-w-0 items-end gap-1 rounded-lg border px-3 py-3'
        role='img'
        aria-label={`${t('Success rate')} ${props.periodLabel}`}
      >
        {points.map((point) => {
          const status = getPerformanceStatus(point.successRate)
          const height = `${Math.max(8, Math.min(100, point.successRate))}%`
          return (
            <span
              key={point.ts}
              className={cn(
                'min-w-1 flex-1 rounded-sm transition-opacity hover:opacity-80',
                statusClasses(status).segment
              )}
              style={{ height }}
              title={`${new Date(point.ts * 1000).toLocaleString()} · ${formatUptimePct(point.successRate)}`}
              aria-hidden='true'
            />
          )
        })}
      </div>
      <div className='text-muted-foreground mt-1 flex justify-between text-[10px]'>
        <span>
          {new Date(points[0].ts * 1000).toLocaleTimeString([], {
            hour: '2-digit',
            minute: '2-digit',
          })}
        </span>
        <span>
          {new Date((points.at(-1) ?? points[0]).ts * 1000).toLocaleTimeString(
            [],
            {
              hour: '2-digit',
              minute: '2-digit',
            }
          )}
        </span>
      </div>
    </section>
  )
}

function GroupDetailsSheet(props: {
  selectedGroup: AggregatedPerformanceGroup | null
  details: ModelPerformanceDetail[]
  open: boolean
  hours: WindowHours
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const group = props.selectedGroup
  const rows = useMemo(
    () => (group ? getModelGroupRows(props.details, group.group) : []),
    [group, props.details]
  )
  const status = group
    ? getPerformanceStatus(group.successRate, group.modelNames.length > 0)
    : 'empty'
  const periodLabel = props.hours === 24 ? t('24 Hours') : t('7 Days')

  return (
    <Sheet open={props.open} onOpenChange={props.onOpenChange}>
      <SheetContent className={sideDrawerContentClassName('sm:max-w-6xl')}>
        <SheetHeader className={sideDrawerHeaderClassName()}>
          <div className='flex min-w-0 items-center gap-2 pr-8'>
            <Activity
              className='text-muted-foreground size-4 shrink-0'
              aria-hidden='true'
            />
            <SheetTitle className='truncate font-mono'>
              {group?.group ?? t('Details')}
            </SheetTitle>
            {group && (
              <Badge variant='outline' className='shrink-0'>
                {statusLabel(t, status)}
              </Badge>
            )}
          </div>
          <SheetDescription>
            <span>
              {periodLabel} · {t('Per-group performance')}
            </span>
            {group?.description && (
              <span className='mt-1 block'>{group.description}</span>
            )}
          </SheetDescription>
        </SheetHeader>

        <div className={sideDrawerFormClassName('gap-5')}>
          {group ? (
            <>
              <div className='grid grid-cols-2 gap-2 sm:grid-cols-4'>
                <MetricValue
                  label={t('Requests')}
                  value={formatCount(group.requestCount)}
                />
                <MetricValue
                  label={t('Success rate')}
                  value={formatUptimePct(group.successRate)}
                  tone={getSuccessRateTextClass(group.successRate)}
                />
                <MetricValue
                  label={t('Average latency')}
                  value={formatLatency(group.avgLatencyMs)}
                />
                <MetricValue
                  label={t('Throughput')}
                  value={formatThroughput(group.avgTps)}
                />
                <MetricValue
                  label={t('Average TTFT')}
                  value={formatLatency(group.avgTtftMs)}
                />
                <MetricValue
                  label={t('Hit Rate')}
                  value={formatCacheHitRate(
                    group.cacheHitRate,
                    t('Not available')
                  )}
                />
                <MetricValue
                  label={t('Input Tokens')}
                  value={formatCount(
                    group.inputTokens > 0 ? group.inputTokens : undefined
                  )}
                />
                <MetricValue
                  label={t('Cache Read')}
                  value={formatCount(
                    group.inputTokens > 0 ? group.cacheReadTokens : undefined
                  )}
                />
                <MetricValue
                  label={t('Cache Write')}
                  value={formatCount(
                    group.inputTokens > 0 ? group.cacheWriteTokens : undefined
                  )}
                />
              </div>

              <PerformanceTrend group={group} periodLabel={periodLabel} />

              <section className='min-w-0'>
                <div className='mb-2 flex items-center gap-2'>
                  <Gauge
                    className='text-muted-foreground size-3.5'
                    aria-hidden='true'
                  />
                  <h3 className='text-sm font-semibold'>{t('Models')}</h3>
                  <span className='text-muted-foreground text-xs'>
                    {t('Per-group performance')}
                  </span>
                </div>
                {rows.length === 0 ? (
                  <div className='text-muted-foreground rounded-lg border p-6 text-center text-sm'>
                    {t('No performance data available')}
                  </div>
                ) : (
                  <div className='min-w-0 overflow-x-auto rounded-lg border'>
                    <StaticDataTable
                      className='min-w-[1040px] rounded-none border-0'
                      tableClassName='text-sm'
                      headerRowClassName={
                        staticDataTableClassNames.compactHeaderRow
                      }
                      data={rows}
                      getRowKey={(row) => row.summary.model_name}
                      columns={[
                        {
                          id: 'model',
                          header: t('Models'),
                          className:
                            staticDataTableClassNames.compactHeaderCell,
                          cellClassName: staticDataTableClassNames.compactCell,
                          cell: (row) => (
                            <span className='font-mono text-xs'>
                              {row.summary.model_name}
                            </span>
                          ),
                        },
                        {
                          id: 'requests',
                          header: t('Requests'),
                          className:
                            staticDataTableClassNames.compactHeaderCellRight,
                          cellClassName:
                            staticDataTableClassNames.compactNumericCell,
                          cell: (row) =>
                            formatCount(
                              row.group.request_count ??
                                row.summary.request_count
                            ),
                        },
                        {
                          id: 'success',
                          header: t('Success rate'),
                          className:
                            staticDataTableClassNames.compactHeaderCellRight,
                          cellClassName:
                            staticDataTableClassNames.compactNumericCell,
                          cell: (row) => (
                            <span
                              className={getSuccessRateTextClass(
                                row.group.success_rate
                              )}
                            >
                              {formatUptimePct(row.group.success_rate)}
                            </span>
                          ),
                        },
                        {
                          id: 'cache',
                          header: t('Hit Rate'),
                          className:
                            staticDataTableClassNames.compactHeaderCellRight,
                          cellClassName:
                            staticDataTableClassNames.compactMutedNumericCell,
                          cell: (row) =>
                            formatCacheHitRate(
                              getCacheHitRate(row.group),
                              t('Not available')
                            ),
                        },
                        {
                          id: 'input-tokens',
                          header: t('Input Tokens'),
                          className:
                            staticDataTableClassNames.compactHeaderCellRight,
                          cellClassName:
                            staticDataTableClassNames.compactMutedNumericCell,
                          cell: (row) => formatCount(row.group.input_tokens),
                        },
                        {
                          id: 'cache-read',
                          header: t('Cache Read'),
                          className:
                            staticDataTableClassNames.compactHeaderCellRight,
                          cellClassName:
                            staticDataTableClassNames.compactMutedNumericCell,
                          cell: (row) =>
                            formatCount(row.group.cache_read_tokens),
                        },
                        {
                          id: 'cache-write',
                          header: t('Cache Write'),
                          className:
                            staticDataTableClassNames.compactHeaderCellRight,
                          cellClassName:
                            staticDataTableClassNames.compactMutedNumericCell,
                          cell: (row) =>
                            formatCount(row.group.cache_write_tokens),
                        },
                        {
                          id: 'ttft',
                          header: t('Average TTFT'),
                          className:
                            staticDataTableClassNames.compactHeaderCellRight,
                          cellClassName:
                            staticDataTableClassNames.compactNumericCell,
                          cell: (row) => formatLatency(row.group.avg_ttft_ms),
                        },
                        {
                          id: 'latency',
                          header: t('Average latency'),
                          className:
                            staticDataTableClassNames.compactHeaderCellRight,
                          cellClassName:
                            staticDataTableClassNames.compactMutedNumericCell,
                          cell: (row) =>
                            formatLatency(row.group.avg_latency_ms),
                        },
                        {
                          id: 'tps',
                          header: 'TPS',
                          className:
                            staticDataTableClassNames.compactHeaderCellRight,
                          cellClassName:
                            staticDataTableClassNames.compactNumericCell,
                          cell: (row) => formatThroughput(row.group.avg_tps),
                        },
                      ]}
                    />
                  </div>
                )}
              </section>
            </>
          ) : (
            <div className='text-muted-foreground rounded-lg border p-6 text-center text-sm'>
              {t('No performance data available')}
            </div>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}

export function PerformancePage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [hours, setHours] = useState<WindowHours>(24)
  const [autoRefresh, setAutoRefresh] = useState(true)
  const [statusFilter, setStatusFilter] = useState<StatusFilter>('all')
  const [selectedGroupName, setSelectedGroupName] = useState<string | null>(
    null
  )
  const [reorderMode, setReorderMode] = useState(false)
  const [draftOrder, setDraftOrder] = useState<string[] | null>(null)
  const draggingGroup = useRef<string | null>(null)

  const summaryQuery = useQuery({
    queryKey: ['perf-metrics-summary', hours],
    queryFn: () => getPerfMetricsSummary(hours),
    staleTime: 30_000,
    retry: false,
    refetchInterval: autoRefresh ? REFRESH_INTERVAL_MS : false,
  })
  const userGroupsQuery = useQuery({
    queryKey: ['user-groups'],
    queryFn: getUserGroups,
    staleTime: 5 * 60_000,
    retry: false,
    refetchInterval: autoRefresh ? REFRESH_INTERVAL_MS : false,
  })
  const models = useMemo<PerfModelSummary[]>(
    () => summaryQuery.data?.data?.models ?? [],
    [summaryQuery.data]
  )
  const detailQueries = useQueries({
    queries: models.map((model) => ({
      queryKey: ['perf-metrics', model.model_name, hours],
      queryFn: () => getPerfMetrics(model.model_name, hours),
      staleTime: 30_000,
      retry: false,
      refetchInterval: autoRefresh ? REFRESH_INTERVAL_MS : false,
    })),
  })
  const details = useMemo<ModelPerformanceDetail[]>(
    () =>
      models.flatMap((summary, index) => {
        const data = detailQueries[index]?.data?.data
        return data ? [{ summary, groups: data.groups ?? [] }] : []
      }),
    [detailQueries, models]
  )
  const groupConfigs = useMemo<PerformanceGroupConfig[]>(
    () =>
      Object.entries(userGroupsQuery.data?.data ?? {}).map(([group, info]) => ({
        group,
        description: info.desc,
        ratio: info.ratio,
      })),
    [userGroupsQuery.data]
  )
  const observedGroups = useMemo(
    () => aggregatePerformanceGroups(details),
    [details]
  )
  const groups = useMemo(() => {
    const scopedGroups = userGroupsQuery.isSuccess
      ? observedGroups.filter((group) =>
          groupConfigs.some((config) => config.group === group.group)
        )
      : observedGroups
    return mergeConfiguredPerformanceGroups(scopedGroups, groupConfigs)
  }, [groupConfigs, observedGroups, userGroupsQuery.isSuccess])
  const selectedGroup = useMemo(
    () => groups.find((group) => group.group === selectedGroupName) ?? null,
    [groups, selectedGroupName]
  )
  const detailLoading = detailQueries.some((query) => query.isLoading)
  const groupLoading = detailLoading || userGroupsQuery.isLoading
  const isRefreshing =
    summaryQuery.isRefetching ||
    userGroupsQuery.isRefetching ||
    detailQueries.some((query) => query.isRefetching)
  const hasSummaryData = summaryQuery.data?.success !== false
  const allDetailsSettled = detailQueries.every(
    (query) => query.isFetched || query.isError
  )
  const totalLatencyMs = groups.reduce((s, g) => s + g.totalLatencyMs, 0)
  const totalRequests = groups.reduce((s, g) => s + g.requestCount, 0)
  const avgLatency =
    totalLatencyMs > 0 && totalRequests > 0
      ? totalLatencyMs / totalRequests
      : weightedAverage(groups, (group) => group.avgLatencyMs)
  const successRate = getOverallSuccessRate(groups)
  const cacheHitRate = getWorkspaceCacheHitRate(groups)
  const healthyCount = groups.filter(
    (group) =>
      getPerformanceStatus(group.successRate, group.modelNames.length > 0) ===
      'healthy'
  ).length
  const statusCounts = useMemo(() => {
    const counts: Record<StatusFilter, number> = {
      all: groups.length,
      healthy: 0,
      degraded: 0,
      critical: 0,
      empty: 0,
    }
    for (const group of groups) {
      const status = getPerformanceStatus(
        group.successRate,
        group.modelNames.length > 0
      )
      counts[status] += 1
    }
    return counts
  }, [groups])
  const savedOrder = useMemo(
    () => summaryQuery.data?.data?.group_order ?? [],
    [summaryQuery.data]
  )
  const userRole = useAuthStore((s) => s.auth.user?.role) ?? 0
  const canReorder = userRole >= 100
  const orderedGroups = useMemo(
    () => orderPerformanceGroups(groups, draftOrder ?? savedOrder),
    [groups, draftOrder, savedOrder]
  )
  const visibleGroups = useMemo(
    () =>
      statusFilter === 'all'
        ? orderedGroups
        : orderedGroups.filter(
            (group) =>
              getPerformanceStatus(
                group.successRate,
                group.modelNames.length > 0
              ) === statusFilter
          ),
    [orderedGroups, statusFilter]
  )

  function moveGroup(name: string, direction: -1 | 1) {
    const current = orderedGroups.map((group) => group.group)
    const index = current.indexOf(name)
    if (index < 0) return
    const target = index + direction
    if (target < 0 || target >= current.length) return
    const next = [...current]
    ;[next[index], next[target]] = [next[target], next[index]]
    setDraftOrder(next)
  }

  function dropGroup(from: string, to: string) {
    if (from === to) return
    const current = orderedGroups.map((group) => group.group)
    const next = current.filter((name) => name !== from)
    const toIndex = next.indexOf(to)
    if (toIndex < 0) {
      next.push(from)
    } else {
      next.splice(toIndex, 0, from)
    }
    setDraftOrder(next)
  }

  function cancelReorder() {
    setDraftOrder(null)
    setReorderMode(false)
  }

  const saveOrder = useMutation({
    mutationFn: (order: string[]) => updatePerfGroupOrder(order),
    onSuccess: (data) => {
      if (!data.success) {
        toast.error(data.message || t('Failed to save card order'))
        return
      }
      toast.success(t('Card order saved'))
      setDraftOrder(null)
      setReorderMode(false)
      void queryClient.invalidateQueries({ queryKey: ['perf-metrics-summary'] })
    },
    onError: () => toast.error(t('Failed to save card order')),
  })

  const resetOrder = () => saveOrder.mutate([])

  function refresh() {
    void summaryQuery.refetch()
    void userGroupsQuery.refetch()
    for (const query of detailQueries) void query.refetch()
  }

  function selectWindow(nextHours: WindowHours) {
    setHours(nextHours)
    setSelectedGroupName(null)
  }

  let pageContent: React.ReactNode
  if (summaryQuery.isError) {
    pageContent = (
      <div className='border-destructive/30 bg-destructive/5 text-destructive flex flex-col items-center gap-3 rounded-lg border p-8 text-center'>
        <AlertTriangle className='size-5' aria-hidden='true' />
        <p className='text-sm font-medium'>{t('Failed to load')}</p>
        <Button type='button' variant='outline' size='sm' onClick={refresh}>
          <RefreshCw data-icon='inline-start' />
          {t('Retry')}
        </Button>
      </div>
    )
  } else if (summaryQuery.isLoading) {
    pageContent = <PerformanceLoadingState />
  } else if (!hasSummaryData) {
    pageContent = (
      <div className='text-muted-foreground flex flex-col items-center gap-2 rounded-lg border border-dashed p-10 text-center'>
        <CircleDashed className='size-5' aria-hidden='true' />
        <p className='text-sm'>{t('No performance data available')}</p>
      </div>
    )
  } else {
    let groupsContent: React.ReactNode
    if (groups.length === 0 && (!allDetailsSettled || groupLoading)) {
      groupsContent = <PerformanceLoadingState compact />
    } else if (groups.length === 0) {
      groupsContent = (
        <div className='text-muted-foreground rounded-lg border border-dashed p-8 text-center text-sm'>
          {t('No performance data available')}
        </div>
      )
    } else if (visibleGroups.length === 0) {
      groupsContent = (
        <div className='text-muted-foreground rounded-lg border border-dashed p-8 text-center text-sm'>
          {t('No data available')}
        </div>
      )
    } else {
      groupsContent = (
        <>
          {reorderMode && (
            <p className='text-muted-foreground text-xs'>
              {t('Drag cards or use the arrows to reorder')}
            </p>
          )}
          <div className='grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3'>
            {visibleGroups.map((group) => (
              <PerformanceGroupCard
                key={group.group}
                group={group}
                onOpen={() => setSelectedGroupName(group.group)}
                reorder={
                  reorderMode
                    ? {
                        onMoveUp:
                          orderedGroups[0]?.group !== group.group
                            ? () => moveGroup(group.group, -1)
                            : undefined,
                        onMoveDown:
                          orderedGroups.at(-1)?.group !== group.group
                            ? () => moveGroup(group.group, 1)
                            : undefined,
                        onDragStart: () => {
                          draggingGroup.current = group.group
                        },
                        onDrop: () => {
                          const from = draggingGroup.current
                          draggingGroup.current = null
                          if (from && from !== group.group) {
                            dropGroup(from, group.group)
                          }
                        },
                      }
                    : undefined
                }
              />
            ))}
          </div>
        </>
      )
    }

    pageContent = (
      <>
        <div className='grid grid-cols-2 gap-2 sm:grid-cols-4 xl:grid-cols-6'>
          <SummaryMetric
            icon={Activity}
            label={t('Models')}
            value={models.length}
          />
          <SummaryMetric
            icon={Gauge}
            label={t('Groups')}
            value={groups.length || (groupLoading ? '…' : 0)}
          />
          <SummaryMetric
            icon={HeartPulse}
            label={t('Healthy')}
            value={groups.length ? `${healthyCount}/${groups.length}` : '—'}
            tone={
              healthyCount > 0
                ? 'text-emerald-600 dark:text-emerald-400'
                : undefined
            }
          />
          <SummaryMetric
            icon={Timer}
            label={t('Average latency')}
            value={formatLatency(avgLatency)}
          />
          <SummaryMetric
            icon={HeartPulse}
            label={t('Success rate')}
            value={formatUptimePct(successRate)}
            tone={getSuccessRateTextClass(successRate)}
          />
          <SummaryMetric
            icon={Database}
            label={t('Hit Rate')}
            value={formatCacheHitRate(cacheHitRate, t('Not available'))}
          />
        </div>

        <div
          className='flex min-w-0 gap-1 overflow-x-auto border-b pb-1'
          role='group'
          aria-label={t('All statuses')}
        >
          {STATUS_FILTERS.map((filter) => {
            const count = statusCounts[filter.value]
            const selected = statusFilter === filter.value
            return (
              <button
                key={filter.value}
                type='button'
                onClick={() => setStatusFilter(filter.value)}
                aria-pressed={selected}
                className={cn(
                  'inline-flex min-h-8 shrink-0 items-center gap-1.5 rounded-md px-2.5 text-xs font-medium transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:outline-none',
                  selected
                    ? 'bg-foreground text-background'
                    : 'text-muted-foreground hover:bg-muted hover:text-foreground'
                )}
              >
                <span>{t(filter.label)}</span>
                <span className='font-mono text-[10px] tabular-nums opacity-70'>
                  {count}
                </span>
              </button>
            )
          })}
        </div>

        {groupsContent}

        <div className='text-muted-foreground flex flex-wrap items-center justify-between gap-2 text-xs'>
          <span>
            {t('Last updated:')}{' '}
            {summaryQuery.dataUpdatedAt
              ? new Date(summaryQuery.dataUpdatedAt).toLocaleTimeString()
              : '—'}
          </span>
          <span>
            {groupLoading ? t('Loading...') : `${groups.length} ${t('Groups')}`}
          </span>
        </div>
      </>
    )
  }

  return (
    <>
      <SectionPageLayout variant='editorial' density='compact'>
        <SectionPageLayout.Title>{t('Performance')}</SectionPageLayout.Title>
        <SectionPageLayout.Description>
          {t('Track usage, costs and performance with real-time analytics')}
        </SectionPageLayout.Description>
        <SectionPageLayout.Actions>
          <div className='flex flex-wrap items-center justify-end gap-2'>
            <div
              className='bg-muted/60 inline-flex h-8 rounded-lg border p-0.5'
              role='group'
              aria-label={t('Period')}
            >
              {WINDOW_OPTIONS.map((option) => (
                <button
                  key={option.hours}
                  type='button'
                  onClick={() => selectWindow(option.hours)}
                  aria-pressed={hours === option.hours}
                  className={cn(
                    'inline-flex h-full items-center rounded-md px-2.5 text-xs font-medium transition-colors focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:outline-none',
                    hours === option.hours
                      ? 'bg-background text-foreground shadow-sm'
                      : 'text-muted-foreground hover:text-foreground'
                  )}
                >
                  {t(option.label)}
                </button>
              ))}
            </div>
            <label className='text-muted-foreground inline-flex h-8 items-center gap-2 rounded-lg border px-2.5 text-xs'>
              <Switch
                size='sm'
                checked={autoRefresh}
                onCheckedChange={(checked) => setAutoRefresh(Boolean(checked))}
                aria-label={t('Auto refresh')}
              />
              <span>{t('Auto refresh')}</span>
            </label>
            <Button
              type='button'
              variant='outline'
              size='icon-sm'
              onClick={refresh}
              disabled={isRefreshing}
              aria-label={t('Refresh')}
              title={t('Refresh')}
            >
              <RefreshCw
                className={cn(isRefreshing && 'animate-spin')}
                aria-hidden='true'
              />
            </Button>
            {canReorder && !reorderMode && (
              <Button
                type='button'
                variant='outline'
                size='sm'
                onClick={() => setReorderMode(true)}
              >
                <ArrowUpDown aria-hidden='true' />
                {t('Arrange cards')}
              </Button>
            )}
            {canReorder && reorderMode && (
              <>
                <Button
                  type='button'
                  size='sm'
                  onClick={() =>
                    saveOrder.mutate(orderedGroups.map((g) => g.group))
                  }
                  disabled={saveOrder.isPending || draftOrder === null}
                >
                  {t('Save order')}
                </Button>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  onClick={resetOrder}
                  disabled={saveOrder.isPending}
                >
                  {t('Reset to default order')}
                </Button>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  onClick={cancelReorder}
                  disabled={saveOrder.isPending}
                >
                  {t('Cancel')}
                </Button>
              </>
            )}
          </div>
        </SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          <div className='mx-auto flex w-full max-w-7xl flex-col gap-4'>
            {pageContent}
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>

      <GroupDetailsSheet
        selectedGroup={selectedGroup}
        details={details}
        open={selectedGroupName !== null}
        hours={hours}
        onOpenChange={(open) => {
          if (!open) setSelectedGroupName(null)
        }}
      />
    </>
  )
}

function PerformanceLoadingState(props: { compact?: boolean }) {
  return (
    <div className='grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3'>
      {LOADING_CARD_KEYS.slice(0, props.compact ? 3 : 6).map((key) => (
        <Card key={key} className='rounded-lg shadow-none'>
          <CardHeader>
            <Skeleton className='h-4 w-32' />
            <Skeleton className='h-2 w-full' />
          </CardHeader>
          <CardContent className='grid grid-cols-2 gap-3'>
            <Skeleton className='h-8 w-full' />
            <Skeleton className='h-8 w-full' />
            <Skeleton className='h-8 w-full' />
            <Skeleton className='h-8 w-full' />
          </CardContent>
        </Card>
      ))}
    </div>
  )
}

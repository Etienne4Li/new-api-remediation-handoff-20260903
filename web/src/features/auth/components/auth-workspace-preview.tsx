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
  Activity,
  BarChart3,
  Check,
  CircleDot,
  KeyRound,
  LayoutDashboard,
  Route,
  WalletCards,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

interface AuthWorkspacePreviewProps {
  statusLoading: boolean
  statusReady: boolean
  systemName: string
  version: string | null
}

function getStatusLabel(
  t: (key: string) => string,
  statusLoading: boolean,
  statusReady: boolean
) {
  if (statusLoading) return t('Loading...')
  if (statusReady) return t('Online')
  return t('Ready')
}

function getVersionLabel(version: string) {
  return version.startsWith('v') ? version : `v${version}`
}

const PREVIEW_ROUTES = [
  { name: 'OpenAI', endpoint: '/v1/chat/completions', tone: 'bg-emerald-400' },
  { name: 'Responses', endpoint: '/v1/responses', tone: 'bg-amber-400' },
  { name: 'Claude', endpoint: '/v1/messages', tone: 'bg-blue-400' },
] as const

export function AuthWorkspacePreview(props: AuthWorkspacePreviewProps) {
  const { t } = useTranslation()
  const statusLabel = getStatusLabel(t, props.statusLoading, props.statusReady)

  return (
    <figure
      aria-label={t('Route, auth, and balance check in one place')}
      className='overflow-hidden rounded-lg border border-white/15 bg-[#0d1110] shadow-2xl shadow-black/25'
    >
      <div className='flex h-10 min-w-0 items-center gap-2 border-b border-white/10 px-3'>
        <CircleDot className='size-3.5 shrink-0 text-emerald-400' />
        <span className='min-w-0 truncate text-xs font-semibold text-white/90'>
          {props.systemName}
        </span>
        {props.version ? (
          <code className='ml-auto shrink-0 text-[9px] text-white/35'>
            {getVersionLabel(props.version)}
          </code>
        ) : null}
      </div>

      <div className='grid h-[286px] grid-cols-[46px_minmax(0,1fr)] xl:h-[310px]'>
        <div className='flex flex-col items-center gap-2 border-r border-white/10 py-3'>
          {[
            { label: t('Dashboard'), icon: LayoutDashboard, active: true },
            { label: t('Token Management'), icon: KeyRound },
            { label: t('Usage'), icon: BarChart3 },
            { label: t('Balance'), icon: WalletCards },
          ].map((item) => (
            <span
              key={item.label}
              title={item.label}
              className={`flex size-8 items-center justify-center rounded-md ${
                item.active ? 'bg-white text-[#111614]' : 'text-white/40'
              }`}
            >
              <item.icon aria-hidden='true' className='size-3.5' />
              <span className='sr-only'>{item.label}</span>
            </span>
          ))}
        </div>

        <div className='min-w-0 overflow-hidden'>
          <div className='flex h-12 min-w-0 items-center gap-2 border-b border-white/10 px-4'>
            <div className='min-w-0'>
              <div className='truncate text-xs font-semibold text-white/90'>
                {t('Routing & Overrides')}
              </div>
              <div className='text-[9px] text-white/40'>
                {t('Configured routes and latency checks')}
              </div>
            </div>
            <span className='ml-auto flex shrink-0 items-center gap-1.5 rounded-md border border-white/10 bg-white/[0.04] px-2 py-1 text-[9px] font-medium text-white/70'>
              <span
                className={`size-1.5 rounded-full ${
                  props.statusLoading ? 'bg-amber-400' : 'bg-emerald-400'
                }`}
              />
              {statusLabel}
            </span>
          </div>

          <div className='grid grid-cols-3 border-b border-white/10'>
            {[
              { label: t('Routes'), value: '4', icon: Route },
              { label: t('Streaming'), value: 'SSE', icon: Activity },
              { label: t('Authentication'), value: t('Ready'), icon: KeyRound },
            ].map((item) => (
              <div
                key={item.label}
                className='min-w-0 border-r border-white/10 px-3 py-2.5 last:border-r-0'
              >
                <item.icon
                  aria-hidden='true'
                  className='mb-1 size-3 text-white/35'
                />
                <div className='truncate text-xs font-semibold text-white/90'>
                  {item.value}
                </div>
                <div className='truncate text-[9px] text-white/35'>
                  {item.label}
                </div>
              </div>
            ))}
          </div>

          <div className='px-4 py-2.5'>
            <div className='mb-1.5 grid grid-cols-[1fr_auto] text-[9px] font-semibold text-white/35 uppercase'>
              <span>{t('Route')}</span>
              <span>{t('Status')}</span>
            </div>
            {PREVIEW_ROUTES.map((route) => (
              <div
                key={route.name}
                className='grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 border-t border-white/[0.07] py-2'
              >
                <div className='flex min-w-0 items-center gap-2'>
                  <span
                    className={`size-1.5 shrink-0 rounded-full ${route.tone}`}
                  />
                  <div className='min-w-0'>
                    <div className='truncate text-[10px] font-medium text-white/85'>
                      {route.name}
                    </div>
                    <code className='block truncate text-[8px] text-white/30'>
                      {route.endpoint}
                    </code>
                  </div>
                </div>
                <span className='flex items-center gap-1 text-[9px] text-emerald-300'>
                  <Check aria-hidden='true' className='size-3' />
                  {t('Available')}
                </span>
              </div>
            ))}
          </div>
        </div>
      </div>

      <div className='flex h-9 min-w-0 items-center gap-2 border-t border-white/10 bg-black/30 px-3 font-mono text-[9px]'>
        <span className='shrink-0 text-emerald-300'>POST</span>
        <span className='min-w-0 truncate text-white/45'>/v1/responses</span>
        <span className='ml-auto flex shrink-0 items-center gap-1 text-emerald-300'>
          <Check aria-hidden='true' className='size-3' />
          HTTP 200
        </span>
      </div>
    </figure>
  )
}

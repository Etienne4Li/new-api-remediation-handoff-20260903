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
import { Braces, MessageSquareText, Sparkles, Workflow } from 'lucide-react'
import { useTranslation } from 'react-i18next'

const PROTOCOLS = [
  {
    name: 'OpenAI',
    endpoint: '/v1/chat/completions',
    icon: MessageSquareText,
  },
  {
    name: 'Responses',
    endpoint: '/v1/responses',
    icon: Workflow,
  },
  {
    name: 'Claude',
    endpoint: '/v1/messages',
    icon: Braces,
  },
  {
    name: 'Gemini',
    endpoint: '/v1beta/models',
    icon: Sparkles,
  },
] as const

export function Protocols() {
  const { t } = useTranslation()

  return (
    <section
      aria-label={t('compatible API routes')}
      className='border-border bg-background relative z-10 border-y px-4 sm:px-6 lg:px-8'
    >
      <div className='border-border mx-auto grid max-w-[80rem] grid-cols-2 border-x md:grid-cols-4'>
        {PROTOCOLS.map((protocol, index) => (
          <div
            key={protocol.name}
            className={`flex min-w-0 items-center gap-3 p-4 sm:p-5 ${
              index % 2 === 1 ? 'border-border border-l' : ''
            } ${index > 1 ? 'border-border border-t md:border-t-0' : ''} ${
              index > 0 ? 'md:border-border md:border-l' : ''
            }`}
          >
            <span className='bg-muted flex size-8 shrink-0 items-center justify-center'>
              <protocol.icon aria-hidden='true' className='size-4' />
            </span>
            <div className='min-w-0'>
              <div className='truncate text-sm font-semibold'>
                {protocol.name}
              </div>
              <span className='text-muted-foreground block truncate text-[10px]'>
                {t('compatible API routes')}
              </span>
              <code className='text-muted-foreground block truncate text-[11px]'>
                {protocol.endpoint}
              </code>
            </div>
          </div>
        ))}
      </div>
    </section>
  )
}

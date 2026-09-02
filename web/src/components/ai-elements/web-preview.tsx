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
'use client'

import {
  type ComponentProps,
  createContext,
  type ReactNode,
  useContext,
  useState,
} from 'react'
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

type WebPreviewContextValue = {
  url: string
}

const WebPreviewContext = createContext<WebPreviewContextValue | null>(null)

const useWebPreview = () => {
  const context = useContext(WebPreviewContext)
  if (!context) {
    throw new Error('WebPreview components must be used within a WebPreview')
  }
  return context
}

export type WebPreviewProps = ComponentProps<'div'> & {
  defaultUrl?: string
}

export const WebPreview = ({
  className,
  children,
  defaultUrl = '',
  ...props
}: WebPreviewProps) => {
  const [url] = useState(defaultUrl)

  const contextValue: WebPreviewContextValue = {
    url,
  }

  return (
    <WebPreviewContext.Provider value={contextValue}>
      <div
        className={cn(
          'bg-card flex size-full flex-col rounded-lg border',
          className
        )}
        {...props}
      >
        {children}
      </div>
    </WebPreviewContext.Provider>
  )
}

export type WebPreviewBodyProps = ComponentProps<'iframe'> & {
  loading?: ReactNode
}

export const WebPreviewBody = ({
  className,
  loading,
  src,
  ...props
}: WebPreviewBodyProps) => {
  const { t } = useTranslation()
  const { url } = useWebPreview()

  return (
    <div className='flex-1'>
      <iframe
        {...props}
        className={cn('size-full', className)}
        referrerPolicy='no-referrer'
        sandbox='allow-scripts allow-forms allow-popups allow-presentation'
        src={(src ?? url) || undefined}
        title={t('Preview')}
      />
      {loading}
    </div>
  )
}

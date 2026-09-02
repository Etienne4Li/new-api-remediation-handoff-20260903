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
import { useEffect, useLayoutEffect, useRef, useState } from 'react'

import { cn } from '@/lib/utils'

type TurnstileWidgetSize = 'compact' | 'flexible'

const TURNSTILE_FLEXIBLE_MIN_WIDTH = 300

declare global {
  interface Window {
    turnstile?: {
      render: (element: HTMLElement, options: Record<string, unknown>) => string
      remove: (widgetId: string) => void
    }
  }
}

interface TurnstileProps {
  siteKey: string
  onVerify: (token: string) => void
  onExpire?: () => void
  className?: string
}

export function Turnstile({
  siteKey,
  onVerify,
  onExpire,
  className,
}: TurnstileProps) {
  const ref = useRef<HTMLDivElement | null>(null)
  const widgetSizeRef = useRef<TurnstileWidgetSize | null>(null)
  const onVerifyRef = useRef(onVerify)
  const onExpireRef = useRef(onExpire)
  const [widgetSize, setWidgetSize] = useState<TurnstileWidgetSize | null>(null)

  useEffect(() => {
    onVerifyRef.current = onVerify
    onExpireRef.current = onExpire
  }, [onExpire, onVerify])

  useLayoutEffect(() => {
    const element = ref.current
    if (!element) return

    const updateWidgetSize = (width: number) => {
      if (width <= 0) return

      const nextSize =
        width < TURNSTILE_FLEXIBLE_MIN_WIDTH ? 'compact' : 'flexible'
      if (widgetSizeRef.current === nextSize) return

      if (widgetSizeRef.current) onExpireRef.current?.()
      widgetSizeRef.current = nextSize
      setWidgetSize(nextSize)
    }

    updateWidgetSize(element.getBoundingClientRect().width)

    const observer = new ResizeObserver((entries) => {
      const width = entries[0]?.contentRect.width
      if (width !== undefined) updateWidgetSize(width)
    })
    observer.observe(element)

    return () => observer.disconnect()
  }, [])

  useEffect(() => {
    if (!widgetSize) return

    let widgetId: string | null = null
    let cancelled = false

    const render = () => {
      if (cancelled || !ref.current || !window.turnstile) return
      try {
        widgetId = window.turnstile.render(ref.current, {
          sitekey: siteKey,
          size: widgetSize,
          callback: (token: string) => onVerifyRef.current(token),
          'error-callback': () => onExpireRef.current?.(),
          'expired-callback': () => onExpireRef.current?.(),
        })
      } catch {
        /* empty */
      }
    }

    if (window.turnstile) {
      render()
      return () => {
        cancelled = true
        if (widgetId) window.turnstile?.remove(widgetId)
      }
    }

    const scriptId = 'cf-turnstile'
    let script = document.querySelector<HTMLScriptElement>(`script#${scriptId}`)
    if (!script) {
      script = document.createElement('script')
      script.id = scriptId
      script.src =
        'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit'
      script.async = true
      script.defer = true
      document.head.appendChild(script)
    }
    script.addEventListener('load', render, { once: true })

    return () => {
      cancelled = true
      script.removeEventListener('load', render)
      if (widgetId) window.turnstile?.remove(widgetId)
    }
  }, [siteKey, widgetSize])

  return (
    <div
      ref={ref}
      className={cn('flex w-full min-w-0 max-w-full justify-center', className)}
    />
  )
}

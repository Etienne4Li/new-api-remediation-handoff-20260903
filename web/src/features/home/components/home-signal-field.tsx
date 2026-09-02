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
import { useEffect, useRef } from 'react'

import { useTheme } from '@/context/theme-provider'
import { cn } from '@/lib/utils'

interface HomeSignalFieldProps {
  className?: string
  dark?: boolean
  paused?: boolean
}

interface SignalPoint {
  x: number
  y: number
}

interface SignalRoute {
  controlA: SignalPoint
  controlB: SignalPoint
  duration: number
  end: SignalPoint
  offset: number
  start: SignalPoint
  tone: number
}

interface SignalNode extends SignalPoint {
  label: string
  tone: number
}

const ROUTER_POSITION = { x: 0.57, y: 0.34 }

const SIGNAL_ROUTES: SignalRoute[] = [
  {
    start: { x: 0.02, y: 0.16 },
    controlA: { x: 0.23, y: 0.13 },
    controlB: { x: 0.39, y: 0.24 },
    end: ROUTER_POSITION,
    tone: 0,
    offset: 0.12,
    duration: 5200,
  },
  {
    start: { x: 0.04, y: 0.38 },
    controlA: { x: 0.25, y: 0.4 },
    controlB: { x: 0.42, y: 0.34 },
    end: ROUTER_POSITION,
    tone: 1,
    offset: 0.54,
    duration: 6300,
  },
  {
    start: { x: 0.1, y: 0.63 },
    controlA: { x: 0.28, y: 0.6 },
    controlB: { x: 0.43, y: 0.44 },
    end: ROUTER_POSITION,
    tone: 2,
    offset: 0.82,
    duration: 7100,
  },
  {
    start: ROUTER_POSITION,
    controlA: { x: 0.7, y: 0.27 },
    controlB: { x: 0.82, y: 0.14 },
    end: { x: 0.97, y: 0.13 },
    tone: 2,
    offset: 0.31,
    duration: 5900,
  },
  {
    start: ROUTER_POSITION,
    controlA: { x: 0.7, y: 0.36 },
    controlB: { x: 0.84, y: 0.41 },
    end: { x: 0.96, y: 0.4 },
    tone: 0,
    offset: 0.67,
    duration: 6800,
  },
  {
    start: ROUTER_POSITION,
    controlA: { x: 0.68, y: 0.43 },
    controlB: { x: 0.79, y: 0.62 },
    end: { x: 0.91, y: 0.67 },
    tone: 1,
    offset: 0.93,
    duration: 7600,
  },
]

const SIGNAL_NODES: SignalNode[] = [
  { x: 0.08, y: 0.16, label: 'REQUEST', tone: 0 },
  { x: 0.18, y: 0.39, label: 'POLICY', tone: 1 },
  { x: 0.21, y: 0.59, label: 'TOKENS', tone: 2 },
  { x: 0.8, y: 0.17, label: 'MODEL A', tone: 2 },
  { x: 0.84, y: 0.4, label: 'MODEL B', tone: 0 },
  { x: 0.78, y: 0.6, label: 'STREAM', tone: 1 },
]

function pointOnRoute(route: SignalRoute, progress: number) {
  const inverse = 1 - progress
  return {
    x:
      inverse ** 3 * route.start.x +
      3 * inverse ** 2 * progress * route.controlA.x +
      3 * inverse * progress ** 2 * route.controlB.x +
      progress ** 3 * route.end.x,
    y:
      inverse ** 3 * route.start.y +
      3 * inverse ** 2 * progress * route.controlA.y +
      3 * inverse * progress ** 2 * route.controlB.y +
      progress ** 3 * route.end.y,
  }
}

/** Draw a sparse, animated routing topology behind the public home page. */
export function HomeSignalField(props: HomeSignalFieldProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const { resolvedTheme } = useTheme()
  const { dark, paused } = props

  useEffect(() => {
    const canvas = canvasRef.current
    const context = canvas?.getContext('2d')
    if (!canvas || !context) return

    const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)')
    const isDark = dark ?? resolvedTheme === 'dark'
    const ink = isDark ? '214, 226, 245' : '35, 50, 78'
    const surface = isDark ? '10, 16, 28' : '255, 255, 255'
    const tones = isDark
      ? ['91, 164, 255', '55, 211, 168', '255, 154, 121']
      : ['36, 101, 235', '3, 132, 111', '204, 78, 56']

    let width = 1
    let height = 1
    let frame = 0
    let running = false
    let visible = true
    let startTime = performance.now()
    let parallaxX = 0
    let parallaxY = 0
    let targetX = 0
    let targetY = 0

    const draw = (timestamp: number) => {
      context.clearRect(0, 0, width, height)
      parallaxX += (targetX - parallaxX) * 0.045
      parallaxY += (targetY - parallaxY) * 0.045

      context.save()
      context.translate(parallaxX, parallaxY)
      context.lineCap = 'square'
      context.lineJoin = 'miter'
      const compact = width < 640

      context.strokeStyle = `rgba(${ink}, ${isDark ? 0.07 : 0.055})`
      context.lineWidth = 1
      context.setLineDash([2, 13])
      for (const guideY of [0.16, 0.4, 0.63]) {
        context.beginPath()
        context.moveTo(0, guideY * height)
        context.lineTo(width, guideY * height)
        context.stroke()
      }

      for (const route of SIGNAL_ROUTES) {
        const tone = tones[route.tone]
        context.beginPath()
        context.moveTo(route.start.x * width, route.start.y * height)
        context.bezierCurveTo(
          route.controlA.x * width,
          route.controlA.y * height,
          route.controlB.x * width,
          route.controlB.y * height,
          route.end.x * width,
          route.end.y * height
        )
        context.strokeStyle = `rgba(${tone}, ${isDark ? 0.3 : 0.22})`
        context.lineWidth = route.tone === 0 ? 1.35 : 1
        context.setLineDash(route.tone === 1 ? [7, 12] : [3, 9])
        context.stroke()

        const elapsed = reducedMotion.matches ? 0 : timestamp - startTime
        const progress = (route.offset + elapsed / route.duration) % 1
        for (let trail = 3; trail >= 0; trail -= 1) {
          const trailProgress = (progress - trail * 0.013 + 1) % 1
          const point = pointOnRoute(route, trailProgress)
          const size = trail === 0 ? 5 : 2.5
          context.fillStyle = `rgba(${tone}, ${0.24 + (3 - trail) * 0.2})`
          context.fillRect(
            point.x * width - size / 2,
            point.y * height - size / 2,
            size,
            size
          )
        }
      }

      context.setLineDash([])
      context.textBaseline = 'middle'
      if (!compact) {
        context.font =
          '600 10px ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace'
        for (const node of SIGNAL_NODES) {
          const x = node.x * width
          const y = node.y * height
          const tone = tones[node.tone]
          context.fillStyle = `rgba(${surface}, ${isDark ? 0.82 : 0.74})`
          context.fillRect(x - 5, y - 5, 10, 10)
          context.strokeStyle = `rgba(${tone}, ${isDark ? 0.72 : 0.58})`
          context.lineWidth = 1
          context.strokeRect(x - 5, y - 5, 10, 10)
          context.fillStyle = `rgba(${ink}, ${isDark ? 0.5 : 0.42})`
          context.fillText(node.label, x + 12, y)
        }
      }

      const routerX = ROUTER_POSITION.x * width
      const routerY = ROUTER_POSITION.y * height
      if (compact) {
        context.fillStyle = `rgba(${tones[1]}, 0.78)`
        context.fillRect(routerX - 3, routerY - 3, 6, 6)
        context.restore()
        return
      }

      context.fillStyle = `rgba(${surface}, ${isDark ? 0.9 : 0.84})`
      context.fillRect(routerX - 47, routerY - 16, 94, 32)
      context.strokeStyle = `rgba(${tones[0]}, ${isDark ? 0.78 : 0.64})`
      context.lineWidth = 1.25
      context.strokeRect(routerX - 47, routerY - 16, 94, 32)
      context.fillStyle = `rgba(${tones[1]}, 0.92)`
      context.fillRect(routerX - 37, routerY - 3, 6, 6)
      context.fillStyle = `rgba(${ink}, ${isDark ? 0.74 : 0.66})`
      context.font =
        '700 10px ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace'
      context.fillText('ROUTER / 01', routerX - 23, routerY)

      context.restore()
    }

    const stop = () => {
      running = false
      window.cancelAnimationFrame(frame)
    }

    const tick = (timestamp: number) => {
      if (!running) return
      draw(timestamp)
      frame = window.requestAnimationFrame(tick)
    }

    const start = () => {
      if (running || paused || !visible || reducedMotion.matches) return
      running = true
      startTime = performance.now()
      frame = window.requestAnimationFrame(tick)
    }

    const resize = () => {
      const rect = canvas.getBoundingClientRect()
      width = Math.max(1, rect.width || canvas.clientWidth)
      height = Math.max(1, rect.height || canvas.clientHeight)
      const pixelRatio = Math.min(window.devicePixelRatio || 1, 2)
      canvas.width = Math.round(width * pixelRatio)
      canvas.height = Math.round(height * pixelRatio)
      context.setTransform(pixelRatio, 0, 0, pixelRatio, 0, 0)
      draw(performance.now())
      start()
    }

    const handlePointerMove = (event: PointerEvent) => {
      if (reducedMotion.matches) return
      const rect = canvas.getBoundingClientRect()
      if (!rect.width || !rect.height) return
      targetX = ((event.clientX - rect.left) / rect.width - 0.5) * 12
      targetY = ((event.clientY - rect.top) / rect.height - 0.5) * 8
    }

    const handleMotionChange = () => {
      stop()
      targetX = 0
      targetY = 0
      draw(performance.now())
      start()
    }

    const handleVisibilityChange = () => {
      visible = !document.hidden
      if (visible) start()
      else stop()
    }

    const observer =
      'IntersectionObserver' in window
        ? new IntersectionObserver(([entry]) => {
            visible = entry?.isIntersecting ?? true
            if (visible) start()
            else stop()
          })
        : null

    resize()
    observer?.observe(canvas)
    reducedMotion.addEventListener('change', handleMotionChange)
    document.addEventListener('visibilitychange', handleVisibilityChange)
    window.addEventListener('pointermove', handlePointerMove, { passive: true })
    window.addEventListener('resize', resize)

    return () => {
      stop()
      observer?.disconnect()
      reducedMotion.removeEventListener('change', handleMotionChange)
      document.removeEventListener('visibilitychange', handleVisibilityChange)
      window.removeEventListener('pointermove', handlePointerMove)
      window.removeEventListener('resize', resize)
    }
  }, [dark, paused, resolvedTheme])

  return (
    <canvas
      ref={canvasRef}
      aria-hidden='true'
      data-testid='home-signal-field'
      data-visual='routing-lattice'
      className={cn(
        'home-entry-signal-layer pointer-events-none absolute inset-0 h-full w-full',
        props.className
      )}
    />
  )
}

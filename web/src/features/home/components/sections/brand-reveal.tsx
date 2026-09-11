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

interface BrandRevealProps {
  name: string
}

// Fallbacks only; the real typography (family / weight) is read from the
// `.home-brand-reveal-word` computed style so CSS stays the single source of
// truth for both the static layer and the canvas.
const BRAND_FALLBACK_FONT_FAMILY =
  '"Lora Variable", Lora, Georgia, "Times New Roman", serif'
const BRAND_FALLBACK_FONT_WEIGHT = '600'
// Uppercase tracking, in em. Mirrors `letter-spacing` on the static word.
const BRAND_TRACKING_EM = 0.08

export function BrandReveal(props: BrandRevealProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const staticRef = useRef<HTMLDivElement>(null)
  const wordRef = useRef<HTMLSpanElement>(null)

  useEffect(() => {
    const canvas = canvasRef.current
    const staticLayer = staticRef.current
    const context = canvas?.getContext('2d')
    if (!canvas || !context || !staticLayer) return

    const source = document.createElement('canvas')
    const sourceContext = source.getContext('2d')
    if (!sourceContext) return

    const wordStyles = wordRef.current
      ? getComputedStyle(wordRef.current)
      : null
    const fontFamily = wordStyles?.fontFamily || BRAND_FALLBACK_FONT_FAMILY
    const fontWeight = wordStyles?.fontWeight || BRAND_FALLBACK_FONT_WEIGHT
    const text = props.name.toUpperCase()
    let disposed = false

    let width = 1
    let height = 1
    let cellSize = 16
    let columns = 1
    let rows = 1
    let offsetX = new Float32Array(1)
    let offsetY = new Float32Array(1)
    let frame = 0
    let running = false
    let visible = true
    let lastPointer: { x: number; y: number } | null = null

    const drawSource = () => {
      const fixedLayer = canvas.closest('.home-brand-reveal-fixed')
      const fixedStyles = fixedLayer
        ? getComputedStyle(fixedLayer)
        : getComputedStyle(document.body)
      const bodyStyles = getComputedStyle(document.body)
      const background =
        fixedStyles.backgroundColor === 'rgba(0, 0, 0, 0)'
          ? bodyStyles.backgroundColor
          : fixedStyles.backgroundColor
      const foreground = fixedStyles.color || bodyStyles.color
      let fontSize = Math.min(272, Math.max(80, width * 0.145))

      const applyFont = () => {
        sourceContext.font = `${fontWeight} ${fontSize}px ${fontFamily}`
        if ('letterSpacing' in sourceContext) {
          sourceContext.letterSpacing = `${BRAND_TRACKING_EM * fontSize}px`
        }
      }

      applyFont()
      let metrics = sourceContext.measureText(text)
      // Long system names (or narrow phones) must not spill past the edges:
      // shrink until the ink fits inside 92% of the layer width.
      const maxInkWidth = width * 0.92
      const inkWidth =
        metrics.actualBoundingBoxLeft + metrics.actualBoundingBoxRight
      if (inkWidth > maxInkWidth) {
        fontSize = Math.max(32, fontSize * (maxInkWidth / inkWidth))
        applyFont()
        metrics = sourceContext.measureText(text)
      }

      sourceContext.clearRect(0, 0, width, height)
      sourceContext.fillStyle = background
      sourceContext.fillRect(0, 0, width, height)
      sourceContext.fillStyle = foreground
      sourceContext.textAlign = 'left'
      sourceContext.textBaseline = 'alphabetic'
      // Centre the ink box rather than the em box, so the capitals sit
      // optically in the middle of the layer on both axes (trailing
      // letter-spacing and font ascent/descent would otherwise bias it).
      const x =
        width / 2 -
        (metrics.actualBoundingBoxRight - metrics.actualBoundingBoxLeft) / 2
      const y =
        height / 2 +
        (metrics.actualBoundingBoxAscent - metrics.actualBoundingBoxDescent) /
          2
      sourceContext.fillText(text, x, y)
    }

    const render = () => {
      context.clearRect(0, 0, width, height)
      let energy = 0

      for (let row = 0; row < rows; row += 1) {
        for (let column = 0; column < columns; column += 1) {
          const index = row * columns + column
          const x = column * cellSize
          const y = row * cellSize
          const cellWidth = Math.min(cellSize, width - x)
          const cellHeight = Math.min(cellSize, height - y)

          offsetX[index] *= 0.88
          offsetY[index] *= 0.88
          if (Math.abs(offsetX[index]) < 0.04) offsetX[index] = 0
          if (Math.abs(offsetY[index]) < 0.04) offsetY[index] = 0
          energy += Math.abs(offsetX[index]) + Math.abs(offsetY[index])

          context.drawImage(
            source,
            x,
            y,
            cellWidth,
            cellHeight,
            x + offsetX[index],
            y + offsetY[index],
            cellWidth,
            cellHeight
          )
        }
      }

      if (energy > 0.1 && visible) {
        frame = window.requestAnimationFrame(render)
      } else {
        running = false
      }
    }

    const requestRender = () => {
      if (running || !visible) return
      running = true
      frame = window.requestAnimationFrame(render)
    }

    const resize = () => {
      const rect = canvas.getBoundingClientRect()
      width = Math.max(1, Math.floor(rect.width))
      height = Math.max(1, Math.floor(rect.height))
      canvas.width = width
      canvas.height = height
      source.width = width
      source.height = height
      cellSize = Math.min(30, Math.max(14, Math.round(width / 76)))
      columns = Math.ceil(width / cellSize)
      rows = Math.ceil(height / cellSize)
      offsetX = new Float32Array(columns * rows)
      offsetY = new Float32Array(columns * rows)
      drawSource()
      context.drawImage(source, 0, 0)
      staticLayer.setAttribute('data-canvas-active', 'true')
    }

    const handlePointerMove = (event: PointerEvent) => {
      if (
        !visible ||
        window.matchMedia('(prefers-reduced-motion: reduce)').matches
      ) {
        return
      }

      const rect = canvas.getBoundingClientRect()
      if (
        event.clientX < rect.left ||
        event.clientX > rect.right ||
        event.clientY < rect.top ||
        event.clientY > rect.bottom
      ) {
        lastPointer = null
        return
      }

      const x = event.clientX - rect.left
      const y = event.clientY - rect.top
      const velocityX = lastPointer ? x - lastPointer.x : 0
      const velocityY = lastPointer ? y - lastPointer.y : 0
      lastPointer = { x, y }
      const radius = Math.min(width, height) * 0.36
      const startColumn = Math.max(0, Math.floor((x - radius) / cellSize))
      const endColumn = Math.min(
        columns - 1,
        Math.ceil((x + radius) / cellSize)
      )
      const startRow = Math.max(0, Math.floor((y - radius) / cellSize))
      const endRow = Math.min(rows - 1, Math.ceil((y + radius) / cellSize))
      const maxOffset = cellSize * 0.95

      for (let row = startRow; row <= endRow; row += 1) {
        for (let column = startColumn; column <= endColumn; column += 1) {
          const distance = Math.hypot(
            x - (column * cellSize + cellSize / 2),
            y - (row * cellSize + cellSize / 2)
          )
          if (distance > radius) continue
          const index = row * columns + column
          const influence = (1 - distance / radius) ** 2
          offsetX[index] = Math.max(
            -maxOffset,
            Math.min(maxOffset, offsetX[index] + velocityX * 0.9 * influence)
          )
          offsetY[index] = Math.max(
            -maxOffset,
            Math.min(maxOffset, offsetY[index] + velocityY * 0.75 * influence)
          )
        }
      }

      requestRender()
    }

    resize()
    // The serif web font is usually still downloading on first paint; the
    // canvas would otherwise keep the fallback face forever because the
    // source is only redrawn on resize. Repaint once the face is available.
    if (typeof document !== 'undefined' && 'fonts' in document) {
      document.fonts
        .load(`${fontWeight} 96px ${fontFamily}`, text)
        .then(() => {
          if (!disposed) resize()
        })
        .catch(() => {})
    }
    const resizeObserver = new ResizeObserver(resize)
    resizeObserver.observe(canvas)
    const visibilityObserver = new IntersectionObserver(([entry]) => {
      visible = entry.isIntersecting
      if (visible) requestRender()
    })
    visibilityObserver.observe(canvas)
    window.addEventListener('pointermove', handlePointerMove, { passive: true })

    return () => {
      disposed = true
      running = false
      window.cancelAnimationFrame(frame)
      resizeObserver.disconnect()
      visibilityObserver.disconnect()
      window.removeEventListener('pointermove', handlePointerMove)
      staticLayer.removeAttribute('data-canvas-active')
    }
  }, [props.name])

  return (
    <section
      aria-labelledby='home-brand-reveal-title'
      className='home-brand-reveal'
    >
      <div className='home-brand-reveal-fixed' data-brand-reveal-theme='system'>
        <div
          ref={staticRef}
          className='home-brand-reveal-static'
          aria-hidden='true'
        >
          <span ref={wordRef} className='home-brand-reveal-word'>
            {props.name.toUpperCase()}
          </span>
        </div>
        <canvas
          ref={canvasRef}
          className='home-brand-reveal-canvas'
          aria-hidden='true'
        />
        <h2 id='home-brand-reveal-title' className='sr-only'>
          {props.name.toUpperCase()}
        </h2>
      </div>
    </section>
  )
}

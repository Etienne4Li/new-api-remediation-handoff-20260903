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
  motion,
  type MotionValue,
  useReducedMotion,
  useScroll,
  useTransform,
} from 'motion/react'
import { useRef } from 'react'
import { useTranslation } from 'react-i18next'

function StatementPhrase(props: {
  className?: string
  end: number
  progress: MotionValue<number>
  start: number
  text: string
}) {
  const reducedMotion = useReducedMotion()
  const opacity = useTransform(
    props.progress,
    [props.start, props.end],
    [0.15, 1]
  )
  const y = useTransform(props.progress, [props.start, props.end], [18, 0])

  if (reducedMotion) {
    return <span className={props.className}>{props.text}</span>
  }

  return (
    <motion.span
      className={`inline-block ${props.className ?? ''}`}
      style={{ opacity, y }}
    >
      {props.text}
    </motion.span>
  )
}

export function BrandStatement() {
  const { t } = useTranslation()
  const sectionRef = useRef<HTMLElement>(null)
  const { scrollYProgress } = useScroll({
    target: sectionRef,
    offset: ['start 0.9', 'start 0.35'],
  })
  const words = [
    t('Every model.'),
    t('One gateway.'),
    t('No theater.'),
  ].flatMap((phrase, phraseIndex) =>
    phrase
      .split(/\s+/)
      .filter(Boolean)
      .map((text) => ({
        id: `${phraseIndex}-${text}`,
        serif: phraseIndex === 1,
        text,
      }))
  )

  return (
    <section
      ref={sectionRef}
      className='relative isolate z-10 flex min-h-svh items-center px-4 py-24 sm:px-6 lg:sticky lg:top-0 lg:px-8'
    >
      <div className='mx-auto max-w-[62rem]'>
        <p className='text-center text-[clamp(2.5rem,6.5vw,5.75rem)] leading-[1.08] font-semibold tracking-[-0.02em] text-balance'>
          {words.map((word, index) => {
            const start = (index / words.length) * 0.8
            return (
              <span key={word.id}>
                <StatementPhrase
                  className={word.serif ? 'font-serif' : undefined}
                  end={Math.min(start + 0.2, 1)}
                  progress={scrollYProgress}
                  start={start}
                  text={word.text}
                />
                {index < words.length - 1 ? ' ' : null}
              </span>
            )
          })}
        </p>
      </div>
    </section>
  )
}

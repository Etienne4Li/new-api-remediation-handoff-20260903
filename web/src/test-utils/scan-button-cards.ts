/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { readdirSync, readFileSync } from 'node:fs'
import { join, relative } from 'node:path'

import { lockedHeightClasses, optsOutOfFixedHeight } from './height-contract'

/**
 * Static sweep for Buttons used as multi-line cards.
 *
 * The DOM assertions next to each component only cover the components that
 * have a test. This sweep is what keeps the audit standing: the next Button
 * that stacks content while holding a fixed height fails here, wherever in the
 * tree it is written.
 */

/** The height class `buttonVariants` injects for each `size`. */
const SIZE_HEIGHT: Record<string, string> = {
  default: 'h-8',
  xs: 'h-6',
  sm: 'h-7',
  lg: 'h-9',
  icon: 'size-8',
  'icon-xs': 'size-6',
  'icon-sm': 'size-7',
  'icon-lg': 'size-9',
}

export interface ButtonCardFinding {
  file: string
  line: number
  /** Why the content can occupy more than one line. */
  stacksBecause: string[]
  /** What the contract says is wrong with it. */
  problem: string
}

/**
 * Walk a JSX open tag, tolerating `{…}` expressions, quoted strings and
 * comments — an apostrophe inside a `// …` comment must not be read as the
 * start of a string literal.
 */
function readOpenTag(source: string, from: number): number {
  let depth = 0
  for (let i = from; i < source.length; i++) {
    const char = source[i]
    if (char === '/' && source[i + 1] === '/') {
      i = source.indexOf('\n', i)
      if (i === -1) return -1
    } else if (char === '/' && source[i + 1] === '*') {
      i = source.indexOf('*/', i)
      if (i === -1) return -1
      i++
    } else if (char === '"' || char === "'" || char === '`') {
      const close = source.indexOf(char, i + 1)
      if (close === -1) return -1
      i = close
    } else if (char === '{') {
      depth++
    } else if (char === '}') {
      depth--
    } else if (char === '>' && depth === 0) {
      return i
    }
  }
  return -1
}

/**
 * Every string literal inside the `className` prop, split into class tokens.
 *
 * Comments are skipped rather than matched over: a `cn(…)` call is routinely
 * annotated, and prose about `h-8` in a comment is not a class on the element.
 */
function classTokens(openTag: string): string[] {
  const at = openTag.indexOf('className=')
  if (at === -1) return []
  const rest = openTag.slice(at + 'className='.length)

  if (rest.startsWith('"') || rest.startsWith("'")) {
    const quote = rest[0]
    return rest.slice(1, rest.indexOf(quote, 1)).split(/\s+/).filter(Boolean)
  }
  if (!rest.startsWith('{')) return []

  const tokens: string[] = []
  let depth = 0
  for (let i = 0; i < rest.length; i++) {
    const char = rest[i]
    if (char === '/' && rest[i + 1] === '/') {
      i = rest.indexOf('\n', i)
      if (i === -1) break
    } else if (char === '/' && rest[i + 1] === '*') {
      i = rest.indexOf('*/', i)
      if (i === -1) break
      i++
    } else if (char === '"' || char === "'" || char === '`') {
      const close = rest.indexOf(char, i + 1)
      if (close === -1) break
      tokens.push(
        ...rest
          .slice(i + 1, close)
          .split(/\s+/)
          .filter(Boolean)
      )
      i = close
    } else if (char === '{') {
      depth++
    } else if (char === '}' && --depth === 0) {
      break
    }
  }
  return tokens
}

/** The children of a `<Button>`, or '' when it is self-closing. */
function readChildren(source: string, afterOpenTag: number): string {
  let depth = 1
  const pattern = /<\/Button\s*>|<Button(?=[\s/>])/g
  pattern.lastIndex = afterOpenTag
  for (let m = pattern.exec(source); m; m = pattern.exec(source)) {
    if (m[0].startsWith('</')) {
      if (--depth === 0) return source.slice(afterOpenTag, m.index)
    } else {
      depth++
    }
  }
  return ''
}

function* tsxFiles(dir: string): Generator<string> {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) {
      if (entry.name !== 'node_modules') yield* tsxFiles(full)
    } else if (entry.name.endsWith('.tsx')) {
      yield full
    }
  }
}

export function scanButtonCards(root: string): ButtonCardFinding[] {
  const findings: ButtonCardFinding[] = []

  for (const file of tsxFiles(root)) {
    const source = readFileSync(file, 'utf8')
    if (!source.includes('<Button')) continue

    for (const match of source.matchAll(/<Button(?=[\s/>])/g)) {
      const tagEnd = readOpenTag(source, match.index + '<Button'.length)
      if (tagEnd === -1) continue
      const openTag = source.slice(match.index, tagEnd + 1)
      const children = openTag.endsWith('/>')
        ? ''
        : readChildren(source, tagEnd + 1)

      const tokens = classTokens(openTag)
      const className = tokens.join(' ')

      // Can the content occupy more than one line? The Button base sets
      // `whitespace-nowrap`, so a single run of text cannot wrap on its own —
      // it takes a column layout or an explicit wrapping class.
      const stacksBecause = [
        tokens.includes('flex-col') && 'flex-col on the button',
        /\bflex-col\b/.test(children) && 'flex-col in its children',
        tokens.some(
          (t) => t.startsWith('whitespace-') && t !== 'whitespace-nowrap'
        ) && 'text is allowed to wrap',
        tokens.some((t) => t.replace(/^.*:/, '').startsWith('min-h-')) &&
          'min-h-* implies more than one line was expected',
      ].filter((reason): reason is string => typeof reason === 'string')
      if (stacksBecause.length === 0) continue

      // An unprefixed h-*/size-* on the call site replaces the variant's; with
      // none, the variant's own fixed height stands.
      const sizeMatch = /size=(?:'([\w-]+)'|"([\w-]+)"|\{'([\w-]+)'\})/.exec(
        openTag
      )
      const size =
        sizeMatch?.[1] ?? sizeMatch?.[2] ?? sizeMatch?.[3] ?? 'default'
      const override = tokens.filter(
        (t) => !t.includes(':') && /^(?:h|size)-/.test(t)
      )
      const effective =
        override.length > 0
          ? override.join(' ')
          : (SIZE_HEIGHT[size] ?? SIZE_HEIGHT.default)

      const locked = lockedHeightClasses(effective)
      if (locked.length === 0 && optsOutOfFixedHeight(effective)) continue
      if (locked.length === 0) continue

      findings.push({
        file: relative(root, file),
        line: source.slice(0, match.index).split('\n').length,
        stacksBecause,
        problem:
          override.length > 0
            ? `pins its height with ${locked.map((c) => `\`${c}\``).join(', ')}`
            : `inherits the fixed \`${locked[0]}\` from size='${size}' and never releases it${className.includes('min-h-') ? ' (a `min-h-*` cannot: different CSS property)' : ''}`,
      })
    }
  }

  return findings.sort((a, b) =>
    a.file === b.file ? a.line - b.line : a.file.localeCompare(b.file)
  )
}

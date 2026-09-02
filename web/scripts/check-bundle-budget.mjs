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
import { readFile, readdir } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { gzipSync } from 'node:zlib'

import { JSDOM } from 'jsdom'

const scriptDirectory = path.dirname(fileURLToPath(import.meta.url))
const distDirectory = path.resolve(scriptDirectory, '../dist')
const jsDirectory = path.join(distDirectory, 'static/js')

const LIMITS = {
  initialJavaScript: { raw: 2.5 * 1024 * 1024, gzip: 700 * 1024 },
  largestAsyncChunk: { raw: 7 * 1024 * 1024, gzip: 3 * 1024 * 1024 },
  totalJavaScript: { raw: 58 * 1024 * 1024, gzip: 17 * 1024 * 1024 },
}

async function listFiles(directory) {
  const entries = await readdir(directory, { withFileTypes: true })
  const nestedFiles = await Promise.all(
    entries.map((entry) => {
      const entryPath = path.join(directory, entry.name)
      return entry.isDirectory() ? listFiles(entryPath) : [entryPath]
    })
  )
  return nestedFiles.flat()
}

async function measureFiles(files) {
  const buffers = await Promise.all(files.map((file) => readFile(file)))
  return buffers.reduce(
    (totals, buffer) => ({
      raw: totals.raw + buffer.byteLength,
      gzip: totals.gzip + gzipSync(buffer).byteLength,
    }),
    { raw: 0, gzip: 0 }
  )
}

function formatBytes(bytes) {
  return `${(bytes / 1024).toFixed(1)} KiB`
}

function recordBudget(violations, label, measurement, limit) {
  if (measurement.raw > limit.raw) {
    violations.push(
      `${label} raw ${formatBytes(measurement.raw)} exceeds ${formatBytes(limit.raw)}`
    )
  }
  if (measurement.gzip > limit.gzip) {
    violations.push(
      `${label} gzip ${formatBytes(measurement.gzip)} exceeds ${formatBytes(limit.gzip)}`
    )
  }
}

const html = await readFile(path.join(distDirectory, 'index.html'), 'utf8')
const document = new JSDOM(html).window.document
const initialJavaScriptFiles = [
  ...document.querySelectorAll('script[src]'),
].map((script) => {
  const source = script.getAttribute('src')
  if (!source) throw new Error('Generated script tag is missing src')

  const assetPath = path.resolve(distDirectory, source.replace(/^\/+/, ''))
  if (!assetPath.startsWith(`${distDirectory}${path.sep}`)) {
    throw new Error(`Generated script path escapes dist: ${source}`)
  }
  return assetPath
})

const allJavaScriptFiles = (await listFiles(jsDirectory)).filter((file) =>
  file.endsWith('.js')
)
const asyncJavaScriptFiles = allJavaScriptFiles.filter((file) =>
  file.includes(`${path.sep}async${path.sep}`)
)

const initialJavaScript = await measureFiles(initialJavaScriptFiles)
const totalJavaScript = await measureFiles(allJavaScriptFiles)
const asyncChunkMeasurements = await Promise.all(
  asyncJavaScriptFiles.map(async (file) => ({
    file,
    ...(await measureFiles([file])),
  }))
)
const largestAsyncChunk = asyncChunkMeasurements.reduce(
  (largest, current) => (current.raw > largest.raw ? current : largest),
  { file: '', raw: 0, gzip: 0 }
)

const violations = []
recordBudget(
  violations,
  'Initial JavaScript',
  initialJavaScript,
  LIMITS.initialJavaScript
)
recordBudget(
  violations,
  `Largest async chunk (${path.basename(largestAsyncChunk.file)})`,
  largestAsyncChunk,
  LIMITS.largestAsyncChunk
)
recordBudget(
  violations,
  'Total JavaScript',
  totalJavaScript,
  LIMITS.totalJavaScript
)

console.log(
  `Bundle budget: initial ${formatBytes(initialJavaScript.raw)} raw / ${formatBytes(initialJavaScript.gzip)} gzip`
)
console.log(
  `Bundle budget: largest async ${formatBytes(largestAsyncChunk.raw)} raw / ${formatBytes(largestAsyncChunk.gzip)} gzip (${path.basename(largestAsyncChunk.file)})`
)
console.log(
  `Bundle budget: total ${formatBytes(totalJavaScript.raw)} raw / ${formatBytes(totalJavaScript.gzip)} gzip`
)

if (violations.length > 0) {
  throw new Error(`Bundle budget exceeded:\n${violations.join('\n')}`)
}

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


export type ChatLinkType = 'web' | 'custom-protocol' | 'fluent'

export type ChatPreset = {
  id: string
  name: string
  url: string
  type: ChatLinkType
}

export type RawChatConfig =
  | string
  | Record<string, unknown>
  | Array<Record<string, unknown>>
  | null
  | undefined

export type ResolveChatUrlParams = {
  template: string
  apiKey?: string
  serverAddress: string
}

const HTTP_REGEX = /^https?:\/\//i
const HTTP_AUTHORITY_REGEX = /^https?:\/\/[^\s/?#]+/i
const EXPLICIT_SCHEME_REGEX = /^([a-z][a-z0-9+.-]*):/i
const FLUENT_LINK = /^fluentread(?:$|:\/\/)/i
const BARE_APP_LINKS = new Set(['fluentread', 'ccswitch'])
const MAX_CHAT_CONFIG_BYTES = 512 * 1024
const MAX_CHAT_ENTRIES = 64
const MAX_CHAT_NAME_LENGTH = 256
const MAX_CHAT_URL_LENGTH = 16 * 1024
const MAX_DECODE_PASSES = 5
const MAX_JSON_DEPTH = 8

// A chat preset is administrator-configured but is still rendered/activated
// in a user's browser. Browser-executable and local-resource schemes must not
// be reachable through the generic "open preset" action: `javascript:` and
// `data:` can execute in the opener's context, while `file:`/`blob:` can
// expose local or opaque data. Supported desktop integrations use ordinary
// application schemes and remain allowed.
const BLOCKED_CHAT_SCHEMES = new Set([
  'javascript',
  'data',
  'vbscript',
  'file',
  'filesystem',
  'blob',
  'about',
  'chrome',
  'chrome-extension',
  'view-source',
])

const CHAT_KEY_PLACEHOLDERS = [
  '{key}',
  '{cherryConfig}',
  '{aionuiConfig}',
  '{deepchatConfig}',
] as const
const CHAT_KEY_PLACEHOLDER_NAMES = new Set([
  'key',
  'cherryconfig',
  'aionuiconfig',
  'deepchatconfig',
])
const CHAT_ALLOWED_PLACEHOLDER_NAMES = new Set([
  'address',
  ...CHAT_KEY_PLACEHOLDER_NAMES,
])
const CHAT_PLACEHOLDER_REGEX = /\{([A-Za-z][A-Za-z0-9_]*)\}/g

// NewAPI keys are normally exposed to clients with an `sk-` prefix. Reject a
// literal key in a preset as well as the supported placeholders: an
// administrator may have copied a resolved URL into the setting, and opening
// it would otherwise send the credential straight to the configured client.
// Keep the minimum length deliberately conservative so ordinary `sk-` labels
// in a URL are not treated as credentials.
const LITERAL_API_KEY_REGEX = /(?:^|[^a-z0-9])sk-[a-z0-9][a-z0-9._~+/=-]{7,}/i
const SENSITIVE_FIELD_NAMES = new Set([
  'key',
  'apikey',
  'xapikey',
  'accesstoken',
  'authorization',
  'auth',
  'token',
  'bearer',
  'secret',
  'secretkey',
  'credential',
  'password',
  'privatekey',
  'clientsecret',
  'accesskey',
  'sessiontoken',
])

function decodeCandidates(source: string): string[] | null {
  const candidates = [source]
  let current = source
  for (let i = 0; i < MAX_DECODE_PASSES; i += 1) {
    if (!current.includes('%')) break
    try {
      const decoded = decodeURIComponent(current)
      if (decoded === current) break
      candidates.push(decoded)
      current = decoded
    } catch {
      return null
    }
    if (i === MAX_DECODE_PASSES - 1 && current.includes('%')) return null
  }
  return candidates
}

function normalizedFieldName(value: string): string {
  return value
    .trim()
    .toLowerCase()
    .replaceAll(/[^a-z0-9]/g, '')
}

function isSensitiveField(value: string): boolean {
  return SENSITIVE_FIELD_NAMES.has(normalizedFieldName(value))
}

function containsSensitiveJson(value: unknown, depth = 0): boolean {
  if (depth > MAX_JSON_DEPTH) {
    return true
  }
  if (value === null || typeof value !== 'object') {
    return false
  }
  if (Array.isArray(value)) {
    return value.some((item) => containsSensitiveJson(item, depth + 1))
  }
  return Object.entries(value).some(([key, item]) => {
    if (isSensitiveField(key)) return true
    return containsSensitiveJson(item, depth + 1)
  })
}

function containsEncodedSensitiveJson(value: string): boolean {
  if (
    value.length < 8 ||
    value.length > MAX_CHAT_URL_LENGTH ||
    !/^[a-z0-9+/_=-]+$/i.test(value)
  ) {
    return false
  }
  try {
    const normalized = value.replaceAll('-', '+').replaceAll('_', '/')
    const padded = normalized + '='.repeat((4 - (normalized.length % 4)) % 4)
    if (typeof globalThis.atob !== 'function') {
      return false
    }
    const decoded = globalThis.atob(padded)
    let parsed: unknown
    try {
      parsed = JSON.parse(decoded)
    } catch {
      return false
    }
    return containsSensitiveJson(parsed)
  } catch {
    return false
  }
}

function containsSensitiveQueryOrJson(source: string): boolean {
  const queryStart = source.indexOf('?')
  const fragmentStart = source.indexOf('#')
  let query = ''
  if (queryStart >= 0) {
    const end =
      fragmentStart >= 0 && fragmentStart > queryStart
        ? fragmentStart
        : undefined
    query = source.slice(queryStart + 1, end)
  } else if (fragmentStart >= 0) {
    query = source.slice(fragmentStart + 1)
  }
  if (query) {
    for (const pair of query.split('&')) {
      if (!pair) continue
      const separator = pair.indexOf('=')
      const rawKey = separator >= 0 ? pair.slice(0, separator) : pair
      const rawValue = separator >= 0 ? pair.slice(separator + 1) : ''
      const keyCandidates = decodeCandidates(rawKey)
      const valueCandidates = decodeCandidates(rawValue)
      if (!keyCandidates || !valueCandidates) return true
      const sensitiveKey = keyCandidates.some(isSensitiveField)
      if (sensitiveKey) return true
      if (
        valueCandidates.some((value) => {
          if (
            CHAT_KEY_PLACEHOLDERS.some((placeholder) =>
              value.toLowerCase().includes(placeholder.toLowerCase())
            )
          ) {
            return true
          }
          if (LITERAL_API_KEY_REGEX.test(value)) return true
          if (containsEncodedSensitiveJson(value)) return true
          try {
            return containsSensitiveJson(JSON.parse(value))
          } catch {
            return false
          }
        })
      ) {
        return true
      }
    }
  }
  return false
}

function containsLiteralApiKey(url: string): boolean {
  const candidates = decodeCandidates(url)
  if (!candidates) return true
  for (const candidate of candidates) {
    if (LITERAL_API_KEY_REGEX.test(candidate)) {
      return true
    }
    if (containsSensitiveQueryOrJson(candidate)) {
      return true
    }
  }
  return false
}

export function detectChatLinkType(url: string): ChatLinkType {
  const candidate = url.trim()
  if (HTTP_REGEX.test(candidate)) {
    return 'web'
  }
  if (FLUENT_LINK.test(candidate)) {
    return 'fluent'
  }
  return 'custom-protocol'
}

export function chatLinkRequiresApiKey(url: string): boolean {
  const candidates = decodeCandidates(url)
  if (!candidates) return true
  if (candidates.some((candidate) => hasControlCharacters(candidate))) {
    return true
  }
  return (
    candidates.some((candidate) =>
      CHAT_KEY_PLACEHOLDERS.some((placeholder) =>
        candidate.toLowerCase().includes(placeholder.toLowerCase())
      )
    ) || containsLiteralApiKey(url)
  )
}

export function isSafeChatLink(url: string): boolean {
  const candidate = url.trim()
  if (
    !candidate ||
    candidate.length > MAX_CHAT_URL_LENGTH ||
    hasControlCharacters(candidate) ||
    candidate.startsWith('//')
  ) {
    return false
  }

  const schemeMatch = EXPLICIT_SCHEME_REGEX.exec(candidate)
  if (!schemeMatch) return BARE_APP_LINKS.has(candidate.toLowerCase())
  const scheme = schemeMatch[1].toLowerCase()
  if (BLOCKED_CHAT_SCHEMES.has(scheme)) return false
  if (
    BARE_APP_LINKS.has(scheme) &&
    !candidate.slice(schemeMatch[0].length).startsWith('//')
  ) {
    return false
  }

  const candidates = decodeCandidates(candidate)
  if (!candidates) return false
  for (const decoded of candidates) {
    if (hasControlCharacters(decoded) || hasUnsupportedPlaceholder(decoded)) {
      return false
    }
    const decodedSchemeMatch = EXPLICIT_SCHEME_REGEX.exec(decoded)
    if (!decodedSchemeMatch) return false
    const decodedScheme = decodedSchemeMatch[1].toLowerCase()
    if (BLOCKED_CHAT_SCHEMES.has(decodedScheme)) return false
    const authorityMatch = /^[a-z][a-z0-9+.-]*:\/\/([^/?#]*)/i.exec(decoded)
    if (authorityMatch?.[1].includes('@')) return false
    try {
      const parsed = new URL(decoded)
      if (parsed.username || parsed.password) return false
      if (
        (decodedScheme === 'http' || decodedScheme === 'https') &&
        (!HTTP_AUTHORITY_REGEX.test(decoded) || !parsed.hostname)
      ) {
        return false
      }
    } catch {
      return false
    }
  }
  return !chatLinkRequiresApiKey(candidate)
}

function hasControlCharacters(value: string): boolean {
  return [...value].some((char) => {
    const code = char.charCodeAt(0)
    return code < 0x20 || code === 0x7f
  })
}

function hasUnsupportedPlaceholder(value: string): boolean {
  for (const match of value.matchAll(CHAT_PLACEHOLDER_REGEX)) {
    if (!CHAT_ALLOWED_PLACEHOLDER_NAMES.has(match[1].toLowerCase())) return true
  }
  return false
}

export function parseChatConfig(raw: RawChatConfig): ChatPreset[] {
  let parsed: unknown = raw

  if (typeof raw === 'string') {
    if (raw.length > MAX_CHAT_CONFIG_BYTES) return []
    try {
      parsed = JSON.parse(raw)
    } catch {
      return []
    }
  }

  if (!Array.isArray(parsed) || parsed.length > MAX_CHAT_ENTRIES) {
    return []
  }

  return parsed
    .map((entry, index) => {
      if (
        !entry ||
        typeof entry !== 'object' ||
        Array.isArray(entry) ||
        Object.keys(entry).length !== 1
      ) {
        return null
      }

      const [name, value] = Object.entries(entry)[0]
      if (
        typeof value !== 'string' ||
        typeof name !== 'string' ||
        name.trim().length === 0 ||
        name.trim().length > MAX_CHAT_NAME_LENGTH
      ) {
        return null
      }

      const url = value.trim()
      if (
        !url ||
        url.length > MAX_CHAT_URL_LENGTH ||
        !isSafeChatLink(url) ||
        chatLinkRequiresApiKey(url)
      ) {
        return null
      }

      return {
        // The array is re-indexed after unsafe entries are removed below.
        id: String(index),
        name,
        url,
        type: detectChatLinkType(url),
      } satisfies ChatPreset
    })
    .filter((item): item is ChatPreset => item !== null)
    .map((item, index) => ({ ...item, id: String(index) }))
}

function replaceToken(source: string, token: string, value: string) {
  return source.split(token).join(value)
}

export function resolveChatUrl({
  template,
  serverAddress,
}: ResolveChatUrlParams): string {
  let url = template.trim()
  const safeServerAddress = serverAddress || ''

  if (!isSafeChatLink(url)) {
    return ''
  }

  // A URL (including a custom protocol URL) is observable by the destination
  // client, browser/OS history and intermediary logs. Never inject or forward
  // a long-lived NewAPI key from a chat preset. Callers may still pass the
  // legacy `apiKey` property for source compatibility, but it is intentionally
  // ignored. Key-bearing presets are rejected so an unresolved placeholder
  // cannot be mistaken for a usable credential.
  if (chatLinkRequiresApiKey(url)) {
    return ''
  }

  if (safeServerAddress) {
    const encodedAddress = encodeURIComponent(safeServerAddress)
    url = replaceToken(url, '{address}', encodedAddress)
  }

  return url
}

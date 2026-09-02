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
const USAGE_AUTO_QUERY_INTERVAL_MINUTES = 5

const USAGE_SCRIPT = `({
  request: {
    url: "{{baseUrl}}/api/usage/balance/",
    method: "GET",
    headers: {
      "Authorization": "Bearer {{apiKey}}",
      "Accept": "application/json"
    }
  },
  extractor: function(response) {
    if (!response.success || !response.data) {
      return {
        isValid: false,
        invalidMessage: response.message || "Balance query failed"
      };
    }

    return {
      isValid: true,
      remaining: Number(response.data.remaining),
      unit: response.data.unit
    };
  }
})`

export interface BuildCCSwitchURLOptions {
  app: string
  name: string
  models: Record<string, string>
  apiKey: string
  serverAddress: string
}

const ALLOWED_CC_SWITCH_APPS = new Set(['claude', 'codex', 'gemini'])

function encodeBase64URL(value: string): string {
  const bytes = new TextEncoder().encode(value)
  const binary = Array.from(bytes, (byte) => String.fromCharCode(byte)).join('')
  return btoa(binary)
    .replaceAll('+', '-')
    .replaceAll('/', '_')
    .replace(/=+$/, '')
}

export function buildCCSwitchURL(options: BuildCCSwitchURLOptions): string {
  if (!ALLOWED_CC_SWITCH_APPS.has(options.app)) return ''
  const normalizedApiKey = options.apiKey.trim()
  if (!normalizedApiKey) return ''

  let parsedServerAddress: URL
  try {
    parsedServerAddress = new URL(options.serverAddress)
  } catch {
    return ''
  }
  if (
    (parsedServerAddress.protocol !== 'http:' &&
      parsedServerAddress.protocol !== 'https:') ||
    !parsedServerAddress.hostname ||
    parsedServerAddress.username ||
    parsedServerAddress.password
  ) {
    return ''
  }
  const serverAddress = parsedServerAddress.origin
  const endpoint =
    options.app === 'codex' ? `${serverAddress}/v1` : serverAddress
  const apiKey = normalizedApiKey.startsWith('sk-')
    ? normalizedApiKey
    : `sk-${normalizedApiKey}`
  const params = new URLSearchParams()

  params.set('resource', 'provider')
  params.set('app', options.app)
  params.set('name', options.name)
  params.set('endpoint', endpoint)
  params.set('apiKey', apiKey)
  for (const [key, value] of Object.entries(options.models)) {
    if (value) params.set(key, value)
  }
  params.set('homepage', serverAddress)
  params.set('enabled', 'true')
  params.set('usageEnabled', 'true')
  params.set('usageBaseUrl', serverAddress)
  params.set('usageScript', encodeBase64URL(USAGE_SCRIPT))
  params.set('usageAutoInterval', String(USAGE_AUTO_QUERY_INTERVAL_MINUTES))

  return `ccswitch://v1/import?${params.toString()}`
}

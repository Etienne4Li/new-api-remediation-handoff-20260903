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
import { describe, expect, it, test } from 'vitest'

import { buildCCSwitchURL } from '../cc-switch'

function parseCCSwitchURL(url: string): URLSearchParams {
  return new URL(url).searchParams
}

describe('buildCCSwitchURL input validation', () => {
  const base = {
    app: 'claude',
    name: 'Test',
    models: { model: 'claude-test' },
    apiKey: 'token',
    serverAddress: 'https://example.com/path?ignored=1',
  }

  it.each([
    ['malformed server address', { serverAddress: 'not a url' }],
    ['dangerous server scheme', { serverAddress: 'javascript:alert(1)' }],
    ['server userinfo', { serverAddress: 'https://user:pass@example.com' }],
    ['blank api key', { apiKey: ' \t\n' }],
    ['unknown application', { app: 'unknown' }],
  ])('rejects %s', (_label, override) => {
    expect(buildCCSwitchURL({ ...base, ...override })).toBe('')
  })
})

describe('CC Switch import URL', () => {
  test('enables a five-minute balance query from the site root', () => {
    const params = parseCCSwitchURL(
      buildCCSwitchURL({
        app: 'claude',
        name: 'Lietio Claude',
        models: { model: 'claude-sonnet-4-6' },
        apiKey: 'token-value',
        serverAddress: 'https://lietio.com/v1/',
      })
    )

    expect(params.get('endpoint')).toBe('https://lietio.com')
    expect(params.get('enabled')).toBe('true')
    expect(params.get('usageEnabled')).toBe('true')
    expect(params.get('usageBaseUrl')).toBe('https://lietio.com')
    expect(params.get('usageAutoInterval')).toBe('5')
    expect(params.has('usageApiKey')).toBe(false)

    const encodedScript = params.get('usageScript')
    expect(encodedScript).toBeTruthy()
    expect(encodedScript).toMatch(/^[A-Za-z0-9_-]+$/)
    if (!encodedScript) throw new Error('missing usage script')

    const script = Buffer.from(encodedScript, 'base64url').toString('utf8')
    expect(script).toMatch(/{{baseUrl}}\/api\/usage\/balance\//)
    expect(script).toMatch(/Authorization.*Bearer {{apiKey}}/s)
    expect(script).toMatch(/remaining: Number\(response\.data\.remaining\)/)
    expect(script).toMatch(/unit: response\.data\.unit/)
    expect(script.includes('/api/usage/token/')).toBe(false)
    expect(script.includes('total:')).toBe(false)
    expect(script.includes('used:')).toBe(false)
    expect(script.includes('token-value')).toBe(false)
  })

  test('adds the sk prefix to a bare API key', () => {
    const params = parseCCSwitchURL(
      buildCCSwitchURL({
        app: 'gemini',
        name: 'Lietio Gemini',
        models: { model: 'gemini-2.5-pro' },
        apiKey: 'token-value',
        serverAddress: 'https://lietio.com',
      })
    )

    expect(params.get('apiKey')).toBe('sk-token-value')
  })

  test('keeps Codex inference on v1 without duplicating an sk prefix', () => {
    const params = parseCCSwitchURL(
      buildCCSwitchURL({
        app: 'codex',
        name: 'Lietio Codex',
        models: { model: 'gpt-5.6' },
        apiKey: 'sk-existing',
        serverAddress: 'https://lietio.com/',
      })
    )

    expect(params.get('endpoint')).toBe('https://lietio.com/v1')
    expect(params.get('usageBaseUrl')).toBe('https://lietio.com')
    expect(params.get('apiKey')).toBe('sk-existing')
    expect(params.get('model')).toBe('gpt-5.6')
  })
})

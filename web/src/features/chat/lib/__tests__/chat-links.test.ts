/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.
*/
import { describe, expect, test } from 'vitest'

import {
  chatLinkRequiresApiKey,
  detectChatLinkType,
  isSafeChatLink,
  parseChatConfig,
  resolveChatUrl,
} from '../chat-links'

describe('chat link security', () => {
  test('rejects a web preset that would expose an API key', () => {
    const url = resolveChatUrl({
      template: 'https://third-party.example/chat?key={key}&address={address}',
      apiKey: 'sk-secret-key',
      serverAddress: 'https://api.example.com',
    })

    expect(
      chatLinkRequiresApiKey('https://third-party.example/?key={key}')
    ).toBe(true)
    expect(url).toBe('')
    expect(url).not.toContain('sk-secret-key')
  })

  test('keeps keyless web links usable without fetching a token', () => {
    expect(
      resolveChatUrl({
        template: 'https://third-party.example/chat?api={address}',
        apiKey: 'sk-secret-key',
        serverAddress: 'https://api.example.com',
      })
    ).toBe('https://third-party.example/chat?api=https%3A%2F%2Fapi.example.com')
  })

  test('does not inject a long-lived key into local protocol links', () => {
    expect(
      resolveChatUrl({
        template: 'cherrystudio://open?key={key}',
        apiKey: 'raw-secret-key',
        serverAddress: 'https://api.example.com',
      })
    ).toBe('')
  })

  test('does not inject a long-lived key into encoded client configs', () => {
    const url = resolveChatUrl({
      template: 'cherrystudio://open?data={cherryConfig}',
      apiKey: 'raw-secret-key',
      serverAddress: 'https://api.example.com',
    })

    expect(url).toBe('')
    expect(url).not.toContain('raw-secret-key')
  })

  test.each(['{key}', '{cherryConfig}', '{aionuiConfig}', '{deepchatConfig}'])(
    'rejects every key-bearing placeholder: %s',
    (placeholder) => {
      expect(
        resolveChatUrl({
          template: `client://open?value=${placeholder}`,
          apiKey: 'raw-secret-key',
          serverAddress: 'https://api.example.com',
        })
      ).toBe('')
    }
  )

  test('keeps a keyless custom protocol link usable without a key', () => {
    expect(
      resolveChatUrl({
        template: 'client://open?address={address}',
        apiKey: 'raw-secret-key',
        serverAddress: 'https://api.example.com',
      })
    ).toBe('client://open?address=https%3A%2F%2Fapi.example.com')
  })

  test('rejects a preset containing a literal API key', () => {
    expect(
      chatLinkRequiresApiKey('cherrystudio://open?key=sk-already-exposed')
    ).toBe(true)
    expect(
      chatLinkRequiresApiKey(
        'cherrystudio://open?key=sk-%61%6c%72%65%61%64%79%2dexposed'
      )
    ).toBe(true)

    const url = resolveChatUrl({
      template: 'cherrystudio://open?key=sk-already-exposed',
      serverAddress: 'https://api.example.com',
    })

    expect(url).toBe('')
    expect(url).not.toContain('sk-already-exposed')
  })

  test('blocks browser and local-resource schemes', () => {
    for (const scheme of [
      'javascript:',
      'data:',
      'vbscript:',
      'file:',
      'blob:',
    ]) {
      expect(isSafeChatLink(`${scheme}alert(1)`)).toBe(false)
      expect(
        resolveChatUrl({ template: `${scheme}alert(1)`, serverAddress: '' })
      ).toBe('')
    }
  })

  test('allows explicit application schemes and requires an explicit scheme', () => {
    expect(isSafeChatLink('cherrystudio://providers/api-keys')).toBe(true)
    expect(isSafeChatLink('fluentread')).toBe(true)
    expect(isSafeChatLink('ccswitch')).toBe(true)
    expect(isSafeChatLink('ccswitch://v1/import?x=1')).toBe(true)
    expect(isSafeChatLink('fluentevil://open')).toBe(true)
    expect(isSafeChatLink('mailto:test@example.com')).toBe(true)
    expect(isSafeChatLink('fluentread://open')).toBe(true)
    expect(isSafeChatLink('//example.com/chat')).toBe(false)
  })

  test.each([
    'foo://user:pass@host',
    'foo://user%3Apass@host',
    'foo://@host',
    'foo://%40host',
    'http:foo',
    'https:foo',
    'http:/foo',
    'fluentread:evil',
    'client://open?value={unknown}',
    'client://open?key=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    'client://open?x=%2500',
    'foo://host?x=%0a',
    'client://open?x=%25252525252525',
  ])('rejects unsafe chat link %s', (url) => {
    expect(isSafeChatLink(url)).toBe(false)
  })

  test('detects encoded and embedded credentials before opening a link', () => {
    expect(chatLinkRequiresApiKey('client://open?value=%7Bkey%7D')).toBe(true)
    expect(
      chatLinkRequiresApiKey('client://open?value=%25257Bkey%25257D')
    ).toBe(true)
    expect(chatLinkRequiresApiKey('client://open?apiKey=literal')).toBe(true)
    expect(
      chatLinkRequiresApiKey('client://open?config=eyJhcGlLZXkiOiJ4In0')
    ).toBe(true)
    expect(chatLinkRequiresApiKey('client://open?key=%zz')).toBe(true)
  })

  test('does not treat similarly named protocols as FluentRead', () => {
    expect(detectChatLinkType('fluentevil://open')).toBe('custom-protocol')
    expect(detectChatLinkType('fluentread:evil')).toBe('custom-protocol')
  })

  test('rejects raw and sensitive token query values', () => {
    expect(
      chatLinkRequiresApiKey(
        'client://open?key=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
      )
    ).toBe(true)
    expect(chatLinkRequiresApiKey('client://open?x=%2500')).toBe(true)
    expect(chatLinkRequiresApiKey('client://open?api-key=public')).toBe(true)
    expect(chatLinkRequiresApiKey('client://open?x-api-key=public')).toBe(true)
    expect(
      chatLinkRequiresApiKey('client://open?%61%70%69%2d%6b%65%79=public')
    ).toBe(true)
    expect(
      chatLinkRequiresApiKey(
        'client://open?id=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
      )
    ).toBe(false)
  })

  test('filters unsafe presets while parsing status data', () => {
    const parsed = parseChatConfig([
      { Safe: 'https://example.com' },
      { Script: 'javascript:alert(1)' },
      { Secret: 'client://open?key={key}' },
    ])
    expect(parsed.map((preset) => preset.name)).toEqual(['Safe'])
    expect(parsed[0]?.id).toBe('0')
  })

  test('filters unknown placeholders and oversized configs', () => {
    expect(
      parseChatConfig([
        { Safe: 'https://ok.test' },
        { Bad: 'client://open?value={unknown}' },
      ]).map((preset) => preset.id)
    ).toEqual(['0'])
    expect(parseChatConfig('x'.repeat(512 * 1024 + 1))).toEqual([])
  })
})

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
// ============================================================================
// OAuth URL Builders
// ============================================================================

export interface CustomOAuthBinding {
  provider_id: number
  provider_name: string
  provider_slug: string
  provider_icon: string
  /** Returned for the current user's profile only; admin responses redact it. */
  provider_user_id?: string
  /** Redacted admin responses expose binding state instead of the identity ID. */
  is_bound?: boolean
}

export function indexCustomOAuthBindings(
  bindings: CustomOAuthBinding[]
): Map<number, CustomOAuthBinding> {
  return new Map(bindings.map((binding) => [binding.provider_id, binding]))
}

/**
 * Build GitHub OAuth URL
 */
export function buildGitHubOAuthUrl(
  clientId: string,
  state: string,
  codeChallenge?: string,
  redirectURI?: string
): string {
  const url = new URL('https://github.com/login/oauth/authorize')
  url.searchParams.set('client_id', clientId)
  url.searchParams.set('state', state)
  url.searchParams.set('scope', 'user:email')
  if (redirectURI) url.searchParams.set('redirect_uri', redirectURI)
  if (codeChallenge) {
    url.searchParams.set('code_challenge', codeChallenge)
    url.searchParams.set('code_challenge_method', 'S256')
  }
  return url.toString()
}

/**
 * Build Discord OAuth URL
 */
export function buildDiscordOAuthUrl(
  clientId: string,
  state: string,
  codeChallenge?: string,
  redirectURI?: string
): string {
  const url = new URL('https://discord.com/oauth2/authorize')
  url.searchParams.set('client_id', clientId)
  url.searchParams.set(
    'redirect_uri',
    redirectURI || `${window.location.origin}/oauth/discord`
  )
  url.searchParams.set('response_type', 'code')
  url.searchParams.set('scope', 'identify+openid')
  url.searchParams.set('state', state)
  if (codeChallenge) {
    url.searchParams.set('code_challenge', codeChallenge)
    url.searchParams.set('code_challenge_method', 'S256')
  }
  return url.toString()
}

/**
 * Build OIDC OAuth URL
 */
export function buildOIDCOAuthUrl(
  authUrl: string,
  clientId: string,
  state: string,
  codeChallenge?: string,
  redirectURI?: string
): string {
  const url = new URL(authUrl)
  url.searchParams.set('client_id', clientId)
  url.searchParams.set(
    'redirect_uri',
    redirectURI || `${window.location.origin}/oauth/oidc`
  )
  url.searchParams.set('response_type', 'code')
  url.searchParams.set('scope', 'openid profile email')
  url.searchParams.set('state', state)
  if (codeChallenge) {
    url.searchParams.set('code_challenge', codeChallenge)
    url.searchParams.set('code_challenge_method', 'S256')
  }
  return url.toString()
}

/**
 * Build LinuxDO OAuth URL
 */
export function buildLinuxDOOAuthUrl(
  clientId: string,
  state: string,
  codeChallenge?: string,
  redirectURI?: string
): string {
  const url = new URL('https://connect.linux.do/oauth2/authorize')
  url.searchParams.set('response_type', 'code')
  url.searchParams.set('client_id', clientId)
  url.searchParams.set(
    'redirect_uri',
    redirectURI || `${window.location.origin}/oauth/linuxdo`
  )
  url.searchParams.set('state', state)
  if (codeChallenge) {
    url.searchParams.set('code_challenge', codeChallenge)
    url.searchParams.set('code_challenge_method', 'S256')
  }
  return url.toString()
}

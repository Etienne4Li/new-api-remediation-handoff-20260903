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
import axios from 'axios'

import { api, refreshAuthentication, type RefreshOutcome } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { getAffiliateCode } from './lib/storage'
import type { TelegramAuthorization } from './lib/telegram-login'
import type {
  LoginPayload,
  LoginResponse,
  Login2FAResponse,
  TwoFAPayload,
  RegisterPayload,
  ApiResponse,
  OAuthFlowStart,
} from './types'

// ============================================================================
// Authentication APIs
// ============================================================================

// ----------------------------------------------------------------------------
// Login & Logout
// ----------------------------------------------------------------------------

// User login with username and password
export async function login(payload: LoginPayload) {
  const turnstile = payload.turnstile ?? ''
  const res = await api.post<LoginResponse>(
    '/api/user/login',
    {
      username: payload.username,
      password: payload.password,
    },
    { params: { turnstile }, skipAuthRefresh: true }
  )
  return res.data
}

// Two-factor authentication login
export async function login2fa(payload: TwoFAPayload) {
  const res = await api.post<Login2FAResponse>('/api/user/login/2fa', payload, {
    skipAuthRefresh: true,
  })
  return res.data
}

interface LogoutRuntime {
  getExpectedSID: () => string | undefined
  request: (expectedSID?: string, signal?: AbortSignal) => Promise<ApiResponse>
  refresh: (signal?: AbortSignal) => Promise<RefreshOutcome>
}

export async function executeLogout(
  runtime: LogoutRuntime,
  allowMismatchRecovery = true,
  signal?: AbortSignal
): Promise<ApiResponse> {
  if (signal?.aborted) throw signal.reason

  try {
    const response = await runtime.request(runtime.getExpectedSID(), signal)
    if (signal?.aborted) throw signal.reason
    return response
  } catch (error: unknown) {
    if (signal?.aborted) throw signal.reason

    const code = axios.isAxiosError(error)
      ? error.response?.data?.code
      : undefined
    if (
      allowMismatchRecovery &&
      axios.isAxiosError(error) &&
      error.response?.status === 409 &&
      code === 'AUTH_SESSION_MISMATCH'
    ) {
      const outcome = await runtime.refresh(signal)
      if (signal?.aborted) throw signal.reason
      if (outcome.kind === 'authenticated') {
        return executeLogout(runtime, false, signal)
      }
      if (outcome.kind === 'anonymous') {
        return { success: true, message: '' }
      }
    }
    throw error
  }
}

// User logout
export async function logout(signal?: AbortSignal): Promise<ApiResponse> {
  return executeLogout(
    {
      getExpectedSID: () => useAuthStore.getState().auth.session?.sid,
      request: async (sid, requestSignal) => {
        const res = await api.post('/api/user/auth/logout', undefined, {
          headers: sid ? { 'X-Auth-Session': sid } : undefined,
          signal: requestSignal,
          skipAuthRefresh: true,
          skipErrorHandler: true,
        })
        return res.data
      },
      refresh: refreshAuthentication,
    },
    true,
    signal
  )
}

// ----------------------------------------------------------------------------
// Password Management
// ----------------------------------------------------------------------------

// Send password reset email
export async function sendPasswordResetEmail(
  email: string,
  turnstile?: string
): Promise<ApiResponse> {
  const res = await api.get('/api/reset_password', {
    params: { email, turnstile },
  })
  return res.data
}

// ----------------------------------------------------------------------------
// OAuth
// ----------------------------------------------------------------------------

// Start GitHub OAuth flow

// Get OAuth state for CSRF protection
export async function createOAuthFlow(
  provider: string,
  intent: 'login' | 'bind',
  signal?: AbortSignal
): Promise<OAuthFlowStart> {
  const aff = intent === 'login' ? getAffiliateCode() : ''
  const res = await api.post<ApiResponse<OAuthFlowStart | string>>(
    '/api/oauth/state',
    { provider, intent, aff: aff || undefined },
    {
      signal,
      skipAuthRefresh: intent === 'login',
      skipErrorHandler: true,
    }
  )
  if (res.data?.success && res.data.data && typeof res.data.data === 'object') {
    const data = res.data.data as Partial<OAuthFlowStart>
    if (
      typeof data.flow_token === 'string' &&
      typeof data.code_challenge === 'string' &&
      data.code_challenge.length > 0
    ) {
      return {
        flow_token: data.flow_token,
        code_challenge: data.code_challenge,
        code_challenge_method:
          data.code_challenge_method === 'S256'
            ? data.code_challenge_method
            : 'S256',
        redirect_uri:
          typeof data.redirect_uri === 'string' && data.redirect_uri.length > 0
            ? data.redirect_uri
            : undefined,
        expires_at:
          typeof data.expires_at === 'number' ? data.expires_at : undefined,
      }
    }
  }
  throw new Error(
    res.data?.message || 'OAuth PKCE challenge is missing; please retry'
  )
}

// WeChat login by authorization code
export async function wechatLoginByCode(code: string): Promise<ApiResponse> {
  const res = await api.get('/api/oauth/wechat', { params: { code } })
  return res.data
}

export async function telegramLogin(
  authorization: TelegramAuthorization
): Promise<ApiResponse> {
  const res = await api.get('/api/oauth/telegram/login', {
    params: authorization,
    disableDuplicate: true,
    skipAuthRefresh: true,
    skipBusinessError: true,
    skipErrorHandler: true,
  })
  return res.data
}

// ----------------------------------------------------------------------------
// Registration
// ----------------------------------------------------------------------------

// User registration
export async function register(payload: RegisterPayload): Promise<ApiResponse> {
  const res = await api.post(`/api/user/register`, payload, {
    params: { turnstile: payload.turnstile ?? '' },
  })
  return res.data
}

// Send email verification code
export async function sendEmailVerification(
  email: string,
  turnstile?: string
): Promise<ApiResponse> {
  const res = await api.get('/api/verification', {
    params: { email, turnstile },
  })
  return res.data
}

// Bind email to OAuth account

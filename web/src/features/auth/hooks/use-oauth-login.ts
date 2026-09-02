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
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { clearAuthentication, isAuthBundle } from '@/lib/api'

import { createOAuthFlow, logout, telegramLogin } from '../api'
import {
  buildGitHubOAuthUrl,
  buildDiscordOAuthUrl,
  buildOIDCOAuthUrl,
  buildLinuxDOOAuthUrl,
} from '../lib/oauth'
import { pickTelegramAuthorization } from '../lib/telegram-login'
import type { SystemStatus, CustomOAuthProviderInfo } from '../types'
import { useAuthRedirect } from './use-auth-redirect'

/**
 * Hook for managing OAuth login
 */
export function useOAuthLogin(
  status: SystemStatus | null,
  redirectTo?: string
) {
  const { t } = useTranslation()
  const { handleLoginSuccess } = useAuthRedirect()
  const [isLoading, setIsLoading] = useState(false)
  const [isTelegramDialogOpen, setIsTelegramDialogOpen] = useState(false)
  const [isTelegramPending, setIsTelegramPending] = useState(false)
  const [githubPhase, setGithubPhase] = useState<
    'idle' | 'redirecting' | 'timed-out'
  >('idle')
  const githubTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const githubAbortControllerRef = useRef<AbortController | null>(null)

  let githubButtonText = t('Continue with GitHub')
  if (githubPhase === 'redirecting') {
    githubButtonText = t('Redirecting to GitHub...')
  } else if (githubPhase === 'timed-out') {
    githubButtonText = t(
      'Request timed out, please refresh and restart GitHub login'
    )
  }
  const githubButtonDisabled = githubPhase !== 'idle'

  useEffect(() => {
    return () => {
      if (githubTimeoutRef.current) {
        clearTimeout(githubTimeoutRef.current)
      }
      githubAbortControllerRef.current?.abort()
    }
  }, [])

  const resetSession = async (signal?: AbortSignal) => {
    const response = await logout(signal)
    if (signal?.aborted) throw signal.reason
    if (!response.success) {
      throw new Error(response.message || t('Failed to sign out session'))
    }
    clearAuthentication()
  }

  const handleGitHubLogin = async () => {
    if (!status?.github_client_id) return
    if (githubPhase !== 'idle' || githubAbortControllerRef.current) return

    const abortController = new AbortController()
    githubAbortControllerRef.current = abortController

    setIsLoading(true)
    setGithubPhase('redirecting')

    if (githubTimeoutRef.current) {
      clearTimeout(githubTimeoutRef.current)
    }

    githubTimeoutRef.current = setTimeout(() => {
      if (githubAbortControllerRef.current !== abortController) return
      githubTimeoutRef.current = null
      abortController.abort()
      setIsLoading(false)
      setGithubPhase('timed-out')
    }, 20000)

    try {
      await resetSession(abortController.signal)
      if (abortController.signal.aborted) return
      const flow = await createOAuthFlow(
        'github',
        'login',
        abortController.signal
      )
      if (abortController.signal.aborted) return

      const url = buildGitHubOAuthUrl(
        status.github_client_id,
        flow.flow_token,
        flow.code_challenge,
        flow.redirect_uri
      )
      if (githubTimeoutRef.current) {
        clearTimeout(githubTimeoutRef.current)
        githubTimeoutRef.current = null
      }
      if (githubAbortControllerRef.current === abortController) {
        githubAbortControllerRef.current = null
      }
      window.open(url, '_self')
    } catch {
      if (abortController.signal.aborted) {
        if (githubAbortControllerRef.current === abortController) {
          githubAbortControllerRef.current = null
        }
        return
      }
      toast.error(t('Failed to start GitHub login'))
      if (githubTimeoutRef.current) {
        clearTimeout(githubTimeoutRef.current)
        githubTimeoutRef.current = null
      }
      if (githubAbortControllerRef.current === abortController) {
        githubAbortControllerRef.current = null
      }
      setIsLoading(false)
      setGithubPhase('idle')
    }
  }

  const handleDiscordLogin = async () => {
    if (!status?.discord_client_id) return

    setIsLoading(true)
    try {
      await resetSession()
      const flow = await createOAuthFlow('discord', 'login')

      const url = buildDiscordOAuthUrl(
        status.discord_client_id,
        flow.flow_token,
        flow.code_challenge,
        flow.redirect_uri
      )
      window.open(url, '_self')
    } catch {
      toast.error(t('Failed to start Discord login'))
    } finally {
      setIsLoading(false)
    }
  }

  const handleOIDCLogin = async () => {
    if (!status?.oidc_authorization_endpoint || !status?.oidc_client_id) return

    setIsLoading(true)
    try {
      await resetSession()
      const flow = await createOAuthFlow('oidc', 'login')

      const url = buildOIDCOAuthUrl(
        status.oidc_authorization_endpoint,
        status.oidc_client_id,
        flow.flow_token,
        flow.code_challenge,
        flow.redirect_uri
      )
      window.open(url, '_self')
    } catch {
      toast.error(t('Failed to start OIDC login'))
    } finally {
      setIsLoading(false)
    }
  }

  const handleLinuxDOLogin = async () => {
    if (!status?.linuxdo_client_id) return

    setIsLoading(true)
    try {
      await resetSession()
      const flow = await createOAuthFlow('linuxdo', 'login')

      const url = buildLinuxDOOAuthUrl(
        status.linuxdo_client_id,
        flow.flow_token,
        flow.code_challenge,
        flow.redirect_uri
      )
      window.open(url, '_self')
    } catch {
      toast.error(t('Failed to start LinuxDO login'))
    } finally {
      setIsLoading(false)
    }
  }

  const handleTelegramLogin = async () => {
    if (!status?.telegram_bot_name?.trim()) {
      toast.error(t('Login failed'))
      return
    }

    setIsLoading(true)
    try {
      await resetSession()
      setIsTelegramDialogOpen(true)
    } catch {
      toast.error(
        t('Failed to start {{provider}} login', { provider: 'Telegram' })
      )
    } finally {
      setIsLoading(false)
    }
  }

  const handleTelegramAuthorization = async (value: unknown) => {
    const authorization = pickTelegramAuthorization(value)
    if (!authorization) {
      toast.error(t('Login failed'))
      return
    }

    setIsTelegramPending(true)
    try {
      const response = await telegramLogin(authorization)
      if (!response.success || !isAuthBundle(response.data)) {
        toast.error(t('Login failed'))
        return
      }

      setIsTelegramDialogOpen(false)
      await handleLoginSuccess(response.data, redirectTo)
      toast.success(t('Welcome back!'))
    } catch {
      toast.error(t('Login failed'))
    } finally {
      setIsTelegramPending(false)
    }
  }

  const handleCustomOAuthLogin = async (provider: CustomOAuthProviderInfo) => {
    if (!provider.authorization_endpoint || !provider.client_id) return

    setIsLoading(true)
    try {
      await resetSession()
      const flow = await createOAuthFlow(provider.slug, 'login')

      const redirectUri =
        flow.redirect_uri || `${window.location.origin}/oauth/${provider.slug}`
      const url = new URL(provider.authorization_endpoint)
      url.searchParams.set('client_id', provider.client_id)
      url.searchParams.set('redirect_uri', redirectUri)
      url.searchParams.set('response_type', 'code')
      url.searchParams.set('state', flow.flow_token)
      url.searchParams.set('code_challenge', flow.code_challenge)
      url.searchParams.set('code_challenge_method', 'S256')
      if (provider.scopes) {
        url.searchParams.set('scope', provider.scopes)
      }

      window.open(url.toString(), '_self')
    } catch {
      toast.error(
        t('Failed to start {{provider}} login', { provider: provider.name })
      )
    } finally {
      setIsLoading(false)
    }
  }

  return {
    isLoading,
    githubButtonText,
    githubButtonDisabled,
    isTelegramDialogOpen,
    isTelegramPending,
    handleGitHubLogin,
    handleDiscordLogin,
    handleOIDCLogin,
    handleLinuxDOLogin,
    handleTelegramLogin,
    handleTelegramAuthorization,
    setIsTelegramDialogOpen,
    handleCustomOAuthLogin,
  }
}

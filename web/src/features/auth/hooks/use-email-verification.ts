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
import i18next from 'i18next'
import { useRef, useState } from 'react'
import { toast } from 'sonner'

import { useCountdown } from '@/hooks/use-countdown'

import { sendEmailVerification } from '../api'
import { EMAIL_VERIFICATION_COUNTDOWN } from '../constants'

interface UseEmailVerificationOptions {
  turnstileToken?: string
  validateTurnstile?: () => boolean
  /**
   * Called after local validation passes and immediately before the request
   * is dispatched, with the token being consumed. The caller should clear
   * the token (and remount the widget) here so a single token can never be
   * reused across send-code / register actions.
   */
  onTokenConsumed?: (token: string | undefined) => void
}

/**
 * Hook for managing email verification code sending
 */
export function useEmailVerification(options?: UseEmailVerificationOptions) {
  const [isSending, setIsSending] = useState(false)
  const isSendingRef = useRef(false)
  const {
    secondsLeft,
    isActive,
    start: startCountdown,
  } = useCountdown({ initialSeconds: EMAIL_VERIFICATION_COUNTDOWN })

  /**
   * Send verification code to email
   */
  const sendCode = async (email: string) => {
    if (!email) {
      toast.error(i18next.t('Please enter your email first'))
      return false
    }

    // Validate turnstile if validation function is provided
    if (options?.validateTurnstile && !options.validateTurnstile()) {
      return false
    }

    // Ref lock: guard against double-send within the same round even before
    // the disabled state propagates through a re-render.
    if (isSendingRef.current) {
      return false
    }
    isSendingRef.current = true

    setIsSending(true)
    try {
      // Capture the token before letting the caller reset the widget, then
      // consume it (reset) locally before the request so the same token can
      // never be reused by a later action (e.g. register). The callback is
      // invoked inside the try so the finally block always releases the ref
      // lock even if it throws.
      const capturedToken = options?.turnstileToken
      options?.onTokenConsumed?.(capturedToken)

      const res = await sendEmailVerification(email, capturedToken)
      if (res?.success) {
        startCountdown()
        toast.success(i18next.t('Verification email sent'))
        return true
      }
      toast.error(
        res?.message || i18next.t('Failed to send verification email')
      )
      return false
    } catch {
      // Errors are handled by global interceptor
      return false
    } finally {
      setIsSending(false)
      isSendingRef.current = false
    }
  }

  return {
    isSending,
    secondsLeft,
    isActive,
    sendCode,
  }
}

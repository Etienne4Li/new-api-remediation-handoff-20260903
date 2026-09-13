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
import { useState, useEffect, useCallback } from 'react'

import { getSelf } from '@/lib/api'

import type { UserWalletData } from '../types'

// ============================================================================
// Wallet User Hook
// ============================================================================

/**
 * Loads the signed-in user's wallet-facing fields (`quota`, `aff_quota`,
 * `aff_history_quota`, `aff_count`, ...) from `/api/user/self`.
 *
 * Shared by the wallet page and the referral page so both read the same
 * fields through the same request rather than keeping two copies of this
 * fetch-and-store dance.
 */
export function useWalletUser() {
  const [user, setUser] = useState<UserWalletData | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(false)

  const refetch = useCallback(async () => {
    try {
      setLoading(true)
      setError(false)
      const response = await getSelf()
      if (response.success && response.data) {
        setUser(response.data as UserWalletData)
      } else {
        setError(true)
      }
    } catch (err) {
      setError(true)
      // eslint-disable-next-line no-console
      console.error('Failed to fetch user data:', err)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- initial data load owns the loading state
    refetch()
  }, [refetch])

  return { user, loading, error, refetch }
}

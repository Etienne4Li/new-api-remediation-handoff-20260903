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
import { useCallback, useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'

import { getUserProfile, updateUserProfile, updateUserSettings } from '../api'
import type {
  UserProfile,
  UpdateUserRequest,
  UpdateUserSettingsRequest,
} from '../types'

// ============================================================================
// Profile Hook
// ============================================================================

export function useProfile() {
  const [profile, setProfile] = useState<UserProfile | null>(null)
  const [loading, setLoading] = useState(true)
  const [updating, setUpdating] = useState(false)
  const requestIdRef = useRef(0)
  const loadingRequestIdRef = useRef(0)

  // Fetch user profile (with optional silent mode)
  const fetchProfile = useCallback(async (silent = false) => {
    const requestId = ++requestIdRef.current
    try {
      if (!silent) {
        loadingRequestIdRef.current = requestId
        setLoading(true)
      }
      const response = await getUserProfile()

      if (requestId !== requestIdRef.current) return
      if (response.success && response.data) {
        setProfile(response.data)
      }
    } catch (error) {
      if (requestId !== requestIdRef.current) return
      // eslint-disable-next-line no-console
      console.error('Failed to fetch profile:', error)
      if (!silent) {
        toast.error(i18next.t('Failed to load profile'))
      }
    } finally {
      if (
        requestId === requestIdRef.current &&
        loadingRequestIdRef.current !== 0
      ) {
        loadingRequestIdRef.current = 0
        setLoading(false)
      }
    }
  }, [])

  // Refresh profile silently (without loading state)
  const refreshProfile = useCallback(async () => {
    await fetchProfile(true)
  }, [fetchProfile])

  // Update user profile
  const updateProfile = useCallback(
    async (data: UpdateUserRequest): Promise<boolean> => {
      try {
        setUpdating(true)
        const response = await updateUserProfile(data)

        if (response.success) {
          toast.success(i18next.t('Profile updated successfully'))
          await refreshProfile() // Refresh profile silently
          return true
        }

        toast.error(response.message || i18next.t('Failed to update profile'))
        return false
      } catch (error) {
        // eslint-disable-next-line no-console
        console.error('Failed to update profile:', error)
        toast.error(i18next.t('Failed to update profile'))
        return false
      } finally {
        setUpdating(false)
      }
    },
    [refreshProfile]
  )

  // Update user settings
  const updateSettings = useCallback(
    async (data: UpdateUserSettingsRequest): Promise<boolean> => {
      try {
        setUpdating(true)
        const response = await updateUserSettings(data)

        if (response.success) {
          toast.success(i18next.t('Settings updated successfully'))
          await refreshProfile() // Refresh profile silently
          return true
        }

        toast.error(response.message || i18next.t('Failed to update settings'))
        return false
      } catch (error) {
        // eslint-disable-next-line no-console
        console.error('Failed to update settings:', error)
        toast.error(i18next.t('Failed to update settings'))
        return false
      } finally {
        setUpdating(false)
      }
    },
    [refreshProfile]
  )

  // Initial fetch
  useEffect(() => {
    let cancelled = false
    const requestId = ++requestIdRef.current
    loadingRequestIdRef.current = requestId

    void getUserProfile()
      .then((response) => {
        if (cancelled || requestId !== requestIdRef.current) return
        if (response.success && response.data) {
          setProfile(response.data)
        }
        loadingRequestIdRef.current = 0
        setLoading(false)
      })
      .catch((error: unknown) => {
        if (cancelled || requestId !== requestIdRef.current) return
        // eslint-disable-next-line no-console
        console.error('Failed to fetch profile:', error)
        toast.error(i18next.t('Failed to load profile'))
        loadingRequestIdRef.current = 0
        setLoading(false)
      })

    return () => {
      cancelled = true
      if (requestId === requestIdRef.current) {
        requestIdRef.current += 1
      }
      if (loadingRequestIdRef.current === requestId) {
        loadingRequestIdRef.current = 0
      }
    }
  }, [])

  return {
    profile,
    loading,
    updating,
    fetchProfile,
    refreshProfile,
    updateProfile,
    updateSettings,
  }
}

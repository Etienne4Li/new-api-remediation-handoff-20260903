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
import { useState, useEffect, useCallback, useRef } from 'react'

import { getAffRebates, isApiSuccess } from '../api'
import type { AffRebateRecord } from '../types'

// ============================================================================
// Affiliate Rebate Hook
// ============================================================================

interface UseAffRebatesOptions {
  /** Initial page number */
  initialPage?: number
  /** Initial page size */
  initialPageSize?: number
}

export function useAffRebates(options: UseAffRebatesOptions = {}) {
  const { initialPage = 1, initialPageSize = 5 } = options

  const [records, setRecords] = useState<AffRebateRecord[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(initialPage)
  const [pageSize] = useState(initialPageSize)
  // The rebate feature stays invisible until the backend says it is on, so the
  // card renders nothing rather than an empty promise while loading.
  const [enabled, setEnabled] = useState(false)
  const [percent, setPercent] = useState(0)
  const [maxTimes, setMaxTimes] = useState(0)
  const [loading, setLoading] = useState(true)
  const requestIdRef = useRef(0)

  const fetchRebates = useCallback(async () => {
    const requestId = ++requestIdRef.current
    setLoading(true)
    try {
      const response = await getAffRebates(page, pageSize)
      if (requestId !== requestIdRef.current) return

      if (isApiSuccess(response) && response.data) {
        setEnabled(response.data.enabled)
        setPercent(response.data.percent)
        setMaxTimes(response.data.max_times)
        setRecords(response.data.items ?? [])
        setTotal(response.data.total ?? 0)
      }
    } catch (error) {
      if (requestId !== requestIdRef.current) return
      // eslint-disable-next-line no-console
      console.error('Failed to fetch affiliate rebates:', error)
    } finally {
      if (requestId === requestIdRef.current) {
        setLoading(false)
      }
    }
  }, [page, pageSize])

  useEffect(() => {
    fetchRebates()
  }, [fetchRebates])

  return {
    records,
    total,
    page,
    pageSize,
    enabled,
    percent,
    maxTimes,
    loading,
    setPage,
    refetch: fetchRebates,
  }
}

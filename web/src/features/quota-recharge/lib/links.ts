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
/**
 * Return a configured HTTPS URL, or null for an absent/unsafe value.
 *
 * The value comes from an administrator-controlled option, but it still
 * crosses a browser navigation boundary. Restricting it here prevents
 * javascript:, data:, and cleartext HTTP URLs from reaching an iframe or an
 * external-link fallback.
 */
export function resolveQuotaRechargeLink(value: unknown): string | null {
  if (typeof value !== 'string') return null

  const trimmed = value.trim()
  try {
    const url = new URL(trimmed)
    return url.protocol === 'https:' ? trimmed : null
  } catch {
    return null
  }
}

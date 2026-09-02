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
 * Application-wide constants
 */

// Presentation defaults. The backend keeps its upstream project identity, while
// the shipped interface uses the Lietio brand until an administrator overrides it.
export const DEFAULT_SYSTEM_NAME = 'Lietio'
export const DEFAULT_DOCUMENT_TITLE = 'Lietio'
export const DEFAULT_LOGO = '/lietio-mark.svg'

const PRESENTATION_NAME_ALIASES = new Set(['New API', 'NewAPI', 'Lietio API'])
const UPSTREAM_DEFAULT_LOGOS = new Set(['/logo.png'])

export function resolveSystemName(value?: string): string {
  const configuredName = typeof value === 'string' ? value.trim() : ''
  return !configuredName || PRESENTATION_NAME_ALIASES.has(configuredName)
    ? DEFAULT_SYSTEM_NAME
    : configuredName
}

export function resolveSystemLogo(value?: string): string {
  const configuredLogo = typeof value === 'string' ? value.trim() : ''
  return !configuredLogo || UPSTREAM_DEFAULT_LOGOS.has(configuredLogo)
    ? DEFAULT_LOGO
    : configuredLogo
}

// LocalStorage Keys

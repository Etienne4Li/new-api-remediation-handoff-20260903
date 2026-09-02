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
export interface DevtoolsEnvironment {
  MODE?: string
  VITE_ENABLE_DEVTOOLS?: string
}

/**
 * Keep developer overlays opt-in so a normal development preview remains
 * usable on small screens. The exact string check avoids accidental exposure
 * from values such as `1`, `TRUE`, or a trailing space.
 */
export function isDevtoolsEnabled(env: DevtoolsEnvironment): boolean {
  return env.MODE === 'development' && env.VITE_ENABLE_DEVTOOLS === 'true'
}

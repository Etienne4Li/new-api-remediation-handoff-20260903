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
import type React from 'react'

/**
 * A data-table filter option can carry either an i18n key or final display
 * text. `labelKey` is preferred for new callers; `translateLabel` is the
 * escape hatch for dynamic labels that must never be looked up.
 */
export type DataTableFilterOption = {
  label: string
  value: string
  labelKey?: string
  translateLabel?: boolean
  icon?: React.ComponentType<{ className?: string }>
  iconNode?: React.ReactNode
  count?: number
}

export type DataTableFilterTranslator = (key: string) => string

/**
 * Resolve a filter label without translating an already-localized value.
 * The legacy `exists` fallback keeps older filter definitions compatible while
 * new definitions can use the explicit `labelKey` / `translateLabel` fields.
 */
export function resolveDataTableFilterLabel(
  option: Pick<DataTableFilterOption, 'label' | 'labelKey' | 'translateLabel'>,
  translate: DataTableFilterTranslator,
  exists: (key: string) => boolean
): string {
  if (option.labelKey) return translate(option.labelKey)
  if (option.translateLabel === false) return option.label
  return exists(option.label) ? translate(option.label) : option.label
}

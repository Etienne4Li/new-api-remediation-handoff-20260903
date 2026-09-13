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
import type { TopupBonusTier, TopupInfo } from '../types'

// ============================================================================
// Top-up Bonus
// ============================================================================
//
// The backend already filters out unusable tiers before sending them, so these
// helpers only have to mirror its tier *selection*: take the highest threshold
// the amount reaches and apply that ratio to the amount. Keeping the two in
// step is what stops the card from advertising a bonus that never arrives.

/**
 * Read the bonus tiers out of a topup info response, newest configuration
 * shape first. Returns an empty list when the promotion is switched off or
 * nothing usable is configured, which is the single signal the UI uses to hide
 * every part of the promotion.
 */
export function getTopupBonusTiers(
  topupInfo: TopupInfo | null | undefined
): TopupBonusTier[] {
  if (!topupInfo?.topup_bonus_enabled || !topupInfo.topup_bonus) {
    return []
  }

  return Object.entries(topupInfo.topup_bonus)
    .map(([threshold, ratio]) => ({
      threshold: Number(threshold),
      ratio: Number(ratio),
    }))
    .filter(
      (tier) =>
        Number.isFinite(tier.threshold) &&
        Number.isFinite(tier.ratio) &&
        tier.threshold > 0 &&
        tier.ratio > 0
    )
    .sort((a, b) => a.threshold - b.threshold)
}

/**
 * Ratio that applies to a top-up of the given amount: the one configured for
 * the highest threshold the amount reaches. Thresholds are inclusive, so
 * exactly 100 already earns the 100 tier. Returns 0 when nothing qualifies.
 */
export function getTopupBonusRatio(
  tiers: TopupBonusTier[],
  amount: number
): number {
  if (!Number.isFinite(amount) || amount <= 0) {
    return 0
  }

  return tiers.reduce(
    (ratio, tier) => (amount >= tier.threshold ? tier.ratio : ratio),
    0
  )
}

/**
 * Extra balance a top-up of the given amount earns, in the same display unit
 * as the amount itself. Returns 0 when the top-up qualifies for no tier.
 */
export function getTopupBonusAmount(
  tiers: TopupBonusTier[],
  amount: number
): number {
  const ratio = getTopupBonusRatio(tiers, amount)
  return ratio > 0 ? amount * ratio : 0
}

/**
 * Render a tier's ratio as a percentage for the ladder labels, trimming the
 * trailing zero so 1.5% stays "1.5" and 2% does not become "2.0".
 */
export function formatTopupBonusPercent(ratio: number): string {
  return Number.parseFloat((ratio * 100).toFixed(2)).toString()
}

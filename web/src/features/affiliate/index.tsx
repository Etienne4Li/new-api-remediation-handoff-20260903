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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { TransferDialog } from '@/features/wallet/components/dialogs/transfer-dialog'
import {
  useAffiliate,
  useAffRebates,
  useTopupInfo,
  useWalletUser,
} from '@/features/wallet/hooks'

import { RebateHistoryCard } from './components/rebate-history-card'
import { ReferralLinkCard } from './components/referral-link-card'
import { ReferralRulesCard } from './components/referral-rules-card'
import { ReferralStatsCards } from './components/referral-stats-cards'

/** The rebate ledger has a page to itself here, so it shows more than the
 *  wallet entry bar's old five rows. */
const REBATE_PAGE_SIZE = 10

/**
 * Standalone referral page: invite link, earnings, rules and the rebate
 * ledger.
 *
 * The link, the three totals and the transfer action predate the rebate
 * feature and are always shown. Only the rules and the ledger follow
 * `AffRebateEnabled`, which the backend reports through `useAffRebates`.
 */
export function Affiliate() {
  const { t } = useTranslation()
  const [transferDialogOpen, setTransferDialogOpen] = useState(false)

  const { user, loading: userLoading, refetch: refetchUser } = useWalletUser()
  const {
    affiliateLink,
    loading: affiliateLoading,
    transferQuota,
    transferring,
  } = useAffiliate()
  const { topupInfo } = useTopupInfo()
  const {
    records: rebates,
    total,
    page,
    pageSize,
    enabled,
    percent,
    maxTimes,
    loading: rebatesLoading,
    setPage,
    refetch: refetchRebates,
  } = useAffRebates({ initialPageSize: REBATE_PAGE_SIZE })

  const handleTransfer = async (amount: number) => {
    const success = await transferQuota(amount)
    if (success) {
      // The transfer moves quota out of `aff_quota` and into the balance, so
      // both the totals and the ledger need re-reading.
      await refetchUser()
      await refetchRebates()
    }
    return success
  }

  return (
    <>
      <SectionPageLayout>
        <SectionPageLayout.Title>
          {t('Referral Program')}
        </SectionPageLayout.Title>
        <SectionPageLayout.Description>
          {enabled
            ? t(
                'Friends who sign up with your link earn you a {{percent}}% rebate on each of their first {{maxTimes}} top-ups, transferable to your balance anytime.',
                { percent, maxTimes }
              )
            : t(
                'Earn rewards when users join through your referral link. Transfer accumulated rewards to your balance anytime.'
              )}
        </SectionPageLayout.Description>
        <SectionPageLayout.Content>
          <div className='mx-auto flex w-full max-w-5xl flex-col gap-4'>
            <ReferralLinkCard
              affiliateLink={affiliateLink}
              loading={affiliateLoading}
            />

            <ReferralStatsCards
              user={user}
              loading={userLoading}
              onTransfer={() => setTransferDialogOpen(true)}
              complianceConfirmed={
                topupInfo?.payment_compliance_confirmed !== false
              }
            />

            <ReferralRulesCard
              enabled={enabled}
              percent={percent}
              maxTimes={maxTimes}
            />

            {enabled && (
              <RebateHistoryCard
                rebates={rebates}
                maxTimes={maxTimes}
                loading={rebatesLoading}
                page={page}
                pageSize={pageSize}
                total={total}
                onPageChange={setPage}
              />
            )}
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>

      <TransferDialog
        open={transferDialogOpen}
        onOpenChange={setTransferDialogOpen}
        onConfirm={handleTransfer}
        availableQuota={user?.aff_quota ?? 0}
        transferring={transferring}
      />
    </>
  )
}

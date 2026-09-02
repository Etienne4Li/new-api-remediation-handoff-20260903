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
import { useQuery } from '@tanstack/react-query'
import { AlertCircle, ExternalLink, RefreshCw, WalletCards } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { getTopupInfo } from '@/features/wallet/api'

import { resolveQuotaRechargeLink } from './lib/links'

const TOPUP_INFO_QUERY_KEY = ['quota-recharge', 'topup-info'] as const

function QuotaRechargeLoading() {
  const { t } = useTranslation()

  return (
    <div
      className='flex h-full min-h-[28rem] flex-col gap-3'
      role='status'
      aria-busy='true'
      aria-label={t('Loading...')}
    >
      <Skeleton className='h-12 w-full rounded-lg' />
      <Skeleton className='min-h-[28rem] flex-1 rounded-lg' />
    </div>
  )
}

function QuotaRechargeUnavailable(props: {
  message: string
  onRetry: () => void
  isRetrying: boolean
}) {
  const { t } = useTranslation()

  return (
    <div className='flex min-h-[28rem] flex-1 items-center justify-center p-6'>
      <div className='flex max-w-md flex-col items-center gap-3 text-center'>
        <AlertCircle
          className='text-muted-foreground size-9'
          aria-hidden='true'
        />
        <div className='space-y-1'>
          <h3 className='text-base font-semibold'>{t('Quota Recharge')}</h3>
          <p className='text-muted-foreground text-sm'>{props.message}</p>
        </div>
        <Button
          variant='outline'
          onClick={props.onRetry}
          disabled={props.isRetrying}
        >
          <RefreshCw
            className={props.isRetrying ? 'animate-spin' : undefined}
          />
          {t('Retry')}
        </Button>
      </div>
    </div>
  )
}

/**
 * Authenticated storefront view for the administrator-configured top-up
 * link. The iframe is intentionally sandboxed and never receives New API
 * credentials; the explicit new-tab action remains available when a store's
 * CSP/X-Frame-Options policy prevents embedding.
 */
export function QuotaRecharge() {
  const { t } = useTranslation()
  const topupQuery = useQuery({
    queryKey: TOPUP_INFO_QUERY_KEY,
    queryFn: getTopupInfo,
    staleTime: 5 * 60 * 1000,
  })

  const rawLink = topupQuery.data?.data?.topup_link
  const rechargeLink = resolveQuotaRechargeLink(rawLink)
  const hasConfiguredValue =
    typeof rawLink === 'string' && rawLink.trim() !== ''
  const responseUnavailable =
    topupQuery.isError ||
    topupQuery.data?.success === false ||
    (!topupQuery.isLoading && !topupQuery.data)

  let content: ReactNode
  if (topupQuery.isLoading) {
    content = <QuotaRechargeLoading />
  } else if (responseUnavailable) {
    content = (
      <QuotaRechargeUnavailable
        message={
          topupQuery.data?.message ||
          t('Unable to load quota recharge settings.')
        }
        onRetry={() => void topupQuery.refetch()}
        isRetrying={topupQuery.isFetching}
      />
    )
  } else if (!rechargeLink) {
    content = (
      <QuotaRechargeUnavailable
        message={
          hasConfiguredValue
            ? t('The configured quota recharge link is not valid.')
            : t('No quota recharge link is configured.')
        }
        onRetry={() => void topupQuery.refetch()}
        isRetrying={topupQuery.isFetching}
      />
    )
  } else {
    content = (
      <div className='flex h-full min-h-0 flex-col gap-3'>
        <Alert>
          <AlertCircle aria-hidden='true' />
          <AlertTitle>{t('Quota Recharge')}</AlertTitle>
          <AlertDescription>
            {t(
              'If the embedded page is blocked, use the button above to open it in a new tab.'
            )}
          </AlertDescription>
        </Alert>
        <div className='bg-background min-h-0 flex-1 overflow-hidden rounded-lg border'>
          <iframe
            // Keep the administrator's URL byte-for-byte intact. The
            // configured CatFK storefront rejects query strings, so adding
            // embed/theme context would make the frame fail to load.
            src={rechargeLink}
            title={t('Quota Recharge')}
            className='h-full min-h-[28rem] w-full border-0'
            loading='eager'
            referrerPolicy='no-referrer'
            allow='payment; clipboard-write'
            // The storefront is a separate origin. Keeping its origin lets
            // its own API calls and session cookie work inside the frame;
            // the cross-origin boundary still prevents access to newapi.
            // eslint-disable-next-line react/iframe-missing-sandbox -- required for the cross-origin storefront session
            sandbox='allow-forms allow-popups allow-popups-to-escape-sandbox allow-same-origin allow-scripts allow-top-navigation-by-user-activation'
          />
        </div>
      </div>
    )
  }

  return (
    <SectionPageLayout fixedContent variant='editorial' density='compact'>
      <SectionPageLayout.Title>
        <span className='inline-flex min-w-0 items-center gap-2'>
          <WalletCards className='size-5 shrink-0' aria-hidden='true' />
          <span className='truncate'>{t('Quota Recharge')}</span>
        </span>
      </SectionPageLayout.Title>
      <SectionPageLayout.Description>
        {t('Purchase quota codes from the configured store.')}
      </SectionPageLayout.Description>
      <SectionPageLayout.Actions>
        {rechargeLink && (
          <Button
            variant='outline'
            render={
              <a
                href={rechargeLink}
                target='_blank'
                rel='noopener noreferrer'
              />
            }
          >
            <ExternalLink aria-hidden='true' />
            {t('Open in new tab')}
          </Button>
        )}
      </SectionPageLayout.Actions>
      <SectionPageLayout.Content>{content}</SectionPageLayout.Content>
    </SectionPageLayout>
  )
}

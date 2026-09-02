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
import {
  Activity,
  AlertCircle,
  Layers3,
  Plus,
  Receipt,
  RefreshCw,
  ShieldCheck,
} from 'lucide-react'
import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import {
  Alert,
  AlertAction,
  AlertDescription,
  AlertTitle,
} from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { useStatus } from '@/hooks/use-status'
import { useSystemConfig } from '@/hooks/use-system-config'
import { getSelf } from '@/lib/api'
import { formatQuota } from '@/lib/format'

import { AffiliateRewardsCard } from './components/affiliate-rewards-card'
import { BillingHistoryDialog } from './components/dialogs/billing-history-dialog'
import { CreemConfirmDialog } from './components/dialogs/creem-confirm-dialog'
import { PaymentConfirmDialog } from './components/dialogs/payment-confirm-dialog'
import { TransferDialog } from './components/dialogs/transfer-dialog'
import { RechargeFormCard } from './components/recharge-form-card'
import { SubscriptionPlansCard } from './components/subscription-plans-card'
import { WalletStatsCard } from './components/wallet-stats-card'
import { DEFAULT_DISCOUNT_RATE, PAYMENT_TYPES } from './constants'
import {
  useTopupInfo,
  usePayment,
  useAffiliate,
  useRedemption,
  useCreemPayment,
  useWaffoPayment,
  useWaffoPancakePayment,
} from './hooks'
import {
  getDefaultPaymentType,
  getMinTopupAmount,
  dispatchSelectedPayment,
} from './lib'
import type {
  UserWalletData,
  PaymentMethod,
  PresetAmount,
  CreemProduct,
  WaffoPayMethod,
  TopupInfo,
} from './types'

interface WalletProps {
  initialShowHistory?: boolean
}

function WalletSignalStrip(props: {
  user: UserWalletData | null
  topupInfo: TopupInfo | null
  loading: boolean
}) {
  const { t } = useTranslation()
  const paymentMethodCount =
    (props.topupInfo?.pay_methods?.length ?? 0) +
    (props.topupInfo?.waffo_pay_methods?.length ?? 0) +
    (props.topupInfo?.enable_creem_topup ? 1 : 0)
  const signals = [
    {
      label: t('Current Balance'),
      value: props.loading
        ? t('Loading...')
        : formatQuota(props.user?.quota ?? 0),
      icon: Activity,
      tone: 'text-success',
    },
    {
      label: t('API Requests'),
      value: props.loading
        ? t('Loading...')
        : Number(props.user?.request_count ?? 0).toLocaleString(),
      icon: Layers3,
      tone: 'text-info',
    },
    {
      label: t('Group'),
      value: props.loading ? t('Loading...') : props.user?.group || 'default',
      icon: ShieldCheck,
      tone: 'text-warning',
    },
    {
      label: t('Payment Method'),
      value: props.loading ? t('Loading...') : String(paymentMethodCount),
      icon: Receipt,
      tone: 'text-primary',
    },
  ]

  return (
    <section
      aria-label={t('Billing')}
      className='border-border/80 bg-muted/10 overflow-hidden rounded-lg border'
    >
      <dl className='bg-border grid grid-cols-2 gap-px sm:grid-cols-4'>
        {signals.map((signal) => (
          <div
            key={signal.label}
            className='bg-background/90 flex min-w-0 items-center gap-2.5 px-3 py-2.5 sm:px-4 sm:py-3'
          >
            <signal.icon
              className={`size-4 shrink-0 ${signal.tone}`}
              aria-hidden='true'
            />
            <div className='min-w-0'>
              <dt className='text-muted-foreground truncate text-[10px] font-medium uppercase'>
                {signal.label}
              </dt>
              <dd className='text-foreground mt-0.5 truncate font-mono text-sm font-semibold tabular-nums'>
                {signal.value}
              </dd>
            </div>
          </div>
        ))}
      </dl>
    </section>
  )
}

export function Wallet(props: WalletProps) {
  const { t } = useTranslation()
  const [user, setUser] = useState<UserWalletData | null>(null)
  const [userLoading, setUserLoading] = useState(true)
  const [userError, setUserError] = useState(false)
  const [topupAmount, setTopupAmount] = useState(0)
  const [selectedPreset, setSelectedPreset] = useState<number | null>(null)
  const [selectedPaymentMethod, setSelectedPaymentMethod] =
    useState<PaymentMethod>()
  const [selectedWaffoMethodIndex, setSelectedWaffoMethodIndex] = useState<
    number | null
  >(null)
  const [paymentLoading, setPaymentLoading] = useState<string | null>(null)
  const [confirmDialogOpen, setConfirmDialogOpen] = useState(false)
  const [transferDialogOpen, setTransferDialogOpen] = useState(false)
  const [billingDialogOpen, setBillingDialogOpen] = useState(false)
  const [redemptionCode, setRedemptionCode] = useState('')
  const [creemDialogOpen, setCreemDialogOpen] = useState(false)
  const [selectedCreemProduct, setSelectedCreemProduct] =
    useState<CreemProduct | null>(null)
  const [showSubscriptionPanel, setShowSubscriptionPanel] = useState(true)
  const addFundsRef = useRef<HTMLDivElement>(null)

  const { status } = useStatus()
  const { currency } = useSystemConfig()
  const {
    topupInfo,
    presetAmounts,
    loading: topupLoading,
    error: topupError,
    refetch: refetchTopupInfo,
  } = useTopupInfo()

  // Calculate effective exchange rate - when display type is USD, use rate of 1
  const effectiveUsdExchangeRate = useMemo(() => {
    return currency?.quotaDisplayType === 'USD'
      ? 1
      : currency?.usdExchangeRate || 1
  }, [currency?.quotaDisplayType, currency?.usdExchangeRate])
  const {
    amount: paymentAmount,
    calculating,
    processing,
    calculatePaymentAmount,
    processPayment,
  } = usePayment()
  const {
    affiliateLink,
    loading: affiliateLoading,
    transferQuota,
    transferring,
  } = useAffiliate()
  const { redeeming, redeemCode } = useRedemption()
  const { processing: creemProcessing, processCreemPayment } = useCreemPayment()
  const { processing: waffoProcessing, processWaffoPayment } = useWaffoPayment()
  const { processing: pancakeProcessing, processWaffoPancakePayment } =
    useWaffoPancakePayment()

  // Fetch and refresh user data
  const fetchUser = useCallback(async () => {
    try {
      setUserLoading(true)
      setUserError(false)
      const response = await getSelf()
      if (response.success && response.data) {
        setUser(response.data as UserWalletData)
      } else {
        setUserError(true)
      }
    } catch (error) {
      setUserError(true)
      // eslint-disable-next-line no-console
      console.error('Failed to fetch user data:', error)
    } finally {
      setUserLoading(false)
    }
  }, [])

  const retryWalletData = useCallback(() => {
    if (userError) void fetchUser()
    if (topupError) void refetchTopupInfo()
  }, [fetchUser, refetchTopupInfo, topupError, userError])

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- initial data load owns the loading state
    fetchUser()
  }, [fetchUser])

  useEffect(() => {
    if (props.initialShowHistory) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- route state intentionally opens the dialog on mount
      setBillingDialogOpen(true)
      window.history.replaceState({}, '', window.location.pathname)
    }
  }, [props.initialShowHistory])

  // Initialize topup amount when topup info is loaded
  const topupAmountInitializedRef = useRef(false)
  useEffect(() => {
    if (topupInfo && !topupAmountInitializedRef.current) {
      topupAmountInitializedRef.current = true
      const minTopup = getMinTopupAmount(topupInfo)
      setTopupAmount(minTopup)

      // Calculate initial payment amount with default payment type
      const defaultPaymentType = getDefaultPaymentType(topupInfo)
      calculatePaymentAmount(minTopup, defaultPaymentType)
    }
  }, [topupInfo, calculatePaymentAmount])

  // Get current payment type (selected or default)
  const getCurrentPaymentType = useCallback(() => {
    return selectedPaymentMethod?.type || getDefaultPaymentType(topupInfo)
  }, [selectedPaymentMethod, topupInfo])

  // Handle preset selection
  const handleSelectPreset = (preset: PresetAmount) => {
    setTopupAmount(preset.value)
    setSelectedPreset(preset.value)
    calculatePaymentAmount(preset.value, getCurrentPaymentType())
  }

  // Handle topup amount change
  const handleTopupAmountChange = (amount: number) => {
    setTopupAmount(amount)
    setSelectedPreset(null)
    calculatePaymentAmount(amount, getCurrentPaymentType())
  }

  // Handle payment method selection
  const handlePaymentMethodSelect = async (method: PaymentMethod) => {
    setSelectedPaymentMethod(method)
    setSelectedWaffoMethodIndex(null)
    setPaymentLoading(method.type)

    try {
      // Validate minimum topup
      const minTopup = getMinTopupAmount(topupInfo)
      if (topupAmount < minTopup) {
        return
      }

      // Calculate payment amount and show confirmation dialog
      await calculatePaymentAmount(topupAmount, method.type)
      setConfirmDialogOpen(true)
    } finally {
      setPaymentLoading(null)
    }
  }

  // Handle payment confirmation
  const handlePaymentConfirm = async () => {
    if (!selectedPaymentMethod) return

    const success = await dispatchSelectedPayment(
      selectedPaymentMethod,
      topupAmount,
      selectedWaffoMethodIndex,
      {
        regular: processPayment,
        waffo: processWaffoPayment,
        waffoPancake: processWaffoPancakePayment,
      }
    )

    if (success) {
      setConfirmDialogOpen(false)
      await fetchUser()
    }
  }

  // Handle redemption
  const handleRedeem = async () => {
    if (!redemptionCode) return

    const success = await redeemCode(redemptionCode)
    if (success) {
      setRedemptionCode('')
      await fetchUser()
    }
  }

  // Handle transfer
  const handleTransfer = async (amount: number) => {
    const success = await transferQuota(amount)
    if (success) {
      await fetchUser()
    }
    return success
  }

  // Handle Creem product selection
  const handleCreemProductSelect = (product: CreemProduct) => {
    setSelectedCreemProduct(product)
    setCreemDialogOpen(true)
  }

  // Handle Creem payment confirmation
  const handleCreemConfirm = async () => {
    if (!selectedCreemProduct) return

    const success = await processCreemPayment(selectedCreemProduct.productId)
    if (success) {
      setCreemDialogOpen(false)
      setSelectedCreemProduct(null)
      await fetchUser()
    }
  }

  const handleWaffoMethodSelect = async (
    method: WaffoPayMethod,
    index: number
  ) => {
    const loadingKey = `waffo-${index}`
    setSelectedPaymentMethod({
      name: method.name,
      type: PAYMENT_TYPES.WAFFO,
      icon: method.icon,
    })
    setSelectedWaffoMethodIndex(index)
    setPaymentLoading(loadingKey)

    try {
      await calculatePaymentAmount(topupAmount, PAYMENT_TYPES.WAFFO)
      setConfirmDialogOpen(true)
    } finally {
      setPaymentLoading(null)
    }
  }

  // Get discount rate for current topup amount
  const getDiscountRate = useCallback(() => {
    return topupInfo?.discount?.[topupAmount] || DEFAULT_DISCOUNT_RATE
  }, [topupInfo, topupAmount])

  const handleSubscriptionAvailabilityChange = useCallback(
    (available: boolean) => {
      setShowSubscriptionPanel(available)
    },
    []
  )

  const handleShowAddFunds = useCallback(() => {
    const addFunds = addFundsRef.current
    if (!addFunds) return

    addFunds.scrollIntoView({ behavior: 'smooth', block: 'start' })
    window.requestAnimationFrame(() => {
      const amountInput = addFunds.querySelector<HTMLInputElement>(
        '#topup-amount:not(:disabled)'
      )
      if (amountInput) {
        amountInput.focus({ preventScroll: true })
      } else {
        addFunds.focus({ preventScroll: true })
      }
    })
  }, [])

  return (
    <>
      <SectionPageLayout>
        <SectionPageLayout.Title>{t('Wallet')}</SectionPageLayout.Title>
        <SectionPageLayout.Description>
          {t('Top up balance and view billing history.')}
        </SectionPageLayout.Description>
        <SectionPageLayout.Actions>
          <Button
            type='button'
            size='sm'
            variant='outline'
            onClick={() => setBillingDialogOpen(true)}
          >
            <Receipt data-icon='inline-start' />
            {t('Order History')}
          </Button>
          <Button type='button' size='sm' onClick={handleShowAddFunds}>
            <Plus data-icon='inline-start' />
            {t('Add Funds')}
          </Button>
        </SectionPageLayout.Actions>
        <SectionPageLayout.FeatureStrip>
          <div className='mt-4'>
            <WalletSignalStrip
              user={user}
              topupInfo={topupInfo}
              loading={userLoading || topupLoading}
            />
          </div>
        </SectionPageLayout.FeatureStrip>
        <SectionPageLayout.Content>
          <div className='mx-auto flex w-full max-w-7xl flex-col gap-5'>
            {(userError || topupError) && (
              <Alert variant='destructive'>
                <AlertCircle aria-hidden='true' />
                <AlertTitle>{t('Unable to load wallet data')}</AlertTitle>
                <AlertDescription>
                  {t(
                    'Some billing data could not be loaded. Retry before making a payment.'
                  )}
                </AlertDescription>
                <AlertAction>
                  <Button
                    type='button'
                    variant='outline'
                    size='sm'
                    onClick={retryWalletData}
                    disabled={userLoading || topupLoading}
                  >
                    <RefreshCw data-icon='inline-start' />
                    {t('Retry')}
                  </Button>
                </AlertAction>
              </Alert>
            )}

            <WalletStatsCard user={user} loading={userLoading} />

            <div
              className={
                showSubscriptionPanel
                  ? 'grid gap-4 xl:grid-cols-[minmax(0,1.05fr)_minmax(360px,0.95fr)] xl:items-start'
                  : 'grid gap-4'
              }
            >
              <div
                ref={addFundsRef}
                id='wallet-add-funds'
                tabIndex={-1}
                aria-label={t('Add Funds')}
                className='scroll-mt-4 outline-none'
              >
                <RechargeFormCard
                  topupInfo={topupInfo}
                  presetAmounts={presetAmounts}
                  selectedPreset={selectedPreset}
                  onSelectPreset={handleSelectPreset}
                  topupAmount={topupAmount}
                  onTopupAmountChange={handleTopupAmountChange}
                  paymentAmount={paymentAmount}
                  calculating={calculating}
                  onPaymentMethodSelect={handlePaymentMethodSelect}
                  paymentLoading={paymentLoading}
                  redemptionCode={redemptionCode}
                  onRedemptionCodeChange={setRedemptionCode}
                  onRedeem={handleRedeem}
                  redeeming={redeeming}
                  topupLink={topupInfo?.topup_link}
                  loading={topupLoading}
                  priceRatio={(status?.price as number) || 1}
                  usdExchangeRate={effectiveUsdExchangeRate}
                  creemProducts={topupInfo?.creem_products}
                  enableCreemTopup={topupInfo?.enable_creem_topup}
                  onCreemProductSelect={handleCreemProductSelect}
                  enableWaffoTopup={topupInfo?.enable_waffo_topup}
                  waffoPayMethods={topupInfo?.waffo_pay_methods}
                  waffoMinTopup={topupInfo?.waffo_min_topup}
                  onWaffoMethodSelect={handleWaffoMethodSelect}
                  enableWaffoPancakeTopup={
                    topupInfo?.enable_waffo_pancake_topup
                  }
                />
              </div>

              <SubscriptionPlansCard
                topupInfo={topupInfo}
                onAvailabilityChange={handleSubscriptionAvailabilityChange}
                userQuota={user?.quota}
                onPurchaseSuccess={fetchUser}
              />
            </div>

            <AffiliateRewardsCard
              user={user}
              affiliateLink={affiliateLink}
              onTransfer={() => setTransferDialogOpen(true)}
              complianceConfirmed={
                topupInfo?.payment_compliance_confirmed !== false
              }
              loading={affiliateLoading}
            />
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>

      <PaymentConfirmDialog
        open={confirmDialogOpen}
        onOpenChange={setConfirmDialogOpen}
        onConfirm={handlePaymentConfirm}
        topupAmount={topupAmount}
        paymentAmount={paymentAmount}
        paymentMethod={selectedPaymentMethod}
        calculating={calculating}
        processing={processing || waffoProcessing || pancakeProcessing}
        discountRate={getDiscountRate()}
        usdExchangeRate={effectiveUsdExchangeRate}
      />

      <TransferDialog
        open={transferDialogOpen}
        onOpenChange={setTransferDialogOpen}
        onConfirm={handleTransfer}
        availableQuota={user?.aff_quota ?? 0}
        transferring={transferring}
      />

      <BillingHistoryDialog
        open={billingDialogOpen}
        onOpenChange={setBillingDialogOpen}
      />

      <CreemConfirmDialog
        open={creemDialogOpen}
        onOpenChange={setCreemDialogOpen}
        onConfirm={handleCreemConfirm}
        product={selectedCreemProduct}
        processing={creemProcessing}
      />
    </>
  )
}

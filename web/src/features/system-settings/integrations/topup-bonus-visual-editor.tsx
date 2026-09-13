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
import { Pencil, Plus, Trash2 } from 'lucide-react'
import { useState, useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { StaticRowActions } from '@/components/data-table/static/static-row-actions'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'

import { safeJsonParseWithValidation } from '../utils/json-parser'
import { isObjectRecord } from '../utils/json-validators'
import { TopupBonusDialog, type TopupBonusData } from './topup-bonus-dialog'

type TopupBonusVisualEditorProps = {
  value: string
  onChange: (value: string) => void
}

function formatPercentage(ratio: number) {
  if (ratio <= 0) return '0%'
  return `${Number.parseFloat((ratio * 100).toFixed(2))}%`
}

export function TopupBonusVisualEditor({
  value,
  onChange,
}: TopupBonusVisualEditorProps) {
  const { t } = useTranslation()
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editData, setEditData] = useState<TopupBonusData | null>(null)

  const tiers = useMemo(() => {
    const parsed = safeJsonParseWithValidation<Record<string, unknown>>(value, {
      fallback: {},
      validator: isObjectRecord,
      validatorMessage: 'Top-up bonus must be a JSON object',
      context: 'topup bonus',
    })

    return Object.entries(parsed)
      .map(([amount, ratio]) => ({
        amount: Number.parseInt(amount, 10),
        bonusRatio:
          typeof ratio === 'number' ? ratio : Number.parseFloat(String(ratio)),
      }))
      .filter(
        (item) => !Number.isNaN(item.amount) && !Number.isNaN(item.bonusRatio)
      )
      .sort((a, b) => a.amount - b.amount)
  }, [value])

  const readTiers = () =>
    safeJsonParseWithValidation<Record<string, unknown>>(value, {
      fallback: {},
      validator: isObjectRecord,
      silent: true,
    })

  const handleSave = (data: TopupBonusData) => {
    const bonusObject = readTiers()

    if (editData && editData.amount !== data.amount) {
      delete bonusObject[editData.amount.toString()]
    }

    bonusObject[data.amount.toString()] = data.bonusRatio

    onChange(JSON.stringify(bonusObject, null, 2))
  }

  const handleDelete = (amount: number) => {
    const bonusObject = readTiers()
    delete bonusObject[amount.toString()]
    onChange(JSON.stringify(bonusObject, null, 2))
  }

  const handleEdit = (tier: TopupBonusData) => {
    setEditData(tier)
    setDialogOpen(true)
  }

  const handleAdd = () => {
    setEditData(null)
    setDialogOpen(true)
  }

  return (
    <div className='space-y-4'>
      <div className='flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between'>
        <p className='text-muted-foreground text-sm'>
          {t('Configure bonus ratios based on recharge amounts')}
        </p>
        <Button
          type='button'
          onClick={(e) => {
            e.preventDefault()
            e.stopPropagation()
            handleAdd()
          }}
          size='sm'
          className='w-full sm:w-auto'
        >
          <Plus className='h-4 w-4 sm:mr-2' />
          <span className='sm:inline'>{t('Add bonus tier')}</span>
        </Button>
      </div>

      {tiers.length === 0 ? (
        <div className='text-muted-foreground rounded-lg border border-dashed p-6 text-center text-sm'>
          {t(
            'No bonus tiers configured. Click "Add bonus tier" to get started.'
          )}
        </div>
      ) : (
        <div className='rounded-md border'>
          {/* Desktop table view */}
          <StaticDataTable
            className='hidden rounded-none border-0 sm:block'
            data={tiers}
            getRowKey={(tier) => tier.amount}
            columns={[
              {
                id: 'amount',
                header: t('Recharge Amount'),
                cell: (tier) => (
                  <span className='font-mono text-sm'>{tier.amount}+</span>
                ),
              },
              {
                id: 'bonus-ratio',
                header: t('Bonus Ratio'),
                cell: (tier) => (
                  <code className='bg-muted rounded px-1.5 py-0.5 text-sm'>
                    {tier.bonusRatio}
                  </code>
                ),
              },
              {
                id: 'bonus',
                header: t('Bonus'),
                cell: (tier) => (
                  <StatusBadge
                    variant={tier.bonusRatio > 0 ? 'success' : 'neutral'}
                    className='font-mono'
                    copyable={false}
                  >
                    +{formatPercentage(tier.bonusRatio)}
                  </StatusBadge>
                ),
              },
              {
                id: 'actions',
                header: t('Actions'),
                className: 'text-right',
                cellClassName: 'text-right',
                cell: (tier) => (
                  <StaticRowActions
                    editLabel={t('Edit')}
                    deleteLabel={t('Delete')}
                    menuLabel={t('Open menu')}
                    onEdit={() => handleEdit(tier)}
                    onDelete={() => handleDelete(tier.amount)}
                  />
                ),
              },
            ]}
          />

          {/* Mobile card view */}
          <div className='divide-y sm:hidden'>
            {tiers.map((tier) => (
              <div key={tier.amount} className='p-4'>
                <div className='mb-3 flex items-start justify-between'>
                  <div className='flex-1'>
                    <div className='mb-2 font-mono text-base font-medium'>
                      {tier.amount}+
                    </div>
                    <StatusBadge
                      variant={tier.bonusRatio > 0 ? 'success' : 'neutral'}
                      className='font-mono'
                      copyable={false}
                    >
                      +{formatPercentage(tier.bonusRatio)}
                    </StatusBadge>
                  </div>
                  <div className='flex gap-1'>
                    <Button
                      type='button'
                      variant='ghost'
                      size='sm'
                      onClick={(e) => {
                        e.preventDefault()
                        e.stopPropagation()
                        handleEdit(tier)
                      }}
                    >
                      <Pencil className='h-4 w-4' />
                    </Button>
                    <Button
                      type='button'
                      variant='ghost'
                      size='sm'
                      onClick={(e) => {
                        e.preventDefault()
                        e.stopPropagation()
                        handleDelete(tier.amount)
                      }}
                    >
                      <Trash2 className='h-4 w-4' />
                    </Button>
                  </div>
                </div>
                <div className='text-sm'>
                  <span className='text-muted-foreground'>
                    {t('Bonus Ratio')}:{' '}
                  </span>
                  <code className='bg-muted rounded px-1.5 py-0.5 text-xs'>
                    {tier.bonusRatio}
                  </code>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      <TopupBonusDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        onSave={handleSave}
        editData={editData}
      />
    </div>
  )
}

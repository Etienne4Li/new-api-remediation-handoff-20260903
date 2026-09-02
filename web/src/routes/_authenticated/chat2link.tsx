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
import { createFileRoute } from '@tanstack/react-router'
import { Loader2 } from 'lucide-react'
import { useEffect, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { useChatPresets } from '@/features/chat/hooks/use-chat-presets'
import {
  chatLinkRequiresApiKey,
  resolveChatUrl,
} from '@/features/chat/lib/chat-links'

export const Route = createFileRoute('/_authenticated/chat2link')({
  component: Chat2LinkPage,
})

function Chat2LinkPage() {
  const { t } = useTranslation()
  const { chatPresets, serverAddress } = useChatPresets()

  const firstWebPreset = useMemo(
    // Only keyless web presets may be opened in a browser. A web URL carrying
    // a long-lived API key would expose it to the third-party page and its
    // surrounding logging/referrer infrastructure.
    () =>
      chatPresets.find(
        (p) => p.type === 'web' && !chatLinkRequiresApiKey(p.url)
      ),
    [chatPresets]
  )

  useEffect(() => {
    if (!firstWebPreset) {
      if (chatPresets.length > 0) {
        toast.error(
          t(
            'No keyless Web chat links are configured. For security, API keys cannot be placed in third-party web URLs.'
          )
        )
      }
      return
    }

    const url = resolveChatUrl({
      template: firstWebPreset.url,
      serverAddress,
    })

    if (url) {
      window.location.href = url
    }
  }, [firstWebPreset, serverAddress, chatPresets.length, t])

  return (
    <div className='flex h-full flex-col items-center justify-center gap-3'>
      <Loader2 className='text-muted-foreground h-8 w-8 animate-spin' />
      <p className='text-muted-foreground text-sm'>
        {t('Redirecting to chat page...')}
      </p>
    </div>
  )
}

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
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Skeleton } from '@/components/ui/skeleton'
import { api } from '@/lib/api'

interface ImageDialogProps {
  imageUrl: string
  taskId?: string
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function ImageDialog({
  imageUrl,
  taskId,
  open,
  onOpenChange,
}: ImageDialogProps) {
  const { t } = useTranslation()
  const [isLoading, setIsLoading] = useState(true)
  const [hasError, setHasError] = useState(false)
  const [renderedImageUrl, setRenderedImageUrl] = useState<string>()

  useEffect(() => {
    let cancelled = false
    let objectUrl: string | undefined

    const loadImage = async () => {
      if (cancelled) return

      setIsLoading(true)
      setHasError(false)
      setRenderedImageUrl(undefined)

      if (!open) return

      let parsedUrl: URL
      try {
        parsedUrl = new URL(imageUrl, window.location.href)
      } catch {
        setHasError(true)
        setIsLoading(false)
        return
      }

      // The MJ image endpoint is authenticated. Fetch only the known proxy path
      // on the current origin through the shared API client so its current
      // Bearer token is attached, then render an object URL. Never attach that
      // token to a third-party URL. The segment check also permits a reverse
      // proxy prefix (for example /newapi/mj/image/...).
      const isMidjourneyProxyPath = /\/mj\/image\/[^/]+$/.test(
        parsedUrl.pathname
      )
      if (
        parsedUrl.origin !== window.location.origin ||
        !isMidjourneyProxyPath
      ) {
        setRenderedImageUrl(imageUrl)
        return
      }

      try {
        const response = await api.get<Blob>(
          parsedUrl.pathname + parsedUrl.search,
          {
            responseType: 'blob',
            skipErrorHandler: true,
          }
        )
        if (cancelled) return
        objectUrl = URL.createObjectURL(response.data)
        setRenderedImageUrl(objectUrl)
      } catch {
        if (cancelled) return
        setHasError(true)
        setIsLoading(false)
      }
    }

    // Defer state initialization until after the effect has committed. This
    // keeps the effect focused on synchronizing the external image request.
    void Promise.resolve().then(loadImage)

    return () => {
      cancelled = true
      if (objectUrl) URL.revokeObjectURL(objectUrl)
    }
  }, [imageUrl, open])

  // Reset loading state when dialog opens or image URL changes
  const handleOpenChange = (newOpen: boolean) => {
    if (newOpen) {
      setIsLoading(true)
      setHasError(false)
    }
    onOpenChange(newOpen)
  }

  const handleImageLoad = () => {
    setIsLoading(false)
    setHasError(false)
  }

  const handleImageError = () => {
    setIsLoading(false)
    setHasError(true)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={handleOpenChange}
      title={t('Image Preview')}
      description={
        taskId ? `${t('Task ID:')} ${taskId}` : t('View the generated image')
      }
      contentClassName='sm:max-w-3xl'
      contentHeight='auto'
      bodyClassName='space-y-4'
    >
      <ScrollArea className='max-h-[600px]'>
        <div className='py-4'>
          <div className='bg-muted/50 relative flex min-h-[300px] items-center justify-center rounded-lg border'>
            {/* Skeleton - show when loading or error */}
            {(isLoading || hasError) && (
              <Skeleton className='absolute inset-0 h-full w-full rounded-lg' />
            )}

            {/* Actual Image */}
            {renderedImageUrl && (
              <img
                src={renderedImageUrl}
                alt={t('Generated image')}
                className={`max-h-[550px] w-full rounded-lg object-contain ${
                  isLoading || hasError ? 'opacity-0' : 'opacity-100'
                }`}
                onLoad={handleImageLoad}
                onError={handleImageError}
                loading='lazy'
              />
            )}

            {/* Error text overlay (shown on skeleton) */}
            {hasError && (
              <div className='absolute inset-0 flex items-center justify-center'>
                <p className='text-muted-foreground text-sm'>
                  {t('Failed to load image')}
                </p>
              </div>
            )}
          </div>

          {/* Image URL */}
          <div className='bg-muted mt-4 rounded-md p-3'>
            <p className='text-muted-foreground font-mono text-xs break-all'>
              {imageUrl}
            </p>
          </div>
        </div>
      </ScrollArea>
    </Dialog>
  )
}

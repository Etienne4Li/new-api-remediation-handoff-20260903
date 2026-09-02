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
import { Link } from '@tanstack/react-router'
import { WalletCards } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { ConfigDrawer } from '@/components/config-drawer'
import { LanguageSwitcher } from '@/components/language-switcher'
import { NotificationPopover } from '@/components/notification-popover'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { Search } from '@/components/search'
import { Button } from '@/components/ui/button'
import { useNotifications } from '@/hooks/use-notifications'
import { useStatus } from '@/hooks/use-status'
import { useTopNavLinks } from '@/hooks/use-top-nav-links'
import { formatQuota } from '@/lib/format'
import { useAuthStore } from '@/stores/auth-store'

import { defaultTopNavLinks } from '../config/top-nav.config'
import type { TopNavLink } from '../types'
import { Header } from './header'
import { SystemBrand } from './system-brand'
import { TopNav } from './top-nav'

/**
 * General application Header component
 * Integrates navigation bar, search, configuration and profile functions
 *
 * @example
 * // Basic usage
 * <AppHeader />
 *
 * @example
 * // Custom navigation links
 * <AppHeader navLinks={customLinks} />
 *
 * @example
 * // Hide navigation bar and search box
 * <AppHeader showTopNav={false} showSearch={false} />
 *
 * @example
 * // Fully customize left and right content
 * <AppHeader
 *   leftContent={<CustomLeft />}
 *   rightContent={<CustomRight />}
 * />
 */
type AppHeaderProps = {
  /**
   * Custom navigation links, uses default global navigation or dynamically generated from backend if not provided
   */
  navLinks?: TopNavLink[]
  /**
   * Whether to show top navigation bar
   * @default true
   */
  showTopNav?: boolean
  /**
   * Optional content displayed after the brand
   */
  leftContent?: React.ReactNode
  /**
   * Whether to show search box
   * @default false
   */
  showSearch?: boolean
  /**
   * Whether to show the authenticated account balance
   * @default true
   */
  showBalance?: boolean
  /**
   * Custom right content, overrides the default navigation and account actions
   */
  rightContent?: React.ReactNode
  /**
   * Whether to show notification button
   * @default true
   */
  showNotifications?: boolean
  /**
   * Whether to show config drawer
   * @default true
   */
  showConfigDrawer?: boolean
  /**
   * Whether to show profile dropdown
   * @default true
   */
  showProfileDropdown?: boolean
}

export function AppHeader({
  navLinks = defaultTopNavLinks,
  showTopNav = true,
  leftContent,
  showSearch = false,
  showBalance = true,
  rightContent,
  showNotifications = true,
  showConfigDrawer = true,
  showProfileDropdown = true,
}: AppHeaderProps) {
  const { t } = useTranslation()
  const user = useAuthStore((state) => state.auth.user)
  // Prioritize dynamically generated links from backend
  const dynamicLinks = useTopNavLinks()
  const { status: statusSnapshot } = useStatus()
  const links =
    dynamicLinks.length > 0 || statusSnapshot !== null ? dynamicLinks : navLinks

  // Notifications hook
  const notifications = useNotifications()

  return (
    <Header showSidebarTrigger={false}>
      <SystemBrand variant='inline' />

      {leftContent ? (
        <div className='ms-2 flex min-w-0 items-center'>{leftContent}</div>
      ) : null}

      {rightContent ?? (
        <div className='ms-auto flex min-w-0 items-center justify-end gap-0.5 sm:gap-2'>
          {showTopNav && links.length > 0 && (
            <div className='me-0 min-w-0 lg:me-1'>
              <TopNav links={links} />
            </div>
          )}
          {showSearch && (
            <Search className='size-8 flex-none justify-center p-0 sm:w-44 sm:justify-start sm:pe-12 md:flex-none lg:w-56 xl:w-72 [&>span]:hidden sm:[&>span]:inline' />
          )}
          {showBalance && user && (
            <Button
              variant='outline'
              size='sm'
              className='h-8 gap-1.5 px-2 sm:px-2.5'
              aria-label={`${t('Balance')}: ${formatQuota(Number(user.quota ?? 0))}`}
              render={<Link to='/wallet' />}
            >
              <WalletCards
                className='text-muted-foreground size-3.5 shrink-0'
                aria-hidden='true'
              />
              <span className='font-mono text-xs font-semibold tabular-nums'>
                {formatQuota(Number(user.quota ?? 0))}
              </span>
            </Button>
          )}
          {showNotifications && (
            <NotificationPopover
              open={notifications.popoverOpen}
              onOpenChange={notifications.setPopoverOpen}
              unreadCount={notifications.unreadCount}
              activeTab={notifications.activeTab}
              onTabChange={notifications.setActiveTab}
              notice={notifications.notice}
              announcements={notifications.announcements}
              loading={notifications.loading}
              className='size-8'
            />
          )}
          <div className='hidden sm:block'>
            <LanguageSwitcher />
          </div>
          {showConfigDrawer && <ConfigDrawer />}
          {showProfileDropdown && <ProfileDropdown />}
        </div>
      )}
    </Header>
  )
}

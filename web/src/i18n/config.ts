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
import i18n, { type BackendModule } from 'i18next'
import LanguageDetector from 'i18next-browser-languagedetector'
import { initReactI18next } from 'react-i18next'

import {
  convertDetectedLanguage,
  type InterfaceLanguageCode,
} from './languages'

type TranslationResource = Record<string, string>
type LocaleLoader = () => Promise<TranslationResource>

const localeLoaders = {
  en: () =>
    import('./locales/en.json').then((module) => module.default.translation),
  zhCN: () =>
    import('./locales/zh.json').then((module) => module.default.translation),
  fr: () =>
    import('./locales/fr.json').then((module) => module.default.translation),
  ru: () =>
    import('./locales/ru.json').then((module) => module.default.translation),
  ja: () =>
    import('./locales/ja.json').then((module) => module.default.translation),
  vi: () =>
    import('./locales/vi.json').then((module) => module.default.translation),
  zhTW: () =>
    import('./locales/zh-TW.json').then((module) => module.default.translation),
} satisfies Record<InterfaceLanguageCode, LocaleLoader>

const localeBackend: BackendModule = {
  type: 'backend',
  init() {},
  read(language: string, _namespace: string) {
    const loader = localeLoaders[language as InterfaceLanguageCode]
    if (!loader) {
      return Promise.reject(
        new Error(`Unsupported interface language: ${language}`)
      )
    }

    return loader()
  },
}

export const i18nReady = i18n
  .use(localeBackend)
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    fallbackLng: 'en',
    supportedLngs: ['en', 'zhCN', 'fr', 'ru', 'ja', 'vi', 'zhTW'],
    load: 'currentOnly',
    nsSeparator: false, // Allow literal colons in keys (e.g., URLs, labels)
    debug: import.meta.env.DEV,
    interpolation: {
      escapeValue: false, // not needed for react as it escapes by default
    },
    detection: {
      order: ['localStorage', 'navigator'],
      caches: ['localStorage'],
      // Browsers report `zh-CN`/`zh-TW`/`zh`; map them onto our `zhCN`/`zhTW`
      // codes (non-Chinese codes pass through for normal supportedLngs matching).
      convertDetectedLanguage,
    },
  })

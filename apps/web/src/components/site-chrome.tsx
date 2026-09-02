import { Languages, Moon, Sun } from 'lucide-react'
import appIconUrl from '../../../../assets/brand/vc-workspace-app-icon.svg?url'
import type { MessageKey } from '../i18n'

type Translator = (key: MessageKey, variables?: Record<string, string>) => string

export function ProductBrand({ product, className = '', href }: { product: string; className?: string; href?: string }) {
  const content = <>
    <img className="brand-mark" src={appIconUrl} alt="" />
    <span>{product}</span>
  </>
  const classes = `brand ${className}`.trim()
  return href
    ? <a className={classes} href={href} aria-label={product}>
        {content}
      </a>
    : <div className={classes}>
        {content}
      </div>
}

export function ThemeLocaleButtons({ t, theme, onLocale, onTheme }: { t: Translator; theme: 'light' | 'dark'; onLocale: () => void; onTheme: () => void }) {
  return <>
    <button className="icon-button" type="button" onClick={onLocale} aria-label={t('switchLanguage')} title={t('switchLanguage')}><Languages aria-hidden="true" /></button>
    <button className="icon-button" type="button" onClick={onTheme} aria-label={theme === 'dark' ? t('lightTheme') : t('darkTheme')} title={theme === 'dark' ? t('lightTheme') : t('darkTheme')}>{theme === 'dark' ? <Sun aria-hidden="true" /> : <Moon aria-hidden="true" />}</button>
  </>
}

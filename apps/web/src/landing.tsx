import { ArrowRight, Bot, Boxes, Cpu, GitFork, Monitor } from 'lucide-react'
import type { Locale, MessageKey } from './i18n'
import { ProductBrand, ThemeLocaleButtons } from './components/site-chrome'

export const APP_ROUTES = {
  landing: '/',
  console: '/console',
} as const

export const SOURCE_REPOSITORY = 'https://github.com/Veritas-Calculus/vc-workspace'

type Translator = (key: MessageKey, variables?: Record<string, string>) => string

const capabilities = [
  { icon: Boxes, title: 'landingCapabilityControl', detail: 'landingCapabilityControlDetail' },
  { icon: Monitor, title: 'landingCapabilityClient', detail: 'landingCapabilityClientDetail' },
  { icon: Cpu, title: 'landingCapabilityImages', detail: 'landingCapabilityImagesDetail' },
  { icon: Bot, title: 'landingCapabilityAgent', detail: 'landingCapabilityAgentDetail' },
] as const satisfies ReadonlyArray<{ icon: typeof Boxes; title: MessageKey; detail: MessageKey }>

export function isConsolePath(pathname: string): boolean {
  return pathname === APP_ROUTES.console || pathname.startsWith(`${APP_ROUTES.console}/`)
}

export function LandingPage({ locale, theme, t, onLocale, onTheme }: { locale: Locale; theme: 'light' | 'dark'; t: Translator; onLocale: () => void; onTheme: () => void }) {
  return <div className="landing-page">
    <header className="landing-header">
      <div className="landing-width landing-header-inner">
        <ProductBrand product={t('product')} className="landing-brand" href={APP_ROUTES.landing} />
        <nav className="landing-header-actions" aria-label={t('landingNavigation')}>
          <ThemeLocaleButtons t={t} theme={theme} onLocale={onLocale} onTheme={onTheme} />
          <a className="button landing-console-link" href={APP_ROUTES.console}>{t('landingOpenConsole')}</a>
        </nav>
      </div>
    </header>

    <main>
      <section className="landing-width landing-hero" aria-labelledby="landing-title">
        <div className="landing-copy">
          <p className="landing-positioning">{t('landingPositioning')}</p>
          <h1 id="landing-title" data-locale={locale}>{t('landingTitle')}</h1>
          <p className="landing-lead">{t('landingLead')}</p>
          <div className="landing-hero-actions">
            <a className="button button-primary" href={APP_ROUTES.console}>{t('landingOpenConsole')}<ArrowRight aria-hidden="true" /></a>
            <a className="button" href={SOURCE_REPOSITORY} target="_blank" rel="noreferrer"><GitFork aria-hidden="true" />{t('landingViewSource')}</a>
          </div>
        </div>

        <figure className="landing-architecture" aria-labelledby="landing-architecture-title">
          <figcaption id="landing-architecture-title">{t('landingArchitecture')}</figcaption>
          <div className="landing-architecture-flow">
            <div className="landing-architecture-node">
              <span>{t('landingInfrastructure')}</span>
              <strong>{t('landingInfrastructureValue')}</strong>
              <small>{t('landingInfrastructureDetail')}</small>
            </div>
            <ArrowRight className="landing-flow-arrow" aria-hidden="true" />
            <div className="landing-architecture-node" data-primary="true">
              <span>{t('landingControlPlane')}</span>
              <strong>{t('product')}</strong>
              <small>{t('landingControlPlaneDetail')}</small>
            </div>
            <ArrowRight className="landing-flow-arrow" aria-hidden="true" />
            <div className="landing-architecture-node">
              <span>{t('landingAccess')}</span>
              <strong>{t('landingAccessValue')}</strong>
              <small>{t('landingAccessDetail')}</small>
            </div>
          </div>
        </figure>
      </section>

      <section className="landing-width landing-capabilities" aria-labelledby="landing-capabilities-title">
        <div className="landing-section-heading">
          <h2 id="landing-capabilities-title">{t('landingCapabilities')}</h2>
          <p>{t('landingCapabilitiesLead')}</p>
        </div>
        <div className="landing-capability-list">
          {capabilities.map(({ icon: Icon, title, detail }) => <article key={title}>
            <Icon aria-hidden="true" />
            <div><h3>{t(title)}</h3><p>{t(detail)}</p></div>
          </article>)}
        </div>
      </section>

      <section className="landing-width landing-repository" aria-labelledby="landing-repository-title">
        <div>
          <h2 id="landing-repository-title">{t('landingRepositoryTitle')}</h2>
          <p>{t('landingRepositoryDetail')}</p>
        </div>
        <a className="button" href={SOURCE_REPOSITORY} target="_blank" rel="noreferrer"><GitFork aria-hidden="true" />{t('landingRepositoryAction')}</a>
      </section>
    </main>

    <footer className="landing-footer">
      <div className="landing-width"><span>{t('product')}</span><span>{t('landingFooter')}</span></div>
    </footer>
  </div>
}

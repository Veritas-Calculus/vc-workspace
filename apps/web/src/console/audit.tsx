import { useCallback, useEffect, useState } from 'react'
import { Search } from 'lucide-react'
import { getAuditEvents, type AuditEvent } from '../api'
import type { Locale, MessageKey } from '../i18n'
import { EmptyState, ErrorState, LoadingState } from '../components/states'
import type { Translator } from './workspaces'

const eventMessages: Partial<Record<string, MessageKey>> = {
  'setup.admin_created': 'auditSetupAdminCreated',
  'setup.admin_creation_failed': 'auditSetupAdminCreationFailed',
  'auth.login_succeeded': 'auditWebLoginSucceeded',
  'auth.login_failed': 'auditWebLoginFailed',
  'auth.logout_succeeded': 'auditWebLogoutSucceeded',
  'auth.oidc_login_succeeded': 'auditOIDCLoginSucceeded',
  'auth.oidc_login_failed': 'auditOIDCLoginFailed',
  'auth.native_login_succeeded': 'auditNativeLoginSucceeded',
  'auth.native_login_failed': 'auditNativeLoginFailed',
  'auth.native_oidc_login_succeeded': 'auditNativeLoginSucceeded',
  'auth.native_oidc_login_failed': 'auditNativeLoginFailed',
  'auth.native_logout_succeeded': 'auditNativeLogoutSucceeded',
  'native.desktop_connection_created': 'auditDesktopConnected',
  'desktop.access_policy_updated': 'auditPermissionUpdated',
  'desktop.access_policy_applied': 'auditPermissionApplied',
  'desktop.access_policy_apply_failed': 'auditPermissionFailed',
  'desktop.access_policy_enforcement_failed': 'auditPermissionFailed',
  'desktop.clone_requested': 'auditDesktopCloneRequested',
  'virtual_machine.start_requested': 'auditDesktopStartRequested',
  'virtual_machine.stop_requested': 'auditDesktopStopRequested',
  'native.desktop_start_requested': 'auditDesktopStartRequested',
  'native.desktop_stop_requested': 'auditDesktopStopRequested',
  'agent.desktop_start_requested': 'auditDesktopStartRequested',
  'agent.desktop_stop_requested': 'auditDesktopStopRequested',
  'image_profile.updated': 'auditImageUpdated',
  'image_build.started': 'auditImageBuildStarted',
  'image_build.succeeded': 'auditImageBuildSucceeded',
  'image_build.failed': 'auditImageBuildFailed',
  'gpu_profile.updated': 'auditGPUUpdated',
  'pci_resource_mapping.created': 'auditMappingCreated',
  'pci_resource_mapping.updated': 'auditMappingUpdated',
  'agent.desktop_lease_created': 'auditAgentLeaseCreated',
  'agent.desktop_lease_released': 'auditAgentLeaseReleased',
	'agent.desktop_access_denied': 'auditDesktopAccessDenied',
	'desktop.access_denied': 'auditDesktopAccessDenied',
	'desktop_registry.reconciled': 'auditDesktopRegistryReconciled',
	'desktop_assignment.created': 'auditDesktopAssigned',
	'desktop_assignment.deleted': 'auditDesktopUnassigned',
	'user.created': 'auditUserCreated',
	'user.updated': 'auditUserUpdated',
	'agent.created': 'auditAgentCreated',
	'agent.updated': 'auditAgentUpdated',
	'agent.token_rotated': 'auditAgentTokenRotated',
  'job.succeeded': 'auditJobSucceeded',
  'job.failed': 'auditJobFailed',
  'audit.read_denied': 'auditReadDenied',
}

type AuditState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'ready'; events: AuditEvent[]; nextCursor?: string }

export function AuditView({ locale, t }: { locale: Locale; t: Translator }) {
  const [query, setQuery] = useState('')
  const [outcome, setOutcome] = useState<'' | AuditEvent['outcome']>('')
  const [filters, setFilters] = useState<{ query: string; outcome: '' | AuditEvent['outcome'] }>({ query: '', outcome: '' })
  const [state, setState] = useState<AuditState>({ kind: 'loading' })
  const [loadingMore, setLoadingMore] = useState(false)
  const [pageError, setPageError] = useState('')

  const load = useCallback(async (signal?: AbortSignal) => {
    setState({ kind: 'loading' })
    setPageError('')
    try {
      const page = await getAuditEvents(filters, signal)
      setState({ kind: 'ready', events: page.events, nextCursor: page.next_cursor })
    } catch (error) {
      if (error instanceof DOMException && error.name === 'AbortError') return
      setState({ kind: 'error', message: error instanceof Error ? error.message : t('unavailable') })
    }
  }, [filters, t])

  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  const loadMore = async () => {
    if (state.kind !== 'ready' || !state.nextCursor || loadingMore) return
    setLoadingMore(true)
    setPageError('')
    try {
      const page = await getAuditEvents({ ...filters, cursor: state.nextCursor })
      setState({ kind: 'ready', events: [...state.events, ...page.events], nextCursor: page.next_cursor })
    } catch (error) {
      setPageError(error instanceof Error ? error.message : t('unavailable'))
    } finally {
      setLoadingMore(false)
    }
  }

  if (state.kind === 'loading') return <LoadingState label={t('loadingAudit')} />
  if (state.kind === 'error') return <ErrorState title={t('auditUnavailable')} detail={state.message} action={t('retry')} onRetry={() => void load()} />

  const filtered = Boolean(filters.query || filters.outcome)
  return <section className="section" aria-labelledby="audit-title">
    <div className="section-heading section-heading-with-tools audit-heading">
      <h2 id="audit-title">{t('auditLog')} <span className="section-count">{state.events.length}</span></h2>
      <form className="filters audit-filters" role="search" onSubmit={(event) => { event.preventDefault(); setFilters({ query: query.trim(), outcome }) }}>
        <label className="search-field"><Search aria-hidden="true" /><span className="sr-only">{t('searchAudit')}</span><input value={query} maxLength={128} onChange={(event) => setQuery(event.target.value)} placeholder={t('searchAudit')} /></label>
        <label className="filter-select"><span className="sr-only">{t('auditOutcome')}</span><select value={outcome} onChange={(event) => setOutcome(event.target.value as '' | AuditEvent['outcome'])}><option value="">{t('allOutcomes')}</option><option value="success">{t('auditSuccess')}</option><option value="failure">{t('auditFailure')}</option></select></label>
        <button className="button" type="submit">{t('filter')}</button>
      </form>
    </div>
    {state.events.length === 0
      ? <EmptyState title={filtered ? t('noFilteredAudit') : t('noAuditEvents')} action={filtered ? t('clearSearch') : undefined} onAction={filtered ? () => { setQuery(''); setOutcome(''); setFilters({ query: '', outcome: '' }) } : undefined} />
      : <>
        <div className="table-scroll"><table className="audit-table"><thead><tr><th>{t('time')}</th><th>{t('actor')}</th><th data-column="event" data-fluid>{t('event')}</th><th>{t('target')}</th><th>{t('result')}</th><th>{t('detail')}</th></tr></thead><tbody>{state.events.map((event) => <AuditRow key={event.id} event={event} locale={locale} t={t} />)}</tbody></table></div>
        {pageError && <p className="field-error" role="alert">{pageError}</p>}
        {state.nextCursor && <div className="pagination audit-pagination"><span>{t('showingAuditEvents', { count: String(state.events.length) })}</span><button className="text-button" type="button" disabled={loadingMore} onClick={() => void loadMore()}>{loadingMore ? t('loadingMore') : t('loadOlder')}</button></div>}
      </>}
  </section>
}

function AuditRow({ event, locale, t }: { event: AuditEvent; locale: Locale; t: Translator }) {
  const eventKey = eventMessages[event.event_type]
  const actor = event.actor_display_name || event.actor_username || t('systemActor')
  const target = event.target_id ? `${event.target_type || t('target')} · ${event.target_id}` : '—'
  const detailEntries = Object.entries(event.detail ?? {})
  return <tr>
    <td><time dateTime={event.occurred_at}>{new Intl.DateTimeFormat(locale, { dateStyle: 'short', timeStyle: 'medium' }).format(new Date(event.occurred_at))}</time></td>
    <td className="audit-actor"><span className="truncate" title={actor}>{actor}</span>{event.actor_username && event.actor_display_name && <small>@{event.actor_username}</small>}</td>
    <td className="primary-cell"><span className="truncate" title={eventKey ? t(eventKey) : event.event_type}>{eventKey ? t(eventKey) : event.event_type}</span></td>
    <td className="audit-target"><span className="truncate" title={target}>{target}</span></td>
    <td><span className="status" data-status={event.outcome === 'success' ? 'succeeded' : 'failed'}><span className="status-mark" aria-hidden="true" />{event.outcome === 'success' ? t('auditSuccess') : t('auditFailure')}</span></td>
    <td><details className="audit-detail"><summary>{t('viewDetail')}</summary><dl><div><dt>{t('eventType')}</dt><dd><code>{event.event_type}</code></dd></div>{detailEntries.map(([key, value]) => <div key={key}><dt>{key.replaceAll('_', ' ')}</dt><dd>{formatAuditValue(value)}</dd></div>)}</dl></details></td>
  </tr>
}

function formatAuditValue(value: unknown): string {
  if (typeof value === 'string') return value
  if (typeof value === 'number' || typeof value === 'boolean') return String(value)
  if (value == null) return '—'
  return JSON.stringify(value)
}

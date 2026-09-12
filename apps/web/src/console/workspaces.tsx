import { useEffect, useMemo, useRef, useState, type ComponentType } from 'react'
import { Activity, AppWindow, ClipboardList, Cpu, Images, Monitor, Pencil, Play, Search, Server, ShieldCheck, Square, UsersRound } from 'lucide-react'
import type { GPUProfile, ImageProfile, Infrastructure, Job, PlatformConfig } from '../api'
import { desktopAppLink } from '../app-link'
import type { Locale, MessageKey } from '../i18n'
import { EmptyState } from '../components/states'
import { canInspectCloneRecovery } from '../clone-recovery'
import { CloneRecoveryForm } from './clone-recovery'

export type ConsoleView = 'desktops' | 'activity' | 'infrastructure' | 'images' | 'gpu' | 'access' | 'audit'
export type Translator = (key: MessageKey, variables?: Record<string, string>) => string

export type WorkspaceDefinition = {
  id: ConsoleView
  label: MessageKey
  icon: ComponentType<{ 'aria-hidden'?: boolean }>
  dataSource: 'infrastructure' | 'jobs' | 'platform' | 'access' | 'audit'
	adminOnly?: boolean
  hasGlobalRefresh?: boolean
  canCreateDesktop?: boolean
  showsDesktopJob?: boolean
  showsImageBuild?: boolean
  showsClusterContext?: boolean
}

export const WORKSPACES: readonly WorkspaceDefinition[] = [
  { id: 'desktops', label: 'desktops', icon: Monitor, dataSource: 'infrastructure', canCreateDesktop: true, showsDesktopJob: true },
  { id: 'activity', label: 'recentTasks', icon: Activity, dataSource: 'jobs', adminOnly: true },
  { id: 'infrastructure', label: 'infrastructure', icon: Server, dataSource: 'infrastructure', adminOnly: true, showsClusterContext: true },
  { id: 'images', label: 'imageProfiles', icon: Images, dataSource: 'platform', adminOnly: true, showsImageBuild: true },
  { id: 'gpu', label: 'gpuAcceleration', icon: Cpu, dataSource: 'platform', adminOnly: true },
	{ id: 'access', label: 'accessControl', icon: UsersRound, dataSource: 'access', adminOnly: true, hasGlobalRefresh: false },
	{ id: 'audit', label: 'auditLog', icon: ClipboardList, dataSource: 'audit', adminOnly: true, hasGlobalRefresh: false },
] as const

const PAGE_SIZE = 20

type DesktopFilter = 'all' | 'running' | 'stopped'

export function managedDesktops(data: Infrastructure): Infrastructure['virtual_machines'] {
  return data.virtual_machines.filter((machine) => machine.managed && !machine.template)
}

export function filterManagedDesktops(data: Infrastructure, query: string, filter: DesktopFilter, locale: Locale): Infrastructure['virtual_machines'] {
  const normalizedQuery = query.trim().toLocaleLowerCase(locale)
  return managedDesktops(data).filter((machine) => {
    if (filter !== 'all' && machine.status !== filter) return false
    if (!normalizedQuery) return true
    return `${machine.name} ${machine.vmid} ${machine.node}`.toLocaleLowerCase(locale).includes(normalizedQuery)
  })
}

export function DesktopsView({ data, locale, activeJob, t, onCreate, onPower, onPermissions }: { data: Infrastructure; locale: Locale; activeJob: Job | null; t: Translator; onCreate: () => void; onPower: (vmid: number, action: 'start' | 'stop') => void; onPermissions?: (machine: Infrastructure['virtual_machines'][number], trigger: HTMLButtonElement) => void }) {
  const [query, setQuery] = useState('')
  const [filter, setFilter] = useState<DesktopFilter>('all')
  const [page, setPage] = useState(0)
  const allDesktops = useMemo(() => managedDesktops(data), [data])
  const filtered = useMemo(() => filterManagedDesktops(data, query, filter, locale), [data, filter, locale, query])
  const pageCount = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE))
  const currentPage = Math.min(page, pageCount - 1)
  const pageStart = currentPage * PAGE_SIZE
  const paged = filtered.slice(pageStart, pageStart + PAGE_SIZE)
  const busy = activeJob?.state === 'accepted' || activeJob?.state === 'running'

  useEffect(() => setPage(0), [filter, query])

  if (allDesktops.length === 0) {
    return <EmptyState title={t('noDesktops')} detail={t('noDesktopsDetail')} action={data.mutations_enabled ? t('createDesktop') : undefined} onAction={data.mutations_enabled ? onCreate : undefined} />
  }

  return (
    <section className="section" aria-labelledby="desktops-title">
      <div className="section-heading section-heading-with-tools">
        <h2 id="desktops-title">{t('desktops')} <span className="section-count">{filtered.length}</span></h2>
        <div className="filters">
          <label className="search-field"><Search aria-hidden="true" /><span className="sr-only">{t('searchDesktops')}</span><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder={t('searchDesktops')} /></label>
          <label className="filter-select"><span className="sr-only">{t('desktopState')}</span><select value={filter} onChange={(event) => setFilter(event.target.value as DesktopFilter)}><option value="all">{t('allStates')}</option><option value="running">{t('running')}</option><option value="stopped">{t('stopped')}</option></select></label>
        </div>
      </div>
      {filtered.length === 0 ? <EmptyState title={t('noFilteredDesktops')} action={t('clearSearch')} onAction={() => { setQuery(''); setFilter('all') }} /> : <div className="table-scroll">
        <table>
          <thead><tr><th data-column="name" data-fluid>{t('name')}</th><th>{t('vmid')}</th><th>{t('node')}</th><th>{t('status')}</th><th className="numeric-column">{t('cpu')}</th><th className="numeric-column">{t('memory')}</th>{data.mutations_enabled && <th>{t('actions')}</th>}</tr></thead>
          <tbody>{paged.map((machine) => {
            const openLabel = t('openInApp', { name: machine.name })
            return <tr key={machine.vmid}><td className="primary-cell"><a className="desktop-launch-link" href={desktopAppLink(machine.vmid)} aria-label={openLabel} title={openLabel}><span className="truncate" title={machine.name}>{machine.name}</span><AppWindow aria-hidden="true" /></a></td><td>{machine.vmid}</td><td>{machine.node}</td><td><Status value={machine.status} t={t} /></td><td className="numeric-column">{machine.cpu_count}</td><td className="numeric-column">{formatMemory(machine.memory_total, locale)}</td>{data.mutations_enabled && <td><div className="table-actions">{onPermissions && <button className="table-action" type="button" aria-label={`${t('desktopPermissions')} · ${machine.name}`} title={t('desktopPermissions')} onClick={(event) => onPermissions(machine, event.currentTarget)}><ShieldCheck aria-hidden="true" /></button>}<button className="table-action" type="button" aria-label={`${machine.status === 'running' ? t('stop') : t('start')} ${machine.name}`} title={machine.status === 'running' ? t('stop') : t('start')} aria-disabled={busy} onClick={() => onPower(machine.vmid, machine.status === 'running' ? 'stop' : 'start')}>{machine.status === 'running' ? <Square aria-hidden="true" /> : <Play aria-hidden="true" />}</button></div></td>}</tr>
          })}</tbody>
        </table>
        {pageCount > 1 && <div className="pagination" aria-label={t('desktops')}><span>{t('resultRange', { start: String(pageStart + 1), end: String(Math.min(pageStart + PAGE_SIZE, filtered.length)), total: String(filtered.length) })}</span><div><button className="text-button" type="button" disabled={currentPage === 0} onClick={() => setPage((value) => Math.max(0, value - 1))}>{t('previousPage')}</button><button className="text-button" type="button" disabled={currentPage >= pageCount - 1} onClick={() => setPage((value) => Math.min(pageCount - 1, value + 1))}>{t('nextPage')}</button></div></div>}
      </div>}
    </section>
  )
}

const jobOperationMessages: Partial<Record<string, MessageKey>> = {
  'pve.template_clone': 'taskCloneDesktop',
  'pve.vm_start': 'taskStartDesktop',
  'pve.vm_stop': 'taskStopDesktop',
  'native.desktop_start': 'taskStartDesktop',
  'native.desktop_stop': 'taskStopDesktop',
  'agent.desktop_start': 'taskStartDesktop',
  'agent.desktop_stop': 'taskStopDesktop',
  'image.build': 'taskBuildImage',
}

const jobStateMessages: Record<Job['state'], MessageKey> = {
  accepted: 'accepted',
  running: 'running',
  succeeded: 'succeeded',
  failed: 'failed',
}

export function ActivityView({ jobs, locale, t, csrf, onJob }: { jobs: Job[]; locale: Locale; t: Translator; csrf?: string; onJob?: (job: Job) => void }) {
  const [recovering, setRecovering] = useState<Job | null>(null)
  const recoveryTrigger = useRef<HTMLButtonElement | null>(null)
  const taskHeading = useRef<HTMLHeadingElement>(null)
  const closeRecovery = () => {
    const target = recoveryTrigger.current
    if (target?.isConnected) target.focus()
    else taskHeading.current?.focus()
    setRecovering(null)
  }
  if (jobs.length === 0) return <EmptyState title={t('noTasks')} detail={t('noTasksDetail')} />
  return (
    <section className="section" aria-labelledby="recent-tasks-title">
      <div className="section-heading"><h2 id="recent-tasks-title" ref={taskHeading} tabIndex={-1}>{t('recentTasks')} <span className="section-count">{jobs.length}</span></h2></div>
      <div className="table-scroll">
        <table>
          <thead><tr><th data-column="operation" data-fluid>{t('operation')}</th><th>{t('target')}</th><th>{t('status')}</th><th className="numeric-column">{t('progress')}</th><th>{t('updated')}</th><th>{t('detail')}</th></tr></thead>
          <tbody>{jobs.map((job) => {
            const operationKey = jobOperationMessages[job.operation]
            const detail = job.error || job.detail || '—'
            return <tr key={job.id}><td className="primary-cell">{operationKey ? t(operationKey) : <code>{job.operation}</code>}</td><td>{job.target_vmid ? `VM ${job.target_vmid}` : job.target_node || '—'}</td><td><span className="status" data-status={job.state}><span className="status-mark" aria-hidden="true" />{t(jobStateMessages[job.state])}</span></td><td className="numeric-column">{job.progress}%</td><td><time dateTime={job.updated_at}>{formatDateTime(job.updated_at, locale)}</time></td><td className="detail-cell"><span className="truncate" title={detail}>{detail}</span>{csrf && onJob && canInspectCloneRecovery(job) && <button type="button" className="button" aria-label={`${t('cloneRecovery')} · VM ${job.target_vmid}`} aria-expanded={recovering?.id === job.id} onClick={event => {
              if (recovering?.id === job.id) closeRecovery()
              else { recoveryTrigger.current = event.currentTarget; setRecovering(job) }
            }}>{t('cloneRecovery')}</button>}</td></tr>
          })}</tbody>
        </table>
      </div>
      {recovering && csrf && onJob && <CloneRecoveryForm key={recovering.id} job={recovering} csrf={csrf} t={t} onJob={onJob} onClose={closeRecovery} />}
    </section>
  )
}

export function InfrastructureView({ data, locale, t }: { data: Infrastructure; locale: Locale; t: Translator }) {
  return (
    <>
      <section className="section" aria-labelledby="nodes-title">
        <div className="section-heading"><h2 id="nodes-title">{t('nodes')} <span className="section-count">{data.nodes.length}</span></h2></div>
        <div className="table-scroll"><table><thead><tr><th data-column="name" data-fluid>{t('name')}</th><th>{t('status')}</th><th className="numeric-column">{t('cpu')}</th><th className="numeric-column">{t('memory')}</th></tr></thead><tbody>{data.nodes.map((node) => <tr key={node.name}><td className="primary-cell">{node.name}</td><td><Status value={node.status} t={t} /></td><td className="numeric-column">{formatPercent(node.cpu_usage, locale)} · {node.cpu_count}</td><td className="numeric-column">{formatMemory(node.memory_used, locale)} / {formatMemory(node.memory_total, locale)}</td></tr>)}</tbody></table></div>
      </section>
      <section className="section" aria-labelledby="storage-title">
        <div className="section-heading"><h2 id="storage-title">{t('storage')} <span className="section-count">{data.storage.length}</span></h2></div>
        {data.storage.length === 0 ? <div className="inline-state">{t('noStorage')}</div> : <div className="table-scroll"><table><thead><tr><th data-column="name" data-fluid>{t('name')}</th><th>{t('type')}</th><th>{t('content')}</th><th>{t('scope')}</th></tr></thead><tbody>{data.storage.map((storage) => <tr key={`${storage.name}-${storage.kind}`}><td className="primary-cell">{storage.name}</td><td>{storage.kind}</td><td>{storage.content}</td><td>{storage.shared ? t('shared') : t('local')}</td></tr>)}</tbody></table></div>}
      </section>
    </>
  )
}

const buildStatusMessages: Record<ImageProfile['build_status'], MessageKey> = {
  draft: 'draft',
  blocked: 'blocked',
  building: 'building',
  testing: 'testing',
  ready: 'ready',
  failed: 'failed',
}

export function imageBuildStatusMessage(status: ImageProfile['build_status']): MessageKey {
  return buildStatusMessages[status]
}

export function ImageProfilesView({ data, t, onEdit }: { data: PlatformConfig; t: Translator; onEdit: (profile: ImageProfile, trigger: HTMLButtonElement) => void }) {
  if (data.image_profiles.length === 0) return <EmptyState title={t('noImageProfiles')} />
  return <section className="section" aria-labelledby="image-profiles-title"><div className="section-heading"><h2 id="image-profiles-title">{t('imageProfiles')} <span className="section-count">{data.image_profiles.length}</span></h2></div><div className="table-scroll"><table><thead><tr><th data-column="os" data-fluid>{t('operatingSystem')}</th><th>{t('status')}</th><th>{t('templateVMID')}</th><th>{t('guestAgent')}</th><th>{t('configuration')}</th></tr></thead><tbody>{data.image_profiles.map((profile) => <tr key={profile.id}><td className="primary-cell"><span className="truncate" title={profile.display_name}>{profile.display_name}</span></td><td><span className="status" data-status={profile.build_status}><span className="status-mark" aria-hidden="true" />{t(buildStatusMessages[profile.build_status])}{profile.lifecycle === 'legacy' ? ` · ${t('legacy')}` : ''}</span></td><td>{profile.template_vmid ?? '—'}</td><td>{profile.agent_kind}</td><td><button className="table-action" type="button" disabled={profile.build_status === 'building'} onClick={(event) => onEdit(profile, event.currentTarget)} aria-label={`${t('edit')} ${profile.display_name}`} title={t('edit')}><Pencil aria-hidden="true" /></button></td></tr>)}</tbody></table></div></section>
}

export function GPUProfilesView({ data, t, onEdit }: { data: PlatformConfig; t: Translator; onEdit: (profile: GPUProfile, trigger: HTMLButtonElement) => void }) {
  const deviceMatches = (profile: GPUProfile) => data.gpu_devices.filter((device) => (!profile.vendor_id || device.vendor_id === profile.vendor_id) && (!profile.device_class || device.class === profile.device_class))
  return <><section className="section" aria-labelledby="gpu-profiles-title"><div className="section-heading"><h2 id="gpu-profiles-title">{t('gpuProfile')} <span className="section-count">{data.gpu_profiles.length}</span></h2></div>{data.gpu_profiles.length === 0 ? <div className="inline-state">{t('noGPUProfiles')}</div> : <div className="table-scroll"><table><thead><tr><th data-column="profile" data-fluid>{t('gpuProfile')}</th><th>{t('mode')}</th><th>{t('resourceMapping')}</th><th>{t('availability')}</th><th>{t('status')}</th><th>{t('configuration')}</th></tr></thead><tbody>{data.gpu_profiles.map((profile) => {
    const matching = deviceMatches(profile)
    const mapping = data.pci_resource_mappings.find((item) => item.id === profile.resource_mapping)
    const mapped = matching.filter((device) => mapping?.entries.some((entry) => entry.node === device.node && mappingEntryHasDevice(entry, device.id)))
    const available = profile.mode === 'mdev' ? mapped.reduce((count, device) => count + ((device.mdev_types ?? []).find((kind) => kind.type === profile.mdev_type)?.available ?? 0), 0) : mapped.filter((device) => device.assignable).length
    return <tr key={profile.id}><td className="primary-cell">{profile.display_name}</td><td>{profile.mode === 'none' ? '—' : profile.mode === 'mdev' ? profile.mdev_type : profile.mode.replaceAll('_', ' ')}</td><td>{profile.mode === 'none' ? '—' : profile.resource_mapping || '—'}</td><td>{profile.mode === 'none' ? '—' : profile.mode === 'mdev' ? t('availableInstances', { count: String(available) }) : `${available} / ${matching.length}`}</td><td>{profile.enabled ? t('enabled') : t('disabled')}{profile.exclusive ? ` · ${t('exclusive')}` : ''}</td><td>{profile.mode !== 'none' && <button className="table-action" type="button" onClick={(event) => onEdit(profile, event.currentTarget)} aria-label={`${t('edit')} ${profile.display_name}`} title={t('edit')}><Pencil aria-hidden="true" /></button>}</td></tr>
  })}</tbody></table></div>}</section><section className="section" aria-labelledby="gpu-devices-title"><div className="section-heading"><h2 id="gpu-devices-title">{t('detectedDevices')} <span className="section-count">{data.gpu_devices.length}</span></h2></div>{!data.gpu_inventory_available ? <div className="inline-state" role="status">{t('inventoryUnavailable')}</div> : data.gpu_devices.length === 0 ? <div className="inline-state">{t('noGPUDevices')}</div> : <div className="table-scroll"><table><thead><tr><th>{t('node')}</th><th data-column="device" data-fluid>{t('device')}</th><th>ID</th><th>{t('availability')}</th></tr></thead><tbody>{data.gpu_devices.map((device) => <tr key={`${device.node}-${device.id}`}><td className="primary-cell">{device.node}</td><td>{device.device_name || device.vendor_name}</td><td>{device.id}</td><td>{device.mdev_types.length > 0 ? device.mdev_types.map((kind) => `${kind.name || kind.type} · ${kind.available}`).join(' / ') : device.assignable ? t('assignable') : t('unavailableDevice')}</td></tr>)}</tbody></table></div>}</section></>
}

export function eligibleNodesForGPU(data: Infrastructure, platform: PlatformConfig, profileID: string): Infrastructure['nodes'] {
  const online = data.nodes.filter((node) => node.status === 'online')
  const profile = platform.gpu_profiles.find((item) => item.id === profileID)
  if (!profile || profile.mode === 'none') return online
  const mapping = platform.pci_resource_mappings.find((item) => item.id === profile.resource_mapping)
  if (!mapping || Boolean(mapping.mdev) !== (profile.mode === 'mdev')) return []
  return online.filter((node) => mapping.entries.some((entry) => {
    if (entry.node !== node.name) return false
    return platform.gpu_devices.some((device) => {
      const available = profile.mode === 'mdev' ? device.mdev_types.some((kind) => kind.type === profile.mdev_type && kind.available > 0) : device.assignable
      return device.node === node.name && mappingEntryHasDevice(entry, device.id) && available && (!profile.vendor_id || device.vendor_id === profile.vendor_id) && (!profile.device_class || device.class === profile.device_class)
    })
  }))
}

export function mappingEntryHasDevice(entry: PlatformConfig['pci_resource_mappings'][number]['entries'][number], deviceID: string): boolean {
  const contains = (path: string) => path === deviceID || (!path.slice(path.lastIndexOf(':') + 1).includes('.') && deviceID.startsWith(`${path}.`))
  return contains(entry.device_id) || (entry.device_paths ?? []).some(contains)
}

export function Status({ value, t }: { value: string; t: Translator }) {
  const normalized = value === 'online' || value === 'running' ? 'online' : value === 'stopped' ? 'stopped' : 'unknown'
  const label: Record<typeof normalized, MessageKey> = { online: 'online', stopped: 'stopped', unknown: 'unknown' }
  return <span className="status" data-status={normalized}><span className="status-mark" aria-hidden="true" />{t(label[normalized])}</span>
}

function formatPercent(value: number, locale: Locale): string {
  return new Intl.NumberFormat(locale, { style: 'percent', maximumFractionDigits: 0 }).format(value)
}

function formatMemory(bytes: number, locale: Locale): string {
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(bytes / 1024 / 1024 / 1024)} GB`
}

function formatDateTime(value: string, locale: Locale): string {
  return new Intl.DateTimeFormat(locale, { dateStyle: 'short', timeStyle: 'short' }).format(new Date(value))
}

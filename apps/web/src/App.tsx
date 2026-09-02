import { useCallback, useEffect, useRef, useState } from 'react'
import {
  Hammer,
  LogOut,
  Plus,
  RefreshCw,
} from 'lucide-react'
import { changePower, createAgentPrincipal, createDesktop, createInitialAdmin, createLocalUser, createPCIResourceMapping, getAccessControl, getDesktopAccessPolicy, getInfrastructure, getJob, getJobs, getMe, getPlatformConfig, getSystem, login, logout, reconcileAccessControl, rotateAgentToken, setDesktopAssignment, startImageBuild, updateAgentPrincipal, updateDesktopAccessPolicy, updateGPUProfile, updateImageProfile, updateUser, type AccessControl, type DesktopAccessPolicy, type GPUProfile, type ImageProfile, type Infrastructure, type Job, type PCIResourceMapping, type PCIResourceMappingInput, type PlatformConfig, type Session, type SystemState } from './api'
import { message, type Locale, type MessageKey } from './i18n'
import { FormField } from './components/form-field'
import { ProductBrand, ThemeLocaleButtons } from './components/site-chrome'
import { ErrorState, LoadingState } from './components/states'
import { AuditView } from './console/audit'
import { AccessView } from './console/access'
import { ActivityView, DesktopsView, GPUProfilesView, ImageProfilesView, InfrastructureView, WORKSPACES, eligibleNodesForGPU, imageBuildStatusMessage, managedDesktops, type ConsoleView, type Translator } from './console/workspaces'
import { isConsolePath, LandingPage } from './landing'

export { eligibleNodesForGPU } from './console/workspaces'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'ready'; data: Infrastructure }

type PlatformLoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'ready'; data: PlatformConfig }

type JobLoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'ready'; data: Job[] }

type AccessLoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'ready'; data: AccessControl }

type BootState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'ready'; system: SystemState; session: Session | null }

function initialLocale(): Locale {
  return document.documentElement.lang === 'en' ? 'en' : 'zh-CN'
}

function currentTheme(): 'light' | 'dark' {
  const explicit = document.documentElement.dataset.theme
  if (explicit === 'light' || explicit === 'dark') return explicit
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

type PreferenceProps = {
  locale: Locale
  theme: 'light' | 'dark'
  t: (key: MessageKey, variables?: Record<string, string>) => string
  switchLocale: () => void
  switchTheme: () => void
}

export function App() {
  const [locale, setLocale] = useState<Locale>(initialLocale)
  const [theme, setTheme] = useState<'light' | 'dark'>(currentTheme)
  const t = useCallback(
    (key: MessageKey, variables?: Record<string, string>) => message(locale, key, variables),
    [locale],
  )

  const switchLocale = useCallback(() => {
    const next = locale === 'zh-CN' ? 'en' : 'zh-CN'
    setLocale(next)
    document.documentElement.lang = next
    localStorage.setItem('vc-vdi-locale', next)
  }, [locale])

  const switchTheme = useCallback(() => {
    const next = theme === 'dark' ? 'light' : 'dark'
    setTheme(next)
    document.documentElement.dataset.theme = next
    document.documentElement.dataset.themeMode = next
    document.documentElement.style.colorScheme = next
    localStorage.setItem('vc-vdi-theme', next)
  }, [theme])

  const preferences = { locale, theme, t, switchLocale, switchTheme }
  return isConsolePath(window.location.pathname)
    ? <ConsoleApp {...preferences} />
    : <LandingPage locale={locale} theme={theme} t={t} onLocale={switchLocale} onTheme={switchTheme} />
}

function ConsoleApp({ locale, theme, t, switchLocale, switchTheme }: PreferenceProps) {
  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [platformState, setPlatformState] = useState<PlatformLoadState>({ kind: 'loading' })
  const [jobState, setJobState] = useState<JobLoadState>({ kind: 'loading' })
  const [accessState, setAccessState] = useState<AccessLoadState>({ kind: 'loading' })
  const [boot, setBoot] = useState<BootState>({ kind: 'loading' })
  const [view, setView] = useState<ConsoleView>('desktops')
	const [createOpen, setCreateOpen] = useState(false)
	const createDesktopTrigger = useRef<HTMLButtonElement | null>(null)
	const [editingImage, setEditingImage] = useState<ImageProfile | null>(null)
	const editingImageTrigger = useRef<HTMLButtonElement | null>(null)
	const [editingGPU, setEditingGPU] = useState<GPUProfile | null>(null)
	const editingGPUTrigger = useRef<HTMLButtonElement | null>(null)
	const [editingAccess, setEditingAccess] = useState<Infrastructure['virtual_machines'][number] | null>(null)
	const editingAccessTrigger = useRef<HTMLButtonElement | null>(null)
  const [activeJob, setActiveJob] = useState<Job | null>(null)
	const [activeImageBuild, setActiveImageBuild] = useState<Job | null>(null)
  const [operationError, setOperationError] = useState('')
	  const load = useCallback(async () => {
    if (boot.kind !== 'ready' || !boot.session) return
		const admin = boot.session.user.role === 'platform_admin'
	    setState({ kind: 'loading' })
		if (admin) {
			setPlatformState({ kind: 'loading' })
			setJobState({ kind: 'loading' })
			setAccessState({ kind: 'loading' })
		}
		const requests: Promise<unknown>[] = [
      getInfrastructure()
        .then((data) => setState({ kind: 'ready', data }))
        .catch((error) => setState({ kind: 'error', message: error instanceof Error ? error.message : t('unavailable') })),
		]
		if (admin) requests.push(
			getPlatformConfig()
	        .then((data) => setPlatformState({ kind: 'ready', data }))
	        .catch((error) => setPlatformState({ kind: 'error', message: error instanceof Error ? error.message : t('unavailable') })),
	      getJobs()
	        .then((data) => setJobState({ kind: 'ready', data }))
	        .catch((error) => setJobState({ kind: 'error', message: error instanceof Error ? error.message : t('unavailable') })),
			getAccessControl()
				.then((data) => setAccessState({ kind: 'ready', data }))
				.catch((error) => setAccessState({ kind: 'error', message: error instanceof Error ? error.message : t('unavailable') })),
		)
		await Promise.all(requests)
  }, [boot, t])

  useEffect(() => {
    const controller = new AbortController()
    const initialize = async () => {
      try {
        const system = await getSystem(controller.signal)
        const session = system.initialized ? await getMe(controller.signal) : null
        setBoot({ kind: 'ready', system, session })
      } catch (error) {
        if (error instanceof DOMException && error.name === 'AbortError') return
        setBoot({ kind: 'error', message: error instanceof Error ? error.message : 'Unable to start VC Workspace' })
      }
    }
    void initialize()
    return () => controller.abort()
  }, [])

  useEffect(() => {
    if (boot.kind !== 'ready' || !boot.session) return
    const controller = new AbortController()
		const admin = boot.session.user.role === 'platform_admin'
		const requests: Promise<unknown>[] = [
      getInfrastructure(controller.signal)
        .then((data) => setState({ kind: 'ready', data }))
        .catch((error) => {
          if (error instanceof DOMException && error.name === 'AbortError') return
          setState({ kind: 'error', message: error instanceof Error ? error.message : t('unavailable') })
        }),
		]
		if (admin) requests.push(
			getPlatformConfig(controller.signal)
        .then((data) => setPlatformState({ kind: 'ready', data }))
        .catch((error) => {
          if (error instanceof DOMException && error.name === 'AbortError') return
	          setPlatformState({ kind: 'error', message: error instanceof Error ? error.message : t('unavailable') })
	        }),
	      getJobs(50, controller.signal)
	        .then((data) => setJobState({ kind: 'ready', data }))
	        .catch((error) => {
	          if (error instanceof DOMException && error.name === 'AbortError') return
	          setJobState({ kind: 'error', message: error instanceof Error ? error.message : t('unavailable') })
	        }),
			getAccessControl(controller.signal)
				.then((data) => setAccessState({ kind: 'ready', data }))
				.catch((error) => {
					if (error instanceof DOMException && error.name === 'AbortError') return
					setAccessState({ kind: 'error', message: error instanceof Error ? error.message : t('unavailable') })
				}),
		)
		void Promise.all(requests)
    return () => controller.abort()
  }, [boot, t])

	  const data = state.kind === 'ready' ? state.data : null
	  const platform = platformState.kind === 'ready' ? platformState.data : null
	  const jobs = jobState.kind === 'ready' ? jobState.data : null
	  const upsertJob = useCallback((job: Job) => {
	    setJobState((current) => current.kind !== 'ready' ? current : { kind: 'ready', data: [job, ...current.data.filter((item) => item.id !== job.id)].slice(0, 50) })
	  }, [])

	  useEffect(() => {
	    if (!activeJob || (activeJob.state !== 'accepted' && activeJob.state !== 'running')) return
    const controller = new AbortController()
    const timer = window.setInterval(async () => {
      try {
	        const next = await getJob(activeJob.id, controller.signal)
	        setActiveJob(next)
	        upsertJob(next)
	        if (next.state !== 'accepted' && next.state !== 'running') {
          window.clearInterval(timer)
          void load()
        }
      } catch {
        window.clearInterval(timer)
      }
    }, 1200)
    return () => { controller.abort(); window.clearInterval(timer) }
	  }, [activeJob, load, upsertJob])

	useEffect(() => {
		if (platformState.kind !== 'ready') return
		const running = (platformState.data.image_builds ?? []).find((job) => job.state === 'running' || job.state === 'accepted')
		if (running && activeImageBuild?.id !== running.id) setActiveImageBuild(running)
	}, [activeImageBuild?.id, platformState])

	useEffect(() => {
		if (!activeImageBuild || (activeImageBuild.state !== 'running' && activeImageBuild.state !== 'accepted')) return
		const controller = new AbortController()
		const timer = window.setInterval(async () => {
			try {
				const next = await getJob(activeImageBuild.id, controller.signal)
				setActiveImageBuild(next)
				upsertJob(next)
				if (next.state !== 'running' && next.state !== 'accepted') {
					window.clearInterval(timer)
					void load()
				}
			} catch {
				window.clearInterval(timer)
			}
		}, 1500)
		return () => { controller.abort(); window.clearInterval(timer) }
	}, [activeImageBuild, load, upsertJob])

  const authActions = <div className="auth-actions"><ThemeLocaleButtons t={t} theme={theme} onLocale={switchLocale} onTheme={switchTheme} /></div>

  if (boot.kind === 'loading') return <AuthShell actions={authActions} product={t('product')}><LoadingState label={t('loading')} /></AuthShell>
  if (boot.kind === 'error') return <AuthShell actions={authActions} product={t('product')}><ErrorState title={t('unavailable')} detail={boot.message} action={t('retry')} onRetry={() => window.location.reload()} /></AuthShell>
  if (!boot.system.database_configured) return <AuthShell actions={authActions} product={t('product')}><ErrorState title={t('databaseMissing')} detail={t('databaseMissingDetail')} action={t('retry')} onRetry={() => window.location.reload()} /></AuthShell>
  if (!boot.system.initialized) return <AuthShell actions={authActions} product={t('product')}><SetupForm t={t} onComplete={(session) => setBoot({ kind: 'ready', system: { ...boot.system, initialized: true }, session })} /></AuthShell>
  if (!boot.session) return <AuthShell actions={authActions} product={t('product')}><LoginForm system={boot.system} t={t} onComplete={(session) => setBoot({ ...boot, session })} /></AuthShell>
  const activeSession = boot.session
  const handleLogout = async () => {
    await logout(activeSession.csrf_token)
    setState({ kind: 'loading' })
    setPlatformState({ kind: 'loading' })
    setJobState({ kind: 'loading' })
    setAccessState({ kind: 'loading' })
    setActiveJob(null)
    setActiveImageBuild(null)
    setView('desktops')
    setBoot({ kind: 'ready', system: boot.system, session: null })
  }
  const runPowerAction = async (vmid: number, action: 'start' | 'stop') => {
    if (activeJob?.state === 'accepted' || activeJob?.state === 'running') return
    setOperationError('')
    try {
      const job = await changePower(vmid, action, activeSession.csrf_token)
      setActiveJob(job)
      upsertJob(job)
    } catch (error) {
      setOperationError(error instanceof Error ? error.message : t('unavailable'))
    }
  }
  const visibleWorkspaces = WORKSPACES.filter((workspace) => !workspace.adminOnly || activeSession.user.role === 'platform_admin')
  const activeWorkspace = visibleWorkspaces.find((workspace) => workspace.id === view) ?? visibleWorkspaces[0]
  const pageTitle = t(activeWorkspace.label)
  const loading = activeWorkspace.dataSource === 'infrastructure'
		? state.kind === 'loading'
		: activeWorkspace.dataSource === 'platform'
			? platformState.kind === 'loading'
			: activeWorkspace.dataSource === 'jobs'
				? jobState.kind === 'loading'
				: activeWorkspace.dataSource === 'access'
					? accessState.kind === 'loading'
					: false
  const hasManagedDesktops = data ? managedDesktops(data).length > 0 : false
  const persistImageProfile = async (profile: ImageProfile, input: Parameters<typeof updateImageProfile>[1]) => {
    const updated = await updateImageProfile(profile.id, input, activeSession.csrf_token)
    setPlatformState((current) => current.kind === 'ready' ? {
      kind: 'ready',
      data: { ...current.data, image_profiles: current.data.image_profiles.map((item) => item.id === updated.id ? updated : item) },
    } : current)
		return updated
  }
	const closeImageProfile = () => {
		setEditingImage(null)
		window.requestAnimationFrame(() => editingImageTrigger.current?.focus())
	}
	const saveImageProfile = async (profile: ImageProfile, input: Parameters<typeof updateImageProfile>[1]) => {
		await persistImageProfile(profile, input)
		closeImageProfile()
	}
	const saveAndBuildImage = async (profile: ImageProfile, input: Parameters<typeof updateImageProfile>[1], secrets: { builder_password: string; windows_product_key: string }) => {
		const job = await startImageBuild(profile.id, input, secrets, activeSession.csrf_token)
		setActiveImageBuild(job)
		upsertJob(job)
		closeImageProfile()
	}
	const saveGPUProfile = async (profile: GPUProfile, input: Parameters<typeof updateGPUProfile>[1]) => {
		const updated = await updateGPUProfile(profile.id, input, activeSession.csrf_token)
		setPlatformState((current) => current.kind === 'ready' ? {
			kind: 'ready',
			data: { ...current.data, gpu_profiles: current.data.gpu_profiles.map((item) => item.id === updated.id ? updated : item) },
		} : current)
		closeGPUProfile()
	}
	const createMapping = async (input: PCIResourceMappingInput): Promise<PCIResourceMapping> => {
		const created = await createPCIResourceMapping(input, activeSession.csrf_token)
		setPlatformState((current) => current.kind === 'ready' ? {
			kind: 'ready',
			data: { ...current.data, pci_resource_mappings: [...(current.data.pci_resource_mappings ?? []), created] },
		} : current)
		return created
	}
	const closeGPUProfile = () => {
		setEditingGPU(null)
		window.requestAnimationFrame(() => editingGPUTrigger.current?.focus())
	}
	const closeCreateDesktop = () => {
		setCreateOpen(false)
		window.requestAnimationFrame(() => createDesktopTrigger.current?.focus())
	}
	const openCreateDesktop = (trigger?: HTMLButtonElement) => {
		createDesktopTrigger.current = trigger ?? (document.activeElement instanceof HTMLButtonElement ? document.activeElement : null)
		setCreateOpen(true)
	}
	const closeDesktopAccess = () => {
		setEditingAccess(null)
		window.requestAnimationFrame(() => editingAccessTrigger.current?.focus())
	}

  const workspaceContent: Record<ConsoleView, React.ReactNode> = {
    desktops: state.kind === 'loading'
      ? <LoadingState label={t('loading')} />
      : state.kind === 'error'
        ? <ErrorState title={t('unavailable')} detail={state.message} action={t('retry')} onRetry={load} />
        : <DesktopsView data={state.data} locale={locale} activeJob={activeJob} t={t} onCreate={openCreateDesktop} onPower={(vmid, action) => void runPowerAction(vmid, action)} onPermissions={activeSession.user.role === 'platform_admin' ? (machine, trigger) => { editingAccessTrigger.current = trigger; setEditingAccess(machine) } : undefined} />,
    activity: jobState.kind === 'loading'
      ? <LoadingState label={t('loading')} />
      : jobState.kind === 'error'
        ? <ErrorState title={t('unavailable')} detail={jobState.message} action={t('retry')} onRetry={load} />
        : <ActivityView jobs={jobState.data} locale={locale} t={t} />,
    infrastructure: state.kind === 'loading'
      ? <LoadingState label={t('loading')} />
      : state.kind === 'error'
        ? <ErrorState title={t('unavailable')} detail={state.message} action={t('retry')} onRetry={load} />
        : <InfrastructureView data={state.data} locale={locale} t={t} />,
    images: platformState.kind === 'loading'
      ? <LoadingState label={t('loading')} />
      : platformState.kind === 'error'
        ? <ErrorState title={t('unavailable')} detail={platformState.message} action={t('retry')} onRetry={load} />
        : <ImageProfilesView data={platformState.data} t={t} onEdit={(profile, trigger) => { editingImageTrigger.current = trigger; setEditingImage(profile) }} />,
    gpu: platformState.kind === 'loading'
      ? <LoadingState label={t('loading')} />
      : platformState.kind === 'error'
        ? <ErrorState title={t('unavailable')} detail={platformState.message} action={t('retry')} onRetry={load} />
        : <GPUProfilesView data={platformState.data} t={t} onEdit={(profile, trigger) => { editingGPUTrigger.current = trigger; setEditingGPU(profile) }} />,
		access: accessState.kind === 'loading'
			? <LoadingState label={t('loadingAccess')} />
			: accessState.kind === 'error'
				? <ErrorState title={t('accessUnavailable')} detail={accessState.message} action={t('retry')} onRetry={load} />
				: <AccessView
					data={accessState.data}
					activeUserID={activeSession.user.id}
					locale={locale}
					t={t}
					onReconcile={async () => setAccessState({ kind: 'ready', data: await reconcileAccessControl(activeSession.csrf_token) })}
					onCreateUser={async (input) => {
						const user = await createLocalUser(input, activeSession.csrf_token)
						setAccessState((current) => current.kind === 'ready' ? { kind: 'ready', data: { ...current.data, users: [...current.data.users, user] } } : current)
					}}
					onCreateAgent={async (input) => {
						const credential = await createAgentPrincipal(input, activeSession.csrf_token)
						setAccessState((current) => current.kind === 'ready' ? { kind: 'ready', data: { ...current.data, agents: [...current.data.agents, credential.agent] } } : current)
						return credential
					}}
					onSetUserDisabled={async (user, disabled) => {
						const updated = await updateUser(user.id, disabled, activeSession.csrf_token)
						setAccessState((current) => current.kind === 'ready' ? { kind: 'ready', data: { ...current.data, users: current.data.users.map((item) => item.id === updated.id ? updated : item) } } : current)
					}}
					onSetAgentEnabled={async (agent, enabled) => {
						const updated = await updateAgentPrincipal(agent.id, enabled, activeSession.csrf_token)
						setAccessState((current) => current.kind === 'ready' ? { kind: 'ready', data: { ...current.data, agents: current.data.agents.map((item) => item.id === updated.id ? updated : item) } } : current)
					}}
					onRotateAgentToken={(agent) => rotateAgentToken(agent.id, activeSession.csrf_token)}
					onSetAssignment={async (subject, vmid, assigned) => {
						await setDesktopAssignment(subject.type, subject.id, vmid, assigned, activeSession.csrf_token)
						setAccessState((current) => current.kind !== 'ready' ? current : { kind: 'ready', data: { ...current.data, assignments: assigned
							? [...current.data.assignments, { subject_type: subject.type, subject_id: subject.id, desktop_vmid: vmid, created_at: new Date().toISOString() }]
							: current.data.assignments.filter((item) => !(item.subject_type === subject.type && item.subject_id === subject.id && item.desktop_vmid === vmid)) } })
					}}
				/>,
    audit: activeSession.user.role === 'platform_admin' ? <AuditView locale={locale} t={t} /> : null,
  }

  return (
    <div className="app-shell">
      <aside className="app-sidebar">
        <ProductBrand product={t('product')} className="sidebar-brand" />
        <nav className="console-nav" aria-label={t('configuration')}>
          {visibleWorkspaces.map((workspace) => {
            const Icon = workspace.icon
            return <button key={workspace.id} type="button" aria-current={activeWorkspace.id === workspace.id ? 'page' : undefined} onClick={() => setView(workspace.id)}><Icon aria-hidden /><span>{t(workspace.label)}</span></button>
          })}
        </nav>
        <div className="sidebar-footer">
          <span className="account-name" title={activeSession.user.display_name}>{activeSession.user.display_name}</span>
          <div className="sidebar-actions">
            <ThemeLocaleButtons t={t} theme={theme} onLocale={switchLocale} onTheme={switchTheme} />
            <button className="icon-button" type="button" onClick={() => void handleLogout()} aria-label={t('logout')} title={t('logout')}><LogOut aria-hidden="true" /></button>
          </div>
        </div>
      </aside>

      <main className="main">
        <header className="page-header">
          <div>
            <h1>{pageTitle}</h1>
            {activeWorkspace.showsClusterContext && data && <p className="page-context">{data.cluster.name} · {t('pveVersion', { version: data.version.version })} · {data.cluster.quorate ? t('quorumHealthy') : t('quorumUnhealthy')}</p>}
          </div>
          <div className="page-actions">
			{activeWorkspace.canCreateDesktop && data?.mutations_enabled && hasManagedDesktops && <button className="button button-primary" type="button" onClick={(event) => openCreateDesktop(event.currentTarget)}><Plus aria-hidden="true" />{t('createDesktop')}</button>}
            {activeWorkspace.hasGlobalRefresh !== false && <button className="button" type="button" onClick={() => { if (!loading) void load() }} aria-disabled={loading}><RefreshCw aria-hidden="true" data-spinning={loading} />{loading ? t('refreshing') : t('refresh')}</button>}
          </div>
        </header>

        {activeWorkspace.showsDesktopJob && activeJob && <div className="job-notice" data-state={activeJob.state} role="status"><span>{activeJob.state === 'accepted' || activeJob.state === 'running' ? t('jobRunning') : activeJob.state === 'succeeded' ? t('jobSucceeded') : t('jobFailed')}</span><code>{activeJob.target_vmid ? `VM ${activeJob.target_vmid}` : activeJob.id}</code>{activeJob.error && <span>{activeJob.error}</span>}</div>}
        {activeWorkspace.showsDesktopJob && operationError && <div className="job-notice" data-state="failed" role="alert">{operationError}</div>}
		{activeWorkspace.showsImageBuild && activeImageBuild && <div className="job-notice image-build-notice" data-state={activeImageBuild.state} role="status"><span>{activeImageBuild.state === 'accepted' || activeImageBuild.state === 'running' ? t('imageBuildRunning') : activeImageBuild.state === 'succeeded' ? t('imageBuildSucceeded') : t('imageBuildFailed')}</span><progress max={100} value={activeImageBuild.progress ?? 0} aria-label={t('imageBuildProgress')} /><span>{activeImageBuild.progress ?? 0}%</span>{activeImageBuild.detail && <span className="job-detail" title={activeImageBuild.detail}>{activeImageBuild.detail}</span>}{activeImageBuild.error && <span className="job-detail" title={activeImageBuild.error}>{activeImageBuild.error}</span>}</div>}

        {workspaceContent[activeWorkspace.id]}
		{data?.mutations_enabled && platform && <CreateDesktopDialog open={createOpen} data={data} platform={platform} t={t} onClose={closeCreateDesktop} onCreate={async (input) => { const job = await createDesktop(input, activeSession.csrf_token); setActiveJob(job); upsertJob(job); closeCreateDesktop() }} />}
		{editingImage && platform && <ImageProfileDialog profile={editingImage} gpuProfiles={platform.gpu_profiles} canBuild={platform.image_builder_available} t={t} onClose={closeImageProfile} onSave={(input) => saveImageProfile(editingImage, input)} onSaveAndBuild={(input, secrets) => saveAndBuildImage(editingImage, input, secrets)} />}
		{editingGPU && platform && <GPUProfileDialog profile={editingGPU} mappings={platform.pci_resource_mappings ?? []} devices={platform.gpu_devices} canMutate={Boolean(data?.mutations_enabled)} t={t} onClose={closeGPUProfile} onCreateMapping={createMapping} onSave={(input) => saveGPUProfile(editingGPU, input)} />}
		{editingAccess && <DesktopAccessDialog machine={editingAccess} t={t} onClose={closeDesktopAccess} onSave={(mode) => updateDesktopAccessPolicy(editingAccess.vmid, mode, activeSession.csrf_token)} />}
      </main>
    </div>
  )
}

function DesktopAccessDialog({ machine, t, onClose, onSave }: { machine: Infrastructure['virtual_machines'][number]; t: Translator; onClose: () => void; onSave: (mode: DesktopAccessPolicy['privilege_mode']) => Promise<DesktopAccessPolicy> }) {
	const [policy, setPolicy] = useState<DesktopAccessPolicy | null>(null)
	const [mode, setMode] = useState<DesktopAccessPolicy['privilege_mode']>('standard')
	const [busy, setBusy] = useState(false)
	const busyRef = useRef(false)
	const [error, setError] = useState('')
	useEffect(() => {
		const controller = new AbortController()
		void getDesktopAccessPolicy(machine.vmid, controller.signal).then((value) => {
			setPolicy(value)
			setMode(value.privilege_mode)
		}).catch((loadError) => {
			if (loadError instanceof DOMException && loadError.name === 'AbortError') return
			setError(loadError instanceof Error ? loadError.message : t('unavailable'))
		})
		return () => controller.abort()
	}, [machine.vmid, t])
	const dialogRef = (element: HTMLDialogElement | null) => {
		if (element && !element.open) element.showModal()
	}
	const submit = async (event: React.FormEvent) => {
		event.preventDefault()
		if (busyRef.current || !policy) return
		busyRef.current = true
		setBusy(true)
		setError('')
		try {
			const updated = await onSave(mode)
			setPolicy(updated)
			if (updated.state === 'failed') setError(t('permissionApplyFailed'))
		} catch (saveError) {
			setError(saveError instanceof Error ? saveError.message : t('unavailable'))
		} finally {
			busyRef.current = false
			setBusy(false)
		}
	}
	const stateLabel = policy?.state === 'applied' ? t('permissionApplied') : policy?.state === 'failed' ? t('permissionFailed') : t('permissionPending')
	return <dialog className="dialog" ref={dialogRef} onKeyDown={(event) => { if (event.key === 'Escape' && !busy) { event.preventDefault(); onClose() } }} onCancel={(event) => { event.preventDefault(); if (!busy) onClose() }} onClose={() => { if (!busy) onClose() }} aria-labelledby="desktop-access-title"><form onSubmit={submit} aria-describedby={error ? 'desktop-access-error' : 'desktop-access-detail'}><h2 id="desktop-access-title">{t('desktopPermissions')} · {machine.name}</h2>{!policy && !error ? <p className="field-hint" role="status">{t('loadingPermissions')}</p> : policy && <><label className="form-field"><span>{t('localPrivilege')}</span><select value={mode} onChange={(event) => setMode(event.target.value as DesktopAccessPolicy['privilege_mode'])}><option value="standard">{t('standardUser')}</option><option value="local_admin">{t('localAdministrator')}</option></select></label><p className="field-hint" id="desktop-access-detail">{mode === 'standard' ? t('standardUserDetail') : t('localAdministratorDetail')}</p><div className="policy-state"><span>{t('enforcement')}</span><strong>{stateLabel}</strong></div>{mode !== policy.privilege_mode && machine.status === 'running' && <p className="field-hint">{t('permissionChangeSignOut')}</p>}</>}{error && <p className="form-error" id="desktop-access-error" role="alert">{error}</p>}<div className="dialog-actions"><button className="button" type="button" onClick={onClose} aria-disabled={busy}>{t('cancel')}</button><button className="button button-primary" type="submit" disabled={busy || !policy}>{busy ? t('saving') : t('apply')}</button></div></form></dialog>
}

function GPUProfileDialog({ profile, mappings, devices, canMutate, t, onClose, onCreateMapping, onSave }: { profile: GPUProfile; mappings: PlatformConfig['pci_resource_mappings']; devices: PlatformConfig['gpu_devices']; canMutate: boolean; t: Translator; onClose: () => void; onCreateMapping: (input: PCIResourceMappingInput) => Promise<PCIResourceMapping>; onSave: (input: Parameters<typeof updateGPUProfile>[1]) => Promise<void> }) {
	const [busy, setBusy] = useState(false)
	const busyRef = useRef(false)
	const [error, setError] = useState('')
	const [values, setValues] = useState({ display_name: profile.display_name, resource_mapping: profile.resource_mapping ?? '', enabled: profile.enabled })
	const [mappingFormOpen, setMappingFormOpen] = useState(false)
	const [mappingID, setMappingID] = useState('')
	const mediated = profile.mode === 'mdev'
	const profileDevices = devices.filter((device) => {
		const vendorMatches = !profile.vendor_id || device.vendor_id === profile.vendor_id
		const classMatches = !profile.device_class || device.class === profile.device_class
		const capabilityMatches = mediated
			? (device.mdev_types ?? []).some((kind) => kind.type === profile.mdev_type)
			: device.assignable
		return vendorMatches && classMatches && capabilityMatches
	})
	const compatibleMappings = mappings.filter((mapping) => Boolean(mapping.mdev) === mediated)
	const deviceKey = (device: PlatformConfig['gpu_devices'][number]) => `${device.node}\n${device.id}`
	const [selectedDevice, setSelectedDevice] = useState(profileDevices[0] ? deviceKey(profileDevices[0]) : '')
	const dialogRef = (element: HTMLDialogElement | null) => {
		if (element && !element.open) element.showModal()
	}
	const beginAction = () => {
		if (busyRef.current) return false
		busyRef.current = true
		setBusy(true)
		setError('')
		return true
	}
	const finishAction = () => {
		busyRef.current = false
		setBusy(false)
	}
	const submit = async (event: React.FormEvent) => {
		event.preventDefault()
		if (!beginAction()) return
		try {
			await onSave(values)
		} catch (submitError) {
			setError(submitError instanceof Error ? submitError.message : t('unavailable'))
			finishAction()
		}
	}
	const createMappingFromDevice = async () => {
		const device = profileDevices.find((item) => deviceKey(item) === selectedDevice)
		const entries = mediated
			? profileDevices.map((item) => ({ node: item.node, device_id: item.id }))
			: device ? [{ node: device.node, device_id: device.id }] : []
		if (entries.length === 0 || !mappingID) return
		if (!beginAction()) return
		try {
			const created = await onCreateMapping({ id: mappingID, description: profile.display_name, mdev: mediated, entries })
			setValues((current) => ({ ...current, resource_mapping: created.id }))
			setMappingFormOpen(false)
		} catch (createError) {
			setError(createError instanceof Error ? createError.message : t('unavailable'))
		} finally {
			finishAction()
		}
	}
	const mappingExists = compatibleMappings.some((mapping) => mapping.id === values.resource_mapping)
	const mappingReady = mediated ? profileDevices.length > 0 : Boolean(selectedDevice)
	return (
		<dialog className="dialog" ref={dialogRef} onKeyDown={(event) => { if (event.key === 'Escape' && !busy) { event.preventDefault(); onClose() } }} onCancel={(event) => { event.preventDefault(); if (!busy) onClose() }} onClose={() => { if (!busy) onClose() }} aria-labelledby="edit-gpu-title">
			<form onSubmit={submit} aria-describedby={error ? 'edit-gpu-error' : undefined}>
				<h2 id="edit-gpu-title">{profile.display_name}</h2>
				<FormField label={t('name')} value={values.display_name} onChange={(value) => setValues({ ...values, display_name: value })} autoComplete="off" />
				{mediated && <div className="read-only-field"><span>{t('mdevType')}</span><code>{profile.mdev_type}</code></div>}
				<label className="form-field"><span>{t('resourceMapping')}</span><select value={values.resource_mapping} required={values.enabled} onChange={(event) => setValues({ ...values, resource_mapping: event.target.value })}><option value="">—</option>{values.resource_mapping && !mappingExists && <option value={values.resource_mapping}>{values.resource_mapping}</option>}{compatibleMappings.map((mapping) => <option key={mapping.id} value={mapping.id}>{mapping.id}</option>)}</select></label>
				{canMutate && <div className="inline-form">
					<button className="text-button" type="button" aria-expanded={mappingFormOpen} onClick={() => setMappingFormOpen((open) => !open)}>{t('createPVEMapping')}</button>
					{mappingFormOpen && <>
						<FormField label={t('mappingID')} value={mappingID} onChange={setMappingID} autoComplete="off" required={false} pattern="[A-Za-z][A-Za-z0-9_-]{0,63}" maxLength={64} />
						{mediated
							? <p className="field-hint">{t('mdevMappingNodes', { count: String(profileDevices.length) })}</p>
							: <label className="form-field"><span>{t('device')}</span><select value={selectedDevice} onChange={(event) => setSelectedDevice(event.target.value)}><option value="">—</option>{profileDevices.map((item) => <option key={deviceKey(item)} value={deviceKey(item)}>{item.node} · {item.device_name || item.vendor_name} · {item.id}</option>)}</select></label>}
						{profileDevices.length === 0 && <p className="field-hint">{mediated ? t('noMdevDevices') : t('noAssignableDevices')}</p>}
						<button className="button" type="button" aria-disabled={busy || !mappingID || !mappingReady} onClick={() => void createMappingFromDevice()}>{busy ? t('creating') : t('createPVEMapping')}</button>
					</>}
				</div>}
				<label className="check-field"><input type="checkbox" checked={values.enabled} onChange={(event) => setValues({ ...values, enabled: event.target.checked })} />{t('enabled')}</label>
				{error && <p className="form-error" id="edit-gpu-error" role="alert">{error}</p>}
				<div className="dialog-actions"><button className="button" type="button" onClick={onClose} aria-disabled={busy}>{t('cancel')}</button><button className="button button-primary" type="submit" aria-disabled={busy}>{busy ? t('saving') : t('save')}</button></div>
			</form>
		</dialog>
	)
}

function ImageProfileDialog({ profile, gpuProfiles, canBuild, t, onClose, onSave, onSaveAndBuild }: { profile: ImageProfile; gpuProfiles: GPUProfile[]; canBuild: boolean; t: Translator; onClose: () => void; onSave: (input: Parameters<typeof updateImageProfile>[1]) => Promise<void>; onSaveAndBuild: (input: Parameters<typeof updateImageProfile>[1], secrets: { builder_password: string; windows_product_key: string }) => Promise<void> }) {
  const [busy, setBusy] = useState(false)
  const busyRef = useRef(false)
  const [error, setError] = useState('')
  const [builderPassword, setBuilderPassword] = useState('')
  const [windowsProductKey, setWindowsProductKey] = useState('')
  const windows = profile.os_family === 'windows'
  const [values, setValues] = useState({
    display_name: profile.display_name,
    enabled: profile.enabled,
    source_node: profile.source_node,
    source_iso: profile.source_iso,
    source_iso_checksum: profile.source_iso_checksum,
    driver_iso: profile.driver_iso,
    driver_iso_checksum: profile.driver_iso_checksum,
    template_vmid: profile.template_vmid ? String(profile.template_vmid) : '',
    mirror_url: profile.mirror_url,
    security_mirror_url: profile.security_mirror_url,
    storage_pool: profile.storage_pool,
    bridge: profile.bridge,
    windows_image_name: profile.windows_image_name,
    default_cores: String(profile.default_cores),
    default_memory_mb: String(profile.default_memory_mb),
    default_disk_gb: String(profile.default_disk_gb),
    firmware: profile.firmware,
    tpm_version: profile.tpm_version,
    default_gpu_profile_id: profile.default_gpu_profile_id,
    build_status: profile.build_status,
    status_detail: profile.status_detail,
  })
  const dialogRef = (element: HTMLDialogElement | null) => {
    if (element && !element.open) element.showModal()
  }
  const begin = () => {
    if (busyRef.current) return false
    busyRef.current = true
    setBusy(true)
    setError('')
    return true
  }
  const finish = () => {
    busyRef.current = false
    setBusy(false)
  }
  const profileInput = (forBuild = false): Parameters<typeof updateImageProfile>[1] => ({
    ...values,
    template_vmid: values.template_vmid ? Number(values.template_vmid) : 0,
    default_cores: Number(values.default_cores),
    default_memory_mb: Number(values.default_memory_mb),
    default_disk_gb: Number(values.default_disk_gb),
    build_status: forBuild ? 'draft' : values.build_status,
    status_detail: forBuild ? '' : values.status_detail,
  })
  const submit = async (event: React.FormEvent) => {
    event.preventDefault()
    if (!begin()) return
    try {
      await onSave(profileInput())
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : t('unavailable'))
      finish()
    }
  }
  const build = async () => {
    if (!builderPassword) {
      setError(t('buildPasswordRequired'))
      return
    }
    if (!begin()) return
    try {
      await onSaveAndBuild(profileInput(true), { builder_password: builderPassword, windows_product_key: windowsProductKey })
    } catch (buildError) {
      setError(buildError instanceof Error ? buildError.message : t('unavailable'))
      finish()
    }
  }
  return (
    <dialog className="dialog dialog-wide" ref={dialogRef} onKeyDown={(event) => { if (event.key === 'Escape' && !busy) { event.preventDefault(); onClose() } }} onCancel={(event) => { event.preventDefault(); if (!busy) onClose() }} aria-labelledby="edit-image-title">
      <form onSubmit={submit} aria-describedby={error ? 'edit-image-error' : undefined}>
        <h2 id="edit-image-title">{profile.display_name}</h2>
        <FormField label={t('name')} value={values.display_name} onChange={(value) => setValues({ ...values, display_name: value })} autoComplete="off" />
        <label className="check-field"><input type="checkbox" checked={values.enabled} onChange={(event) => setValues({ ...values, enabled: event.target.checked })} />{t('enabled')}</label>

        <fieldset className="form-section">
          <legend>{t('buildSource')}</legend>
          <div className="form-grid">
            <FormField label={t('sourceNode')} value={values.source_node} onChange={(value) => setValues({ ...values, source_node: value })} autoComplete="off" required={false} />
            <FormField label={t('templateVMID')} type="number" min={0} value={values.template_vmid} onChange={(value) => setValues({ ...values, template_vmid: value })} autoComplete="off" required={false} />
            <FormField label={t('storagePool')} value={values.storage_pool} onChange={(value) => setValues({ ...values, storage_pool: value })} autoComplete="off" required={false} />
            <FormField label={t('networkBridge')} value={values.bridge} onChange={(value) => setValues({ ...values, bridge: value })} autoComplete="off" required={false} />
            <FormField label={t('sourceISO')} value={values.source_iso} onChange={(value) => setValues({ ...values, source_iso: value })} autoComplete="off" required={false} />
            <FormField label={t('sourceISOChecksum')} value={values.source_iso_checksum} onChange={(value) => setValues({ ...values, source_iso_checksum: value })} autoComplete="off" required={false} />
            {windows && <FormField label={t('driverISO')} value={values.driver_iso} onChange={(value) => setValues({ ...values, driver_iso: value })} autoComplete="off" required={false} />}
            {windows && <FormField label={t('driverISOChecksum')} value={values.driver_iso_checksum} onChange={(value) => setValues({ ...values, driver_iso_checksum: value })} autoComplete="off" required={false} />}
            {windows && <FormField label={t('windowsImageName')} value={values.windows_image_name} onChange={(value) => setValues({ ...values, windows_image_name: value })} autoComplete="off" required={false} maxLength={100} />}
          </div>
          {!windows && <div className="form-grid"><FormField label={t('mirrorURL')} type="url" value={values.mirror_url} onChange={(value) => setValues({ ...values, mirror_url: value })} autoComplete="url" required={false} /><FormField label={t('securityMirrorURL')} type="url" value={values.security_mirror_url} onChange={(value) => setValues({ ...values, security_mirror_url: value })} autoComplete="url" required={false} /></div>}
        </fieldset>

        <fieldset className="form-section">
          <legend>{t('hardware')}</legend>
          <div className="form-grid form-grid-three">
            <FormField label={t('cpuCores')} type="number" min={1} max={256} value={values.default_cores} onChange={(value) => setValues({ ...values, default_cores: value })} autoComplete="off" />
            <FormField label={t('memoryMiB')} type="number" min={512} max={1048576} value={values.default_memory_mb} onChange={(value) => setValues({ ...values, default_memory_mb: value })} autoComplete="off" />
            <FormField label={t('diskGiB')} type="number" min={8} max={16384} value={values.default_disk_gb} onChange={(value) => setValues({ ...values, default_disk_gb: value })} autoComplete="off" />
          </div>
          <div className="form-grid">
            <label className="form-field"><span>{t('firmware')}</span><select value={values.firmware} onChange={(event) => setValues({ ...values, firmware: event.target.value as ImageProfile['firmware'] })}><option value="seabios">SeaBIOS</option><option value="uefi">UEFI</option></select></label>
            <label className="form-field"><span>{t('tpm')}</span><select value={values.tpm_version} onChange={(event) => setValues({ ...values, tpm_version: event.target.value as ImageProfile['tpm_version'] })}><option value="none">{t('noTPM')}</option><option value="2.0">2.0</option></select></label>
          </div>
          <label className="form-field"><span>{t('defaultGPU')}</span><select value={values.default_gpu_profile_id} onChange={(event) => setValues({ ...values, default_gpu_profile_id: event.target.value })}>{gpuProfiles.filter((item) => item.enabled).map((item) => <option key={item.id} value={item.id}>{item.display_name}</option>)}</select></label>
        </fieldset>

        <fieldset className="form-section">
          <legend>{t('availability')}</legend>
          <label className="form-field"><span>{t('status')}</span><select value={values.build_status} onChange={(event) => setValues({ ...values, build_status: event.target.value as ImageProfile['build_status'] })}>{(['draft', 'blocked', 'building', 'testing', 'ready', 'failed'] as ImageProfile['build_status'][]).map((value) => <option key={value} value={value} disabled={value === 'building'}>{t(imageBuildStatusMessage(value))}</option>)}</select></label>
          <label className="form-field"><span>{t('statusDetail')}</span><textarea value={values.status_detail} onChange={(event) => setValues({ ...values, status_detail: event.target.value })} rows={3} maxLength={500} /></label>
        </fieldset>

        {canBuild && <fieldset className="form-section"><legend>{t('buildTemplate')}</legend><FormField label={t('buildPassword')} type="password" value={builderPassword} onChange={setBuilderPassword} autoComplete="new-password" required={false} minLength={12} maxLength={128} pattern="[A-Za-z0-9!@#$%^*()_+={}\\[\\]:,.?-]{12,128}" />{windows && <FormField label={t('windowsProductKey')} type="password" value={windowsProductKey} onChange={setWindowsProductKey} autoComplete="off" required={false} maxLength={100} pattern="[A-Za-z0-9-]{0,100}" />}<p className="field-hint">{t('buildSecretHint')}</p></fieldset>}
        {error && <p className="form-error" id="edit-image-error" role="alert">{error}</p>}
        <div className="dialog-actions"><button className="button" type="button" onClick={onClose} aria-disabled={busy}>{t('cancel')}</button><button className="button" type="submit" aria-disabled={busy}>{busy ? t('saving') : t('save')}</button>{canBuild && <button className="button button-primary" type="button" aria-disabled={busy} onClick={() => void build()}><Hammer aria-hidden="true" />{busy ? t('startingBuild') : t('buildTemplate')}</button>}</div>
      </form>
    </dialog>
  )
}

function CreateDesktopDialog({ open, data, platform, t, onClose, onCreate }: { open: boolean; data: Infrastructure; platform: PlatformConfig; t: (key: MessageKey) => string; onClose: () => void; onCreate: (input: Parameters<typeof createDesktop>[0]) => Promise<void> }) {
	const templateVMIDs = new Set(data.virtual_machines.filter((vm) => vm.template && vm.kind === 'qemu').map((vm) => vm.vmid))
	const images = platform.image_profiles.filter((profile) => profile.enabled && profile.build_status === 'ready' && profile.template_vmid && templateVMIDs.has(profile.template_vmid))
	const gpuProfiles = platform.gpu_profiles.filter((profile) => profile.enabled)
	const initialImage = images[0]
	const initialGPU = gpuProfiles.some((profile) => profile.id === initialImage?.default_gpu_profile_id) ? initialImage?.default_gpu_profile_id ?? 'none' : 'none'
	const initialNodes = eligibleNodesForGPU(data, platform, initialGPU)
	const [busy, setBusy] = useState(false)
	const [error, setError] = useState('')
	const [name, setName] = useState('vc-vdi-')
	const [sourceVMID, setSourceVMID] = useState(initialImage?.template_vmid ?? 0)
	const [gpuProfileID, setGPUProfileID] = useState(initialGPU)
	const [targetNode, setTargetNode] = useState(initialNodes[0]?.name ?? '')
	const nodes = eligibleNodesForGPU(data, platform, gpuProfileID)
  const dialogRef = (element: HTMLDialogElement | null) => {
    if (!element) return
    if (open && !element.open) element.showModal()
    if (!open && element.open) element.close()
  }
	const submit = async (event: React.FormEvent) => {
		event.preventDefault()
		if (busy || sourceVMID <= 0 || !targetNode) return
    setBusy(true)
    setError('')
    try {
			await onCreate({ name, source_vmid: sourceVMID, target_node: targetNode, storage: '', full: false, gpu_profile_id: gpuProfileID })
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : t('unavailable'))
      setBusy(false)
    }
  }
	const selectGPU = (profileID: string) => {
		setGPUProfileID(profileID)
		setTargetNode(eligibleNodesForGPU(data, platform, profileID)[0]?.name ?? '')
	}
	const selectImage = (vmid: number) => {
		setSourceVMID(vmid)
		const image = images.find((profile) => profile.template_vmid === vmid)
		const profileID = gpuProfiles.some((profile) => profile.id === image?.default_gpu_profile_id) ? image?.default_gpu_profile_id ?? 'none' : 'none'
		selectGPU(profileID)
	}
	return <dialog className="dialog" ref={dialogRef} onKeyDown={(event) => { if (event.key === 'Escape' && !busy) { event.preventDefault(); onClose() } }} onCancel={(event) => { event.preventDefault(); if (!busy) onClose() }} onClose={() => { if (!busy) onClose() }} aria-labelledby="create-desktop-title"><form onSubmit={submit} aria-describedby={error ? 'create-desktop-error' : undefined}><h2 id="create-desktop-title">{t('createDesktop')}</h2><FormField label={t('machineName')} value={name} onChange={setName} autoComplete="off" /><label className="form-field"><span>{t('sourceTemplate')}</span><select value={sourceVMID} required onChange={(event) => selectImage(Number(event.target.value))}>{images.map((profile) => <option key={profile.id} value={profile.template_vmid}>{profile.display_name} · {profile.template_vmid}</option>)}</select></label><label className="form-field"><span>{t('gpuSelection')}</span><select value={gpuProfileID} required onChange={(event) => selectGPU(event.target.value)}>{gpuProfiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.display_name}</option>)}</select></label><label className="form-field"><span>{t('targetNode')}</span><select value={targetNode} required onChange={(event) => setTargetNode(event.target.value)}>{nodes.map((node) => <option key={node.name} value={node.name}>{node.name}</option>)}</select></label>{error && <p className="form-error" id="create-desktop-error" role="alert">{error}</p>}<div className="dialog-actions"><button className="button" type="button" onClick={onClose} aria-disabled={busy}>{t('cancel')}</button><button className="button button-primary" type="submit" disabled={busy || sourceVMID <= 0 || !targetNode}>{busy ? t('creating') : t('create')}</button></div></form></dialog>
}

function AuthShell({ actions, children, product }: { actions: React.ReactNode; children: React.ReactNode; product: string }) {
  return <main className="auth-shell"><header>{actions}</header><div className="auth-content"><ProductBrand product={product} className="auth-brand" />{children}</div></main>
}

function SetupForm({ t, onComplete }: { t: (key: MessageKey) => string; onComplete: (session: Session) => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [values, setValues] = useState({ setup_token: '', username: '', display_name: '', password: '' })
  const submit = async (event: React.FormEvent) => {
    event.preventDefault()
    if (busy) return
    setBusy(true); setError('')
    try { onComplete(await createInitialAdmin(values)) } catch (submitError) { setError(submitError instanceof Error ? submitError.message : t('unavailable')); setBusy(false) }
  }
  return <form className="auth-form" onSubmit={submit} aria-describedby={error ? 'setup-error' : undefined}><div><h1>{t('setupTitle')}</h1><p className="form-intro">{t('setupIntro')}</p></div><FormField label={t('setupToken')} type="password" value={values.setup_token} onChange={(value) => setValues({ ...values, setup_token: value })} autoComplete="off" /><FormField label={t('username')} value={values.username} onChange={(value) => setValues({ ...values, username: value })} autoComplete="username" /><FormField label={t('displayName')} value={values.display_name} onChange={(value) => setValues({ ...values, display_name: value })} autoComplete="name" /><FormField label={t('password')} type="password" value={values.password} onChange={(value) => setValues({ ...values, password: value })} autoComplete="new-password" />{error && <p className="form-error" id="setup-error" role="alert">{error}</p>}<button className="button button-primary" type="submit" aria-disabled={busy}>{busy ? t('creatingAdmin') : t('createAdmin')}</button></form>
}

function LoginForm({ system, t, onComplete }: { system: SystemState; t: (key: MessageKey, variables?: Record<string, string>) => string; onComplete: (session: Session) => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [values, setValues] = useState({ username: '', password: '' })
  const submit = async (event: React.FormEvent) => {
    event.preventDefault()
    if (busy) return
    setBusy(true); setError('')
    try { onComplete(await login(values)) } catch (submitError) { setError(submitError instanceof Error ? submitError.message : t('unavailable')); setBusy(false) }
  }
  return <form className="auth-form" onSubmit={submit} aria-label={t('login')} aria-describedby={error ? 'login-error' : undefined}>{system.oidc_configured && <><a className="button sso-button" href="/api/v1/auth/oidc/start">{t('continueWithSSO', { provider: system.oidc_name })}</a><div className="auth-divider"><span>{t('or')}</span></div></>}<FormField label={t('username')} value={values.username} onChange={(value) => setValues({ ...values, username: value })} autoComplete="username" /><FormField label={t('password')} type="password" value={values.password} onChange={(value) => setValues({ ...values, password: value })} autoComplete="current-password" />{error && <p className="form-error" id="login-error" role="alert">{error}</p>}<button className="button button-primary" type="submit" aria-disabled={busy}>{busy ? t('loggingIn') : t('login')}</button></form>
}

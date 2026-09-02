import { useEffect, useMemo, useRef, useState } from 'react'
import { KeyRound, Plus, RefreshCw } from 'lucide-react'
import type { AccessControl, AgentCredential, AgentPrincipal, DesktopAssignment, UserAccount } from '../api'
import { FormField } from '../components/form-field'
import { EmptyState } from '../components/states'
import type { Locale } from '../i18n'
import type { Translator } from './workspaces'

type Subject = {
  type: DesktopAssignment['subject_type']
  id: string
  name: string
  disabled: boolean
}

type AccessViewProps = {
  data: AccessControl
  activeUserID: string
  locale: Locale
  t: Translator
  onReconcile: () => Promise<void>
  onCreateUser: (input: { username: string; display_name: string; password: string }) => Promise<void>
  onCreateAgent: (input: { id: string; display_name: string }) => Promise<AgentCredential>
  onSetUserDisabled: (user: UserAccount, disabled: boolean) => Promise<void>
  onSetAgentEnabled: (agent: AgentPrincipal, enabled: boolean) => Promise<void>
  onRotateAgentToken: (agent: AgentPrincipal) => Promise<AgentCredential>
  onSetAssignment: (subject: Subject, vmid: number, assigned: boolean) => Promise<void>
}

const subjectKey = (subject: Pick<Subject, 'type' | 'id'>) => `${subject.type}\u0000${subject.id}`

export function AccessView({ data, activeUserID, locale, t, onReconcile, onCreateUser, onCreateAgent, onSetUserDisabled, onSetAgentEnabled, onRotateAgentToken, onSetAssignment }: AccessViewProps) {
  const [createUserOpen, setCreateUserOpen] = useState(false)
  const [createAgentOpen, setCreateAgentOpen] = useState(false)
  const [rotatingAgent, setRotatingAgent] = useState<AgentPrincipal | null>(null)
  const [credential, setCredential] = useState<AgentCredential | null>(null)
  const [selectedSubjectKey, setSelectedSubjectKey] = useState('')
  const [busyKey, setBusyKey] = useState('')
  const [error, setError] = useState('')
  const createUserTrigger = useRef<HTMLButtonElement | null>(null)
  const createAgentTrigger = useRef<HTMLButtonElement | null>(null)
  const rotateAgentTrigger = useRef<HTMLButtonElement | null>(null)
  const credentialReturnFocus = useRef<HTMLButtonElement | null>(null)

  const subjects = useMemo<Subject[]>(() => [
    ...data.users.map((user) => ({ type: 'user' as const, id: user.id, name: user.display_name || user.username, disabled: user.disabled })),
    ...data.agents.map((agent) => ({ type: 'agent' as const, id: agent.id, name: agent.display_name, disabled: !agent.enabled })),
  ], [data.agents, data.users])
  const selectedSubject = subjects.find((subject) => subjectKey(subject) === selectedSubjectKey) ?? subjects[0]
  const assignedVMIDs = useMemo(() => new Set(data.assignments
    .filter((assignment) => selectedSubject && assignment.subject_type === selectedSubject.type && assignment.subject_id === selectedSubject.id)
    .map((assignment) => assignment.desktop_vmid)), [data.assignments, selectedSubject])

  useEffect(() => {
    if (!selectedSubjectKey && subjects[0]) setSelectedSubjectKey(subjectKey(subjects[0]))
    if (selectedSubjectKey && !subjects.some((subject) => subjectKey(subject) === selectedSubjectKey)) {
      setSelectedSubjectKey(subjects[0] ? subjectKey(subjects[0]) : '')
    }
  }, [selectedSubjectKey, subjects])

  const run = async (key: string, action: () => Promise<void>) => {
    if (busyKey) return
    setBusyKey(key)
    setError('')
    try {
      await action()
    } catch (actionError) {
      setError(actionError instanceof Error ? actionError.message : t('unavailable'))
    } finally {
      setBusyKey('')
    }
  }

  return <>
    {error && <div className="job-notice" data-state="failed" role="alert">{error}</div>}

    <section className="section" aria-labelledby="users-title">
      <div className="section-heading section-heading-with-tools">
        <h2 id="users-title">{t('users')} <span className="section-count">{data.users.length}</span></h2>
        <button ref={createUserTrigger} className="button" type="button" onClick={() => setCreateUserOpen(true)}><Plus aria-hidden="true" />{t('addLocalUser')}</button>
      </div>
      <div className="table-scroll"><table><thead><tr><th data-fluid>{t('name')}</th><th>{t('username')}</th><th>{t('identity')}</th><th>{t('role')}</th><th>{t('status')}</th><th>{t('actions')}</th></tr></thead><tbody>{data.users.map((user) => <tr key={user.id}><td className="primary-cell">{user.display_name}</td><td>{user.username}</td><td>{user.identity_kind === 'local' ? t('localAccount') : t('oidcAccount')}</td><td>{user.role === 'platform_admin' ? t('administrator') : t('userRole')}</td><td>{user.disabled ? t('disabled') : t('enabled')}</td><td>{user.id === activeUserID ? '—' : <button className="text-button" type="button" disabled={Boolean(busyKey)} onClick={() => void run(`user-state:${user.id}`, () => onSetUserDisabled(user, !user.disabled))}>{user.disabled ? t('enableAction') : t('disableAction')}</button>}</td></tr>)}</tbody></table></div>
    </section>

    <section className="section" aria-labelledby="agents-title">
      <div className="section-heading section-heading-with-tools">
        <h2 id="agents-title">{t('agents')} <span className="section-count">{data.agents.length}</span></h2>
        <button ref={createAgentTrigger} className="button" type="button" onClick={() => setCreateAgentOpen(true)}><Plus aria-hidden="true" />{t('addAgent')}</button>
      </div>
      {data.agents.length === 0 ? <EmptyState title={t('noAgents')} /> : <div className="table-scroll"><table><thead><tr><th data-fluid>{t('name')}</th><th>{t('agentID')}</th><th>{t('lastUsed')}</th><th>{t('status')}</th><th>{t('actions')}</th></tr></thead><tbody>{data.agents.map((agent) => <tr key={agent.id}><td className="primary-cell">{agent.display_name}</td><td><code>{agent.id}</code></td><td>{agent.last_used_at ? new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(agent.last_used_at)) : t('never')}</td><td>{agent.enabled ? t('enabled') : t('disabled')}</td><td><div className="table-actions"><button className="text-button" type="button" disabled={Boolean(busyKey)} onClick={() => void run(`agent-state:${agent.id}`, () => onSetAgentEnabled(agent, !agent.enabled))}>{agent.enabled ? t('disableAction') : t('enableAction')}</button><button className="table-action" type="button" aria-label={`${t('rotateToken')} · ${agent.display_name}`} title={t('rotateToken')} disabled={Boolean(busyKey)} onClick={(event) => { rotateAgentTrigger.current = event.currentTarget; setRotatingAgent(agent) }}><KeyRound aria-hidden="true" /></button></div></td></tr>)}</tbody></table></div>}
    </section>

    <section className="section" aria-labelledby="assignments-title">
      <div className="section-heading section-heading-with-tools access-assignment-heading">
        <h2 id="assignments-title">{t('desktopAssignments')}</h2>
        <div className="filters">
          <label className="filter-select"><span className="sr-only">{t('subject')}</span><select value={selectedSubject ? subjectKey(selectedSubject) : ''} disabled={subjects.length === 0} onChange={(event) => setSelectedSubjectKey(event.target.value)}><option value="" disabled>{t('selectSubject')}</option>{subjects.map((subject) => <option key={subjectKey(subject)} value={subjectKey(subject)} disabled={subject.disabled}>{subject.type === 'agent' ? `${t('agent')} · ` : ''}{subject.name}</option>)}</select></label>
          <button className="button" type="button" disabled={Boolean(busyKey)} onClick={() => void run('reconcile', onReconcile)}><RefreshCw aria-hidden="true" data-spinning={busyKey === 'reconcile'} />{t('reconcileDesktops')}</button>
        </div>
      </div>
      {subjects.length === 0 ? <EmptyState title={t('noAssignmentSubjects')} /> : data.desktops.length === 0 ? <EmptyState title={t('noRegisteredDesktops')} /> : <div className="table-scroll"><table><thead><tr><th data-fluid>{t('desktop')}</th><th>{t('vmid')}</th><th>{t('node')}</th><th>{t('status')}</th><th>{t('access')}</th></tr></thead><tbody>{data.desktops.map((desktop) => {
        const assigned = assignedVMIDs.has(desktop.vmid)
        const canAssign = Boolean(selectedSubject) && !selectedSubject?.disabled && desktop.present && desktop.enabled
        const actionKey = `assignment:${selectedSubjectKey}:${desktop.vmid}`
        return <tr key={desktop.vmid}><td className="primary-cell">{desktop.display_name}</td><td>{desktop.vmid}</td><td>{desktop.node || '—'}</td><td>{!desktop.present ? t('notInCluster') : desktop.enabled ? t('enabled') : t('disabled')}</td><td><button className="text-button" type="button" aria-pressed={assigned} disabled={Boolean(busyKey) || (!assigned && !canAssign)} onClick={() => selectedSubject && void run(actionKey, () => onSetAssignment(selectedSubject, desktop.vmid, !assigned))}>{assigned ? t('removeAssignment') : t('assignDesktop')}</button></td></tr>
      })}</tbody></table></div>}
    </section>

    {createUserOpen && <CreateUserDialog t={t} onClose={() => { setCreateUserOpen(false); window.requestAnimationFrame(() => createUserTrigger.current?.focus()) }} onCreate={onCreateUser} />}
    {createAgentOpen && <CreateAgentDialog t={t} onClose={() => { setCreateAgentOpen(false); window.requestAnimationFrame(() => createAgentTrigger.current?.focus()) }} onCreate={onCreateAgent} onCreated={(created) => { credentialReturnFocus.current = createAgentTrigger.current; setCreateAgentOpen(false); setCredential(created) }} />}
    {rotatingAgent && <MutationDialog title={`${t('rotateToken')} · ${rotatingAgent.display_name}`} submitLabel={t('rotateToken')} busyLabel={t('saving')} t={t} onClose={() => { setRotatingAgent(null); window.requestAnimationFrame(() => rotateAgentTrigger.current?.focus()) }} onSubmit={() => onRotateAgentToken(rotatingAgent)} onSubmitted={(created) => { credentialReturnFocus.current = rotateAgentTrigger.current; setRotatingAgent(null); setCredential(created as AgentCredential) }}><p className="field-hint">{t('rotateTokenHint')}</p></MutationDialog>}
    {credential && <CredentialDialog credential={credential} t={t} onClose={() => { setCredential(null); window.requestAnimationFrame(() => credentialReturnFocus.current?.focus()) }} />}
  </>
}

function CreateUserDialog({ t, onClose, onCreate }: { t: Translator; onClose: () => void; onCreate: AccessViewProps['onCreateUser'] }) {
  const [values, setValues] = useState({ username: '', display_name: '', password: '' })
  return <MutationDialog title={t('addLocalUser')} submitLabel={t('create')} t={t} onClose={onClose} onSubmit={() => onCreate(values)}>
    <FormField label={t('username')} value={values.username} onChange={(username) => setValues({ ...values, username })} autoComplete="username" pattern="[A-Za-z0-9][A-Za-z0-9._-]{2,63}" maxLength={64} />
    <FormField label={t('displayName')} value={values.display_name} onChange={(display_name) => setValues({ ...values, display_name })} autoComplete="name" required={false} maxLength={128} />
    <FormField label={t('password')} type="password" value={values.password} onChange={(password) => setValues({ ...values, password })} autoComplete="new-password" minLength={12} maxLength={128} />
  </MutationDialog>
}

function CreateAgentDialog({ t, onClose, onCreate, onCreated }: { t: Translator; onClose: () => void; onCreate: AccessViewProps['onCreateAgent']; onCreated: (credential: AgentCredential) => void }) {
  const [values, setValues] = useState({ id: '', display_name: '' })
  return <MutationDialog title={t('addAgent')} submitLabel={t('create')} t={t} onClose={onClose} onSubmit={() => onCreate(values)} onSubmitted={(created) => onCreated(created as AgentCredential)}>
    <FormField label={t('agentID')} value={values.id} onChange={(id) => setValues({ ...values, id })} autoComplete="off" pattern="[A-Za-z0-9][A-Za-z0-9._:-]{2,127}" maxLength={128} />
    <FormField label={t('displayName')} value={values.display_name} onChange={(display_name) => setValues({ ...values, display_name })} autoComplete="off" maxLength={128} />
  </MutationDialog>
}

function MutationDialog({ title, submitLabel, busyLabel, t, onClose, onSubmit, onSubmitted, children }: { title: string; submitLabel: string; busyLabel?: string; t: Translator; onClose: () => void; onSubmit: () => Promise<unknown>; onSubmitted?: (result: unknown) => void; children: React.ReactNode }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const busyRef = useRef(false)
  const dialogRef = (element: HTMLDialogElement | null) => { if (element && !element.open) element.showModal() }
  const submit = async (event: React.FormEvent) => {
    event.preventDefault()
    if (busyRef.current) return
    busyRef.current = true
    setBusy(true)
    setError('')
    try {
      const result = await onSubmit()
      if (onSubmitted) onSubmitted(result)
      else onClose()
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : t('unavailable'))
      busyRef.current = false
      setBusy(false)
    }
  }
  return <dialog className="dialog" ref={dialogRef} onKeyDown={(event) => { if (event.key === 'Escape' && !busy) { event.preventDefault(); onClose() } }} onCancel={(event) => { event.preventDefault(); if (!busy) onClose() }} aria-labelledby="mutation-dialog-title"><form onSubmit={submit}><h2 id="mutation-dialog-title">{title}</h2>{children}{error && <p className="form-error" role="alert">{error}</p>}<div className="dialog-actions"><button className="button" type="button" disabled={busy} onClick={onClose}>{t('cancel')}</button><button className="button button-primary" type="submit" disabled={busy}>{busy ? busyLabel ?? t('creating') : submitLabel}</button></div></form></dialog>
}

function CredentialDialog({ credential, t, onClose }: { credential: AgentCredential; t: Translator; onClose: () => void }) {
  const [copied, setCopied] = useState(false)
  const dialogRef = (element: HTMLDialogElement | null) => { if (element && !element.open) element.showModal() }
  return <dialog className="dialog" ref={dialogRef} onKeyDown={(event) => { if (event.key === 'Escape') { event.preventDefault(); onClose() } }} onCancel={(event) => { event.preventDefault(); onClose() }} aria-labelledby="agent-token-title"><div className="credential-dialog"><h2 id="agent-token-title">{t('agentToken')} · {credential.agent.display_name}</h2><p className="field-hint">{t('agentTokenHint')}</p><textarea className="credential-value" readOnly rows={4} value={credential.access_token} aria-label={t('agentToken')} onFocus={(event) => event.currentTarget.select()} /><div className="dialog-actions"><button className="button" type="button" onClick={async () => { try { await navigator.clipboard.writeText(credential.access_token); setCopied(true) } catch { setCopied(false) } }}>{copied ? t('copied') : t('copyToken')}</button><button className="button button-primary" type="button" onClick={onClose}>{t('done')}</button></div></div></dialog>
}

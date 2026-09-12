import { useEffect, useId, useMemo, useRef, useState } from 'react'
import { KeyRound, Pencil, Plus, RefreshCw } from 'lucide-react'
import type { AccessControl, AgentCredential, AgentPrincipal, APIToken, APITokenCredential, DesktopAssignment, IdentityGroup, IdentityProfile, IdentityProfileMode, ManagedDesktop, UserAccount } from '../api'
import { FormField } from '../components/form-field'
import { EmptyState } from '../components/states'
import type { Locale } from '../i18n'
import type { Translator } from './workspaces'

type Subject = { type: DesktopAssignment['subject_type']; id: string; name: string; disabled: boolean }
type AccessPanel = 'accounts' | 'groups' | 'desktops' | 'guestIdentity'

type AccessViewProps = {
  data: AccessControl
  activeUserID: string
  locale: Locale
  t: Translator
  onReconcile: () => Promise<void>
  onCreateUser: (input: { username: string; display_name: string; password: string }) => Promise<void>
  onCreateAgent: (input: { id: string; display_name: string }) => Promise<AgentCredential>
  onSetUserDisabled: (user: UserAccount, disabled: boolean) => Promise<void>
  onResetUserPassword: (user: UserAccount, password: string) => Promise<void>
  onCreateAPIToken: (input: { name: string; expires_in_days: number }) => Promise<APITokenCredential>
  onRevokeAPIToken: (token: APIToken) => Promise<void>
  onSetAgentEnabled: (agent: AgentPrincipal, enabled: boolean) => Promise<void>
  onRotateAgentToken: (agent: AgentPrincipal) => Promise<AgentCredential>
  onSetAssignment: (subject: Subject, vmid: number, assigned: boolean) => Promise<void>
  onCreateGroup: (input: { id: string; display_name: string; source: 'local'; enabled: boolean }) => Promise<void>
  onSetGroupEnabled: (group: IdentityGroup, enabled: boolean) => Promise<void>
  onSetGroupMembership: (groupID: string, userID: string, member: boolean) => Promise<void>
  onSetDesktopIdentity: (desktop: ManagedDesktop, input: { access_mode: ManagedDesktop['access_mode']; owner_user_id?: string; identity_profile_id?: string }) => Promise<void>
  onReconcileDesktopIdentity: (desktop: ManagedDesktop, input: { join_username?: string; join_password?: string; client_secret?: string }) => Promise<void>
  onPutIdentityProfile: (id: string, input: Pick<IdentityProfile, 'display_name' | 'platform' | 'mode' | 'enabled' | 'experimental' | 'config'>) => Promise<void>
}

const subjectKey = (subject: Pick<Subject, 'type' | 'id'>) => `${subject.type}\u0000${subject.id}`

export function AccessView(props: AccessViewProps) {
  const { data, activeUserID, locale, t } = props
  const [panel, setPanel] = useState<AccessPanel>('accounts')
  const [busyKey, setBusyKey] = useState('')
  const [error, setError] = useState('')
  const [createUserOpen, setCreateUserOpen] = useState(false)
  const [createAgentOpen, setCreateAgentOpen] = useState(false)
  const [createAPITokenOpen, setCreateAPITokenOpen] = useState(false)
  const [createGroupOpen, setCreateGroupOpen] = useState(false)
  const [editingDesktop, setEditingDesktop] = useState<ManagedDesktop | null>(null)
  const [reconcilingDesktop, setReconcilingDesktop] = useState<ManagedDesktop | null>(null)
  const [editingProfile, setEditingProfile] = useState<IdentityProfile | 'new' | null>(null)
  const [rotatingAgent, setRotatingAgent] = useState<AgentPrincipal | null>(null)
  const [resettingUser, setResettingUser] = useState<UserAccount | null>(null)
  const [credential, setCredential] = useState<AgentCredential | null>(null)
  const [apiCredential, setAPICredential] = useState<APITokenCredential | null>(null)
  const [selectedSubjectKey, setSelectedSubjectKey] = useState('')
  const [selectedGroupID, setSelectedGroupID] = useState('')
  const tabID = useId()
  const createUserTrigger = useRef<HTMLButtonElement | null>(null)
  const createAgentTrigger = useRef<HTMLButtonElement | null>(null)
  const createAPITokenTrigger = useRef<HTMLButtonElement | null>(null)
  const createGroupTrigger = useRef<HTMLButtonElement | null>(null)
  const createProfileTrigger = useRef<HTMLButtonElement | null>(null)
  const rotateAgentTrigger = useRef<HTMLButtonElement | null>(null)
  const resetUserTrigger = useRef<HTMLButtonElement | null>(null)
  const credentialReturnFocus = useRef<HTMLButtonElement | null>(null)

  const subjects = useMemo<Subject[]>(() => [
    ...data.users.map((user) => ({ type: 'user' as const, id: user.id, name: user.display_name || user.username, disabled: user.disabled })),
    ...data.groups.map((group) => ({ type: 'group' as const, id: group.id, name: group.display_name, disabled: !group.enabled })),
    ...data.agents.map((agent) => ({ type: 'agent' as const, id: agent.id, name: agent.display_name, disabled: !agent.enabled })),
  ], [data.agents, data.groups, data.users])
  const selectedSubject = subjects.find((subject) => subjectKey(subject) === selectedSubjectKey) ?? subjects[0]
  const selectedGroup = data.groups.find((group) => group.id === selectedGroupID) ?? data.groups[0]
  const assignedVMIDs = useMemo(() => new Set(data.assignments.filter((assignment) => selectedSubject && assignment.subject_type === selectedSubject.type && assignment.subject_id === selectedSubject.id).map((assignment) => assignment.desktop_vmid)), [data.assignments, selectedSubject])
  const groupMemberIDs = useMemo(() => new Set(data.group_memberships.filter((membership) => membership.group_id === selectedGroup?.id).map((membership) => membership.user_id)), [data.group_memberships, selectedGroup])
  const desktopControlByAgent = useMemo(() => new Map(data.agents.map((agent) => {
    const summary = (data.desktop_leases ?? []).filter((lease) => lease.agent_id === agent.id).map((lease) => t('activeDesktopLease', { vmid: lease.desktop_id, time: new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(lease.expires_at)) })).join(', ')
    return [agent.id, summary]
  })), [data.agents, data.desktop_leases, locale, t])

  useEffect(() => {
    if (!selectedSubjectKey && subjects[0]) setSelectedSubjectKey(subjectKey(subjects[0]))
    if (selectedSubjectKey && !subjects.some((subject) => subjectKey(subject) === selectedSubjectKey)) setSelectedSubjectKey(subjects[0] ? subjectKey(subjects[0]) : '')
  }, [selectedSubjectKey, subjects])
  useEffect(() => {
    if (!selectedGroupID && data.groups[0]) setSelectedGroupID(data.groups[0].id)
    if (selectedGroupID && !data.groups.some((group) => group.id === selectedGroupID)) setSelectedGroupID(data.groups[0]?.id ?? '')
  }, [data.groups, selectedGroupID])

  const run = async (key: string, action: () => Promise<void>) => {
    if (busyKey) return
    setBusyKey(key)
    setError('')
    try { await action() } catch (actionError) { setError(actionError instanceof Error ? actionError.message : t('unavailable')) } finally { setBusyKey('') }
  }
  const panels: Array<[AccessPanel, Parameters<Translator>[0]]> = [['accounts', 'accounts'], ['groups', 'groups'], ['desktops', 'desktopAccess'], ['guestIdentity', 'guestIdentity']]

  const moveTabFocus = (current: AccessPanel, direction: -1 | 1 | 'first' | 'last') => {
    const currentIndex = panels.findIndex(([id]) => id === current)
    const nextIndex = direction === 'first' ? 0 : direction === 'last' ? panels.length - 1 : (currentIndex + direction + panels.length) % panels.length
    const next = panels[nextIndex][0]
    setPanel(next)
    window.requestAnimationFrame(() => document.getElementById(`${tabID}-${next}-tab`)?.focus())
  }

  return <>
    <div className="section-tabs" role="tablist" aria-label={t('accessControl')}>{panels.map(([id, label]) => <button id={`${tabID}-${id}-tab`} key={id} type="button" role="tab" aria-selected={panel === id} aria-controls={`${tabID}-${id}`} tabIndex={panel === id ? 0 : -1} onClick={() => setPanel(id)} onKeyDown={(event) => { if (event.key === 'ArrowLeft') { event.preventDefault(); moveTabFocus(id, -1) } else if (event.key === 'ArrowRight') { event.preventDefault(); moveTabFocus(id, 1) } else if (event.key === 'Home') { event.preventDefault(); moveTabFocus(id, 'first') } else if (event.key === 'End') { event.preventDefault(); moveTabFocus(id, 'last') } }}>{t(label)}</button>)}</div>
    {error && <div className="job-notice" data-state="failed" role="alert">{error}</div>}
    <div id={`${tabID}-${panel}`} role="tabpanel" aria-labelledby={`${tabID}-${panel}-tab`}>
      {panel === 'accounts' && <AccountsPanel {...props} busyKey={busyKey} run={run} controlByAgent={desktopControlByAgent} createUserTrigger={createUserTrigger} createAgentTrigger={createAgentTrigger} createAPITokenTrigger={createAPITokenTrigger} rotateAgentTrigger={rotateAgentTrigger} resetUserTrigger={resetUserTrigger} openUser={() => setCreateUserOpen(true)} openAgent={() => setCreateAgentOpen(true)} openAPIToken={() => setCreateAPITokenOpen(true)} rotateAgent={setRotatingAgent} resetUser={setResettingUser} />}
      {panel === 'groups' && <GroupsPanel {...props} busyKey={busyKey} run={run} selectedGroup={selectedGroup} setSelectedGroupID={setSelectedGroupID} memberIDs={groupMemberIDs} createGroupTrigger={createGroupTrigger} openCreate={() => setCreateGroupOpen(true)} />}
      {panel === 'desktops' && <DesktopAccessPanel {...props} busyKey={busyKey} run={run} subjects={subjects} selectedSubject={selectedSubject} selectedSubjectKey={selectedSubjectKey} setSelectedSubjectKey={setSelectedSubjectKey} assignedVMIDs={assignedVMIDs} editDesktop={setEditingDesktop} reconcileDesktop={setReconcilingDesktop} />}
      {panel === 'guestIdentity' && <IdentityProfilesPanel data={data} t={t} createTrigger={createProfileTrigger} edit={setEditingProfile} />}
    </div>
    {createUserOpen && <CreateUserDialog t={t} onClose={() => { setCreateUserOpen(false); window.requestAnimationFrame(() => createUserTrigger.current?.focus()) }} onCreate={props.onCreateUser} />}
    {createAgentOpen && <CreateAgentDialog t={t} onClose={() => { setCreateAgentOpen(false); window.requestAnimationFrame(() => createAgentTrigger.current?.focus()) }} onCreate={props.onCreateAgent} onCreated={(created) => { credentialReturnFocus.current = createAgentTrigger.current; setCreateAgentOpen(false); setCredential(created) }} />}
    {createAPITokenOpen && <CreateAPITokenDialog t={t} onClose={() => { setCreateAPITokenOpen(false); window.requestAnimationFrame(() => createAPITokenTrigger.current?.focus()) }} onCreate={props.onCreateAPIToken} onCreated={(created) => { setCreateAPITokenOpen(false); setAPICredential(created) }} />}
    {createGroupOpen && <CreateGroupDialog t={t} onClose={() => { setCreateGroupOpen(false); window.requestAnimationFrame(() => createGroupTrigger.current?.focus()) }} onCreate={props.onCreateGroup} />}
    {editingDesktop && <DesktopIdentityDialog desktop={editingDesktop} data={data} t={t} onClose={() => setEditingDesktop(null)} onSave={(input) => props.onSetDesktopIdentity(editingDesktop, input)} />}
    {reconcilingDesktop && <IdentityReconcileDialog desktop={reconcilingDesktop} data={data} t={t} onClose={() => setReconcilingDesktop(null)} onReconcile={(input) => props.onReconcileDesktopIdentity(reconcilingDesktop, input)} />}
    {editingProfile && <IdentityProfileDialog profile={editingProfile === 'new' ? null : editingProfile} t={t} onClose={() => { setEditingProfile(null); window.requestAnimationFrame(() => createProfileTrigger.current?.focus()) }} onSave={props.onPutIdentityProfile} />}
    {rotatingAgent && <MutationDialog title={`${t('rotateToken')} · ${rotatingAgent.display_name}`} submitLabel={t('rotateToken')} busyLabel={t('saving')} t={t} onClose={() => { setRotatingAgent(null); window.requestAnimationFrame(() => rotateAgentTrigger.current?.focus()) }} onSubmit={() => props.onRotateAgentToken(rotatingAgent)} onSubmitted={(created) => { credentialReturnFocus.current = rotateAgentTrigger.current; setRotatingAgent(null); setCredential(created as AgentCredential) }}><p className="field-hint">{t('rotateTokenHint')}</p></MutationDialog>}
    {resettingUser && <ResetPasswordDialog user={resettingUser} t={t} onClose={() => { setResettingUser(null); window.requestAnimationFrame(() => resetUserTrigger.current?.focus()) }} onReset={props.onResetUserPassword} />}
    {credential && <CredentialDialog credential={credential} t={t} onClose={() => { setCredential(null); window.requestAnimationFrame(() => credentialReturnFocus.current?.focus()) }} />}
    {apiCredential && <APITokenCredentialDialog credential={apiCredential} t={t} onClose={() => { setAPICredential(null); window.requestAnimationFrame(() => createAPITokenTrigger.current?.focus()) }} />}
  </>
}

type PanelHelpers = { busyKey: string; run: (key: string, action: () => Promise<void>) => Promise<void> }

function AccountsPanel({ data, activeUserID, locale, t, onSetUserDisabled, onSetAgentEnabled, onRevokeAPIToken, busyKey, run, controlByAgent, createUserTrigger, createAgentTrigger, createAPITokenTrigger, rotateAgentTrigger, resetUserTrigger, openUser, openAgent, openAPIToken, rotateAgent, resetUser }: AccessViewProps & PanelHelpers & { controlByAgent: Map<string, string>; createUserTrigger: React.RefObject<HTMLButtonElement | null>; createAgentTrigger: React.RefObject<HTMLButtonElement | null>; createAPITokenTrigger: React.RefObject<HTMLButtonElement | null>; rotateAgentTrigger: React.MutableRefObject<HTMLButtonElement | null>; resetUserTrigger: React.MutableRefObject<HTMLButtonElement | null>; openUser: () => void; openAgent: () => void; openAPIToken: () => void; rotateAgent: (agent: AgentPrincipal) => void; resetUser: (user: UserAccount) => void }) {
  return <><section className="section" aria-labelledby="users-title"><div className="section-heading section-heading-with-tools"><h2 id="users-title">{t('users')} <span className="section-count">{data.users.length}</span></h2><button ref={createUserTrigger} className="button" type="button" onClick={openUser}><Plus aria-hidden="true" />{t('addLocalUser')}</button></div><div className="table-scroll"><table><thead><tr><th data-fluid>{t('name')}</th><th>{t('username')}</th><th>{t('identity')}</th><th>{t('role')}</th><th>{t('status')}</th><th>{t('actions')}</th></tr></thead><tbody>{data.users.map((user) => <tr key={user.id}><td className="primary-cell">{user.display_name}</td><td>{user.username}</td><td>{user.identity_kind === 'local' ? t('localAccount') : t('oidcAccount')}</td><td>{user.role === 'platform_admin' ? t('administrator') : t('userRole')}</td><td>{user.disabled ? t('disabled') : t('enabled')}</td><td>{user.id === activeUserID ? '—' : <div className="table-actions"><button className="text-button" type="button" disabled={Boolean(busyKey)} onClick={() => void run(`user:${user.id}`, () => onSetUserDisabled(user, !user.disabled))}>{user.disabled ? t('enableAction') : t('disableAction')}</button>{user.identity_kind === 'local' && <button className="table-action" type="button" aria-label={`${t('resetPassword')} · ${user.display_name}`} title={t('resetPassword')} disabled={Boolean(busyKey)} onClick={(event) => { resetUserTrigger.current = event.currentTarget; resetUser(user) }}><KeyRound aria-hidden="true" /></button>}</div>}</td></tr>)}</tbody></table></div></section><section className="section" aria-labelledby="agents-title"><div className="section-heading section-heading-with-tools"><h2 id="agents-title">{t('agents')} <span className="section-count">{data.agents.length}</span></h2><button ref={createAgentTrigger} className="button" type="button" onClick={openAgent}><Plus aria-hidden="true" />{t('addAgent')}</button></div>{data.agents.length === 0 ? <EmptyState title={t('noAgents')} /> : <div className="table-scroll"><table><thead><tr><th data-fluid>{t('name')}</th><th>{t('agentID')}</th><th>{t('desktopControl')}</th><th>{t('lastUsed')}</th><th>{t('status')}</th><th>{t('actions')}</th></tr></thead><tbody>{data.agents.map((agent) => { const control = controlByAgent.get(agent.id) || '—'; return <tr key={agent.id}><td className="primary-cell">{agent.display_name}</td><td><code>{agent.id}</code></td><td><span className="truncate" title={control}>{control}</span></td><td>{agent.last_used_at ? new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(agent.last_used_at)) : t('never')}</td><td>{agent.enabled ? t('enabled') : t('disabled')}</td><td><div className="table-actions"><button className="text-button" type="button" disabled={Boolean(busyKey)} onClick={() => void run(`agent:${agent.id}`, () => onSetAgentEnabled(agent, !agent.enabled))}>{agent.enabled ? t('disableAction') : t('enableAction')}</button><button className="table-action" type="button" aria-label={`${t('rotateToken')} · ${agent.display_name}`} title={t('rotateToken')} disabled={Boolean(busyKey)} onClick={(event) => { rotateAgentTrigger.current = event.currentTarget; rotateAgent(agent) }}><KeyRound aria-hidden="true" /></button></div></td></tr> })}</tbody></table></div>}</section><section className="section" aria-labelledby="api-tokens-title"><div className="section-heading section-heading-with-tools"><h2 id="api-tokens-title">{t('apiTokens')} <span className="section-count">{data.api_tokens.length}</span></h2><button ref={createAPITokenTrigger} className="button" type="button" onClick={openAPIToken}><Plus aria-hidden="true" />{t('createAPIToken')}</button></div>{data.api_tokens.length === 0 ? <EmptyState title={t('noAPITokens')} /> : <div className="table-scroll"><table><thead><tr><th data-fluid>{t('name')}</th><th>{t('expires')}</th><th>{t('lastUsed')}</th><th>{t('actions')}</th></tr></thead><tbody>{data.api_tokens.map((token) => <tr key={token.id}><td className="primary-cell">{token.name}<small className="cell-detail">{token.id}</small></td><td>{new Intl.DateTimeFormat(locale, { dateStyle: 'medium' }).format(new Date(token.expires_at))}</td><td>{token.last_used_at ? new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(token.last_used_at)) : t('never')}</td><td><button className="text-button" type="button" disabled={Boolean(busyKey)} onClick={() => void run(`api-token:${token.id}`, () => onRevokeAPIToken(token))}>{t('revoke')}</button></td></tr>)}</tbody></table></div>}</section></>
}

function GroupsPanel({ data, t, onSetGroupEnabled, onSetGroupMembership, busyKey, run, selectedGroup, setSelectedGroupID, memberIDs, createGroupTrigger, openCreate }: AccessViewProps & PanelHelpers & { selectedGroup?: IdentityGroup; setSelectedGroupID: (id: string) => void; memberIDs: Set<string>; createGroupTrigger: React.RefObject<HTMLButtonElement | null>; openCreate: () => void }) {
  return <><section className="section" aria-labelledby="groups-title"><div className="section-heading section-heading-with-tools"><h2 id="groups-title">{t('groups')} <span className="section-count">{data.groups.length}</span></h2><button ref={createGroupTrigger} className="button" type="button" onClick={openCreate}><Plus aria-hidden="true" />{t('addGroup')}</button></div>{data.groups.length === 0 ? <EmptyState title={t('noGroups')} /> : <div className="table-scroll"><table><thead><tr><th data-fluid>{t('name')}</th><th>{t('source')}</th><th>{t('members')}</th><th>{t('status')}</th><th>{t('actions')}</th></tr></thead><tbody>{data.groups.map((group) => <tr key={group.id}><td className="primary-cell">{group.display_name}</td><td>{group.source.toUpperCase()}</td><td>{group.member_count}</td><td>{group.enabled ? t('enabled') : t('disabled')}</td><td><button className="text-button" type="button" disabled={Boolean(busyKey)} onClick={() => void run(`group:${group.id}`, () => onSetGroupEnabled(group, !group.enabled))}>{group.enabled ? t('disableAction') : t('enableAction')}</button></td></tr>)}</tbody></table></div>}</section>{selectedGroup && <section className="section" aria-labelledby="group-members-title"><div className="section-heading section-heading-with-tools"><h2 id="group-members-title">{t('members')}</h2><label className="filter-select"><span className="sr-only">{t('groups')}</span><select value={selectedGroup.id} onChange={(event) => setSelectedGroupID(event.target.value)}>{data.groups.map((group) => <option key={group.id} value={group.id}>{group.display_name}</option>)}</select></label></div><div className="table-scroll"><table><thead><tr><th data-fluid>{t('name')}</th><th>{t('username')}</th><th>{t('membership')}</th></tr></thead><tbody>{data.users.map((user) => { const member = memberIDs.has(user.id); const managedByOIDC = data.group_memberships.some((item) => item.group_id === selectedGroup.id && item.user_id === user.id && item.source === 'oidc'); return <tr key={user.id}><td className="primary-cell">{user.display_name}</td><td>{user.username}</td><td><button className="text-button" type="button" aria-pressed={member} disabled={Boolean(busyKey) || selectedGroup.source === 'oidc' || managedByOIDC} onClick={() => void run(`member:${selectedGroup.id}:${user.id}`, () => onSetGroupMembership(selectedGroup.id, user.id, !member))}>{member ? t('removeMember') : t('addMember')}</button></td></tr> })}</tbody></table></div></section>}</>
}

function DesktopAccessPanel({ data, t, onReconcile, onSetAssignment, busyKey, run, subjects, selectedSubject, selectedSubjectKey, setSelectedSubjectKey, assignedVMIDs, editDesktop, reconcileDesktop }: AccessViewProps & PanelHelpers & { subjects: Subject[]; selectedSubject?: Subject; selectedSubjectKey: string; setSelectedSubjectKey: (key: string) => void; assignedVMIDs: Set<number>; editDesktop: (desktop: ManagedDesktop) => void; reconcileDesktop: (desktop: ManagedDesktop) => void }) {
  return <section className="section" aria-labelledby="assignments-title"><div className="section-heading section-heading-with-tools access-assignment-heading"><h2 id="assignments-title">{t('desktopAccess')}</h2><div className="filters"><label className="filter-select"><span className="sr-only">{t('subject')}</span><select value={selectedSubject ? subjectKey(selectedSubject) : ''} disabled={subjects.length === 0} onChange={(event) => setSelectedSubjectKey(event.target.value)}><option value="" disabled>{t('selectSubject')}</option>{subjects.map((subject) => <option key={subjectKey(subject)} value={subjectKey(subject)} disabled={subject.disabled}>{subject.type === 'agent' ? `${t('agent')} · ` : subject.type === 'group' ? `${t('group')} · ` : ''}{subject.name}</option>)}</select></label><button className="button" type="button" disabled={Boolean(busyKey)} onClick={() => void run('reconcile', onReconcile)}><RefreshCw aria-hidden="true" data-spinning={busyKey === 'reconcile'} />{t('reconcileDesktops')}</button></div></div>{subjects.length === 0 ? <EmptyState title={t('noAssignmentSubjects')} /> : data.desktops.length === 0 ? <EmptyState title={t('noRegisteredDesktops')} /> : <div className="table-scroll"><table><thead><tr><th data-fluid>{t('desktop')}</th><th>{t('accessMode')}</th><th>{t('owner')}</th><th>{t('guestIdentity')}</th><th>{t('access')}</th><th>{t('configuration')}</th></tr></thead><tbody>{data.desktops.map((desktop) => { const assigned = assignedVMIDs.has(desktop.vmid); const canAssign = Boolean(selectedSubject) && !selectedSubject?.disabled && desktop.present && desktop.enabled && (selectedSubject?.type === 'agent' || desktop.access_mode === 'shared' || (selectedSubject?.type === 'user' && (!desktop.owner_user_id || desktop.owner_user_id === selectedSubject.id))); const owner = data.users.find((user) => user.id === desktop.owner_user_id); const profile = data.identity_profiles.find((item) => item.id === desktop.identity_profile_id); return <tr key={desktop.vmid}><td className="primary-cell">{desktop.display_name}<small className="cell-detail">VM {desktop.vmid} · {desktop.node || '—'}</small></td><td>{desktop.access_mode === 'personal' ? t('personal') : t('shared')}</td><td>{owner?.display_name ?? '—'}</td><td>{profile?.display_name ?? t('managedLocal')}<small className="cell-detail">{identityStateLabel(desktop.identity_state, t)}</small></td><td><button className="text-button" type="button" aria-pressed={assigned} disabled={Boolean(busyKey) || (!assigned && !canAssign)} onClick={() => selectedSubject && void run(`assignment:${selectedSubjectKey}:${desktop.vmid}`, () => onSetAssignment(selectedSubject, desktop.vmid, !assigned))}>{assigned ? t('removeAssignment') : t('assignDesktop')}</button></td><td><div className="table-actions"><button className="table-action" type="button" aria-label={`${t('applyIdentity')} · ${desktop.display_name}`} title={t('applyIdentity')} onClick={() => reconcileDesktop(desktop)}><RefreshCw aria-hidden="true" /></button><button className="table-action" type="button" aria-label={`${t('edit')} · ${desktop.display_name}`} title={t('edit')} onClick={() => editDesktop(desktop)}><Pencil aria-hidden="true" /></button></div></td></tr> })}</tbody></table></div>}</section>
}

function identityStateLabel(state: ManagedDesktop['identity_state'], t: Translator): string {
  const labels: Record<ManagedDesktop['identity_state'], Parameters<Translator>[0]> = { pending: 'identityPending', applied: 'identityApplied', restart_required: 'identityRestartRequired', failed: 'identityFailed' }
  return t(labels[state])
}

function IdentityProfilesPanel({ data, t, createTrigger, edit }: { data: AccessControl; t: Translator; createTrigger: React.RefObject<HTMLButtonElement | null>; edit: (profile: IdentityProfile | 'new') => void }) {
  return <section className="section" aria-labelledby="identity-profiles-title"><div className="section-heading section-heading-with-tools"><h2 id="identity-profiles-title">{t('identityProfiles')} <span className="section-count">{data.identity_profiles.length}</span></h2><button ref={createTrigger} className="button" type="button" onClick={() => edit('new')}><Plus aria-hidden="true" />{t('addIdentityProfile')}</button></div><div className="table-scroll"><table><thead><tr><th data-fluid>{t('name')}</th><th>{t('platform')}</th><th>{t('mode')}</th><th>{t('status')}</th><th>{t('configuration')}</th></tr></thead><tbody>{data.identity_profiles.map((profile) => <tr key={profile.id}><td className="primary-cell">{profile.display_name}<small className="cell-detail">{profile.id}</small></td><td>{profile.platform === 'linux' ? 'Linux' : 'Windows'}</td><td>{identityModeLabel(profile.mode, t)}</td><td>{profile.enabled ? t('enabled') : t('disabled')}{profile.experimental ? ` · ${t('experimental')}` : ''}</td><td><button className="table-action" type="button" onClick={() => edit(profile)} aria-label={`${t('edit')} · ${profile.display_name}`} title={t('edit')}><Pencil aria-hidden="true" /></button></td></tr>)}</tbody></table></div></section>
}

function identityModeLabel(mode: IdentityProfileMode, t: Translator): string {
  const labels: Record<IdentityProfileMode, Parameters<Translator>[0]> = { managed_local: 'managedLocal', linux_sssd_ad: 'sssdAD', linux_sssd_freeipa: 'sssdFreeIPA', linux_sssd_ldap: 'sssdLDAP', linux_sssd_oidc: 'sssdOIDC', windows_ad: 'windowsAD', windows_entra: 'windowsEntra' }
  return t(labels[mode])
}

function CreateUserDialog({ t, onClose, onCreate }: { t: Translator; onClose: () => void; onCreate: AccessViewProps['onCreateUser'] }) {
  const [values, setValues] = useState({ username: '', display_name: '', password: '' })
  return <MutationDialog title={t('addLocalUser')} submitLabel={t('create')} t={t} onClose={onClose} onSubmit={() => onCreate(values)}><FormField label={t('username')} value={values.username} onChange={(username) => setValues({ ...values, username })} autoComplete="username" pattern="[A-Za-z0-9][A-Za-z0-9._-]{2,63}" maxLength={64} /><FormField label={t('displayName')} value={values.display_name} onChange={(display_name) => setValues({ ...values, display_name })} autoComplete="name" required={false} maxLength={128} /><FormField label={t('password')} type="password" value={values.password} onChange={(password) => setValues({ ...values, password })} autoComplete="new-password" minLength={12} maxLength={128} /></MutationDialog>
}

function ResetPasswordDialog({ user, t, onClose, onReset }: { user: UserAccount; t: Translator; onClose: () => void; onReset: AccessViewProps['onResetUserPassword'] }) {
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  return <MutationDialog title={`${t('resetPassword')} · ${user.display_name}`} submitLabel={t('resetPassword')} busyLabel={t('saving')} t={t} onClose={onClose} onSubmit={() => password === confirm ? onReset(user, password) : Promise.reject(new Error(t('passwordsDoNotMatch')))}><p className="field-hint">{t('resetPasswordHint')}</p><FormField label={t('newPassword')} type="password" value={password} onChange={setPassword} autoComplete="new-password" minLength={12} maxLength={128} hint={t('passwordRequirement')} /><FormField label={t('confirmPassword')} type="password" value={confirm} onChange={setConfirm} autoComplete="new-password" minLength={12} maxLength={128} /></MutationDialog>
}

function CreateAgentDialog({ t, onClose, onCreate, onCreated }: { t: Translator; onClose: () => void; onCreate: AccessViewProps['onCreateAgent']; onCreated: (credential: AgentCredential) => void }) {
  const [values, setValues] = useState({ id: '', display_name: '' })
  return <MutationDialog title={t('addAgent')} submitLabel={t('create')} t={t} onClose={onClose} onSubmit={() => onCreate(values)} onSubmitted={(created) => onCreated(created as AgentCredential)}><FormField label={t('agentID')} value={values.id} onChange={(id) => setValues({ ...values, id })} autoComplete="off" pattern="[A-Za-z0-9][A-Za-z0-9._:-]{2,127}" maxLength={128} /><FormField label={t('displayName')} value={values.display_name} onChange={(display_name) => setValues({ ...values, display_name })} autoComplete="off" maxLength={128} /></MutationDialog>
}

function CreateAPITokenDialog({ t, onClose, onCreate, onCreated }: { t: Translator; onClose: () => void; onCreate: AccessViewProps['onCreateAPIToken']; onCreated: (credential: APITokenCredential) => void }) {
  const [name, setName] = useState('Terraform')
  const [expires, setExpires] = useState('30')
  return <MutationDialog title={t('createAPIToken')} submitLabel={t('create')} t={t} onClose={onClose} onSubmit={() => onCreate({ name, expires_in_days: Number(expires) })} onSubmitted={(created) => onCreated(created as APITokenCredential)}><p className="field-hint">{t('apiTokenHint')}</p><FormField label={t('name')} value={name} onChange={setName} autoComplete="off" maxLength={80} /><FormField label={t('expiresInDays')} type="number" value={expires} onChange={setExpires} autoComplete="off" min={1} max={90} /></MutationDialog>
}

function CreateGroupDialog({ t, onClose, onCreate }: { t: Translator; onClose: () => void; onCreate: AccessViewProps['onCreateGroup'] }) {
  const [values, setValues] = useState({ id: '', display_name: '' })
  return <MutationDialog title={t('addGroup')} submitLabel={t('create')} t={t} onClose={onClose} onSubmit={() => onCreate({ ...values, source: 'local', enabled: true })}><FormField label={t('groupID')} value={values.id} onChange={(id) => setValues({ ...values, id })} autoComplete="off" pattern="[A-Za-z0-9][A-Za-z0-9._:-]{2,127}" maxLength={128} /><FormField label={t('displayName')} value={values.display_name} onChange={(display_name) => setValues({ ...values, display_name })} autoComplete="off" maxLength={128} /></MutationDialog>
}

type DesktopIdentityInput = { access_mode: ManagedDesktop['access_mode']; owner_user_id?: string; identity_profile_id?: string }

export function compatibleIdentityProfiles(desktop: ManagedDesktop, profiles: IdentityProfile[]): IdentityProfile[] {
  return profiles.filter((profile) => profile.enabled && profile.mode !== 'managed_local' && (desktop.os_family === 'unknown' || profile.platform === desktop.os_family))
}

function DesktopIdentityDialog({ desktop, data, t, onClose, onSave }: { desktop: ManagedDesktop; data: AccessControl; t: Translator; onClose: () => void; onSave: (input: DesktopIdentityInput) => Promise<void> }) {
  const [accessMode, setAccessMode] = useState(desktop.access_mode)
  const [ownerUserID, setOwnerUserID] = useState(desktop.owner_user_id ?? data.users.find((user) => !user.disabled)?.id ?? '')
  const currentProfile = data.identity_profiles.find((profile) => profile.id === desktop.identity_profile_id)
  const [profileID, setProfileID] = useState(currentProfile?.mode === 'managed_local' ? '' : desktop.identity_profile_id ?? '')
  const compatibleProfiles = compatibleIdentityProfiles(desktop, data.identity_profiles)
  return <MutationDialog title={`${t('desktopAccess')} · ${desktop.display_name}`} submitLabel={t('save')} busyLabel={t('saving')} t={t} onClose={onClose} onSubmit={() => onSave({ access_mode: accessMode, owner_user_id: accessMode === 'personal' ? ownerUserID : undefined, identity_profile_id: profileID || undefined })}><SelectField label={t('accessMode')} value={accessMode} onChange={(value) => setAccessMode(value as ManagedDesktop['access_mode'])} options={[["personal", t('personal')], ["shared", t('shared')]]} />{accessMode === 'personal' && <SelectField label={t('owner')} value={ownerUserID} onChange={setOwnerUserID} options={data.users.filter((user) => !user.disabled).map((user) => [user.id, user.display_name || user.username])} />}<SelectField label={t('guestIdentity')} value={profileID} onChange={setProfileID} options={[["", t('managedLocal')], ...compatibleProfiles.map((profile) => [profile.id, profile.display_name] as [string, string])]} /></MutationDialog>
}

function IdentityReconcileDialog({ desktop, data, t, onClose, onReconcile }: { desktop: ManagedDesktop; data: AccessControl; t: Translator; onClose: () => void; onReconcile: (input: { join_username?: string; join_password?: string; client_secret?: string }) => Promise<void> }) {
  const profile = data.identity_profiles.find((item) => item.id === desktop.identity_profile_id)
  const needsJoinCredential = profile?.mode === 'linux_sssd_ad' || profile?.mode === 'linux_sssd_freeipa' || profile?.mode === 'windows_ad'
  const needsClientSecret = profile?.mode === 'linux_sssd_oidc'
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  return <MutationDialog title={`${t('applyIdentity')} · ${desktop.display_name}`} submitLabel={t('apply')} busyLabel={t('applying')} t={t} onClose={onClose} onSubmit={() => onReconcile({ join_username: username || undefined, join_password: password || undefined, client_secret: clientSecret || undefined })}><p className="field-hint">{profile?.display_name ?? t('managedLocal')}</p>{needsJoinCredential && <><FormField label={t('joinUsername')} value={username} onChange={setUsername} autoComplete="username" /><FormField label={t('oneTimeJoinPassword')} type="password" value={password} onChange={setPassword} autoComplete="new-password" /></>}{needsClientSecret && <FormField label={t('oneTimeClientSecret')} type="password" value={clientSecret} onChange={setClientSecret} autoComplete="new-password" />}<p className="field-hint">{needsJoinCredential || needsClientSecret ? t('oneTimeSecretHint') : t('identityApplyHint')}</p></MutationDialog>
}

function IdentityProfileDialog({ profile, t, onClose, onSave }: { profile: IdentityProfile | null; t: Translator; onClose: () => void; onSave: AccessViewProps['onPutIdentityProfile'] }) {
  const [id, setID] = useState(profile?.id ?? '')
  const [displayName, setDisplayName] = useState(profile?.display_name ?? '')
  const [platform, setPlatform] = useState<IdentityProfile['platform']>(profile?.platform ?? 'linux')
  const [mode, setMode] = useState<IdentityProfileMode>(profile?.mode ?? 'managed_local')
  const [enabled, setEnabled] = useState(profile?.enabled ?? true)
  const [config, setConfig] = useState<Record<string, string>>(() => Object.fromEntries(Object.entries(profile?.config ?? {}).map(([key, value]) => [key, String(value)])))
  const modes: IdentityProfileMode[] = platform === 'linux' ? ['managed_local', 'linux_sssd_ad', 'linux_sssd_freeipa', 'linux_sssd_ldap', 'linux_sssd_oidc'] : ['managed_local', 'windows_ad', 'windows_entra']
  const fields: Partial<Record<IdentityProfileMode, Array<[string, Parameters<Translator>[0]]>>> = {
    linux_sssd_ad: [['domain', 'domain'], ['realm', 'realm'], ['allowed_groups', 'allowedGroups']],
    linux_sssd_freeipa: [['domain', 'domain'], ['server', 'directoryServer'], ['realm', 'realm']],
    linux_sssd_ldap: [['domain', 'domain'], ['uri', 'ldapURI'], ['search_base', 'searchBase'], ['access_filter', 'accessFilter']],
    linux_sssd_oidc: [['domain', 'domain'], ['idp_type', 'idpType'], ['client_id', 'clientID'], ['token_endpoint', 'tokenEndpoint'], ['userinfo_endpoint', 'userinfoEndpoint'], ['device_auth_endpoint', 'deviceAuthEndpoint'], ['id_scope', 'idScope']],
    windows_ad: [['domain', 'domain'], ['join_method', 'joinMethod'], ['allowed_group', 'allowedGroup']],
    windows_entra: [['tenant_id', 'tenantID'], ['target_hostname', 'targetHostname'], ['allowed_principal', 'allowedPrincipal']],
  }
  const selectPlatform = (value: string) => { const next = value as IdentityProfile['platform']; setPlatform(next); setMode('managed_local'); setConfig({}) }
  const experimental = mode === 'linux_sssd_oidc' || mode === 'windows_entra'
  return <MutationDialog title={profile ? t('editIdentityProfile') : t('addIdentityProfile')} submitLabel={t('save')} busyLabel={t('saving')} t={t} onClose={onClose} onSubmit={() => onSave(id, { display_name: displayName, platform, mode, enabled, experimental, config })}><FormField label={t('profileID')} value={id} onChange={setID} autoComplete="off" pattern="[A-Za-z0-9][A-Za-z0-9._:-]{2,127}" maxLength={128} disabled={Boolean(profile)} /><FormField label={t('displayName')} value={displayName} onChange={setDisplayName} autoComplete="off" maxLength={128} /><div className="form-grid"><SelectField label={t('platform')} value={platform} onChange={selectPlatform} disabled={Boolean(profile)} options={[["linux", 'Linux'], ["windows", 'Windows']]} /><SelectField label={t('mode')} value={mode} onChange={(value) => { setMode(value as IdentityProfileMode); setConfig({}) }} disabled={Boolean(profile)} options={modes.map((item) => [item, identityModeLabel(item, t)])} /></div>{fields[mode]?.map(([key, label]) => <FormField key={key} label={t(label)} value={config[key] ?? ''} onChange={(value) => setConfig({ ...config, [key]: value })} autoComplete="off" />)}{experimental && <p className="field-hint">{t('experimentalIdentityHint')}</p>}<label className="check-field"><input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} />{t('enabled')}</label></MutationDialog>
}

function SelectField({ label, value, onChange, options, disabled = false }: { label: string; value: string; onChange: (value: string) => void; options: Array<[string, string]>; disabled?: boolean }) {
  const id = useId()
  return <div className="form-field"><label htmlFor={id}>{label}</label><select id={id} value={value} disabled={disabled} onChange={(event) => onChange(event.target.value)}>{options.map(([optionValue, optionLabel]) => <option key={optionValue} value={optionValue}>{optionLabel}</option>)}</select></div>
}

function MutationDialog({ title, submitLabel, busyLabel, t, onClose, onSubmit, onSubmitted, children }: { title: string; submitLabel: string; busyLabel?: string; t: Translator; onClose: () => void; onSubmit: () => Promise<unknown>; onSubmitted?: (result: unknown) => void; children: React.ReactNode }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const busyRef = useRef(false)
  const titleID = useId()
  const dialogRef = (element: HTMLDialogElement | null) => { if (element && !element.open) element.showModal() }
  const submit = async (event: React.FormEvent) => { event.preventDefault(); if (busyRef.current) return; busyRef.current = true; setBusy(true); setError(''); try { const result = await onSubmit(); if (onSubmitted) onSubmitted(result); else onClose() } catch (submitError) { setError(submitError instanceof Error ? submitError.message : t('unavailable')); busyRef.current = false; setBusy(false) } }
  return <dialog className="dialog" ref={dialogRef} onKeyDown={(event) => { if (event.key === 'Escape' && !busy) { event.preventDefault(); onClose() } }} onCancel={(event) => { event.preventDefault(); if (!busy) onClose() }} aria-labelledby={titleID}><form onSubmit={submit} aria-busy={busy}><h2 id={titleID}>{title}</h2>{children}{error && <p className="form-error" role="alert">{error}</p>}<div className="dialog-actions"><button className="button" type="button" disabled={busy} onClick={onClose}>{t('cancel')}</button><button className="button button-primary" type="submit" disabled={busy}>{busy ? busyLabel ?? t('creating') : submitLabel}</button></div></form></dialog>
}

function CredentialDialog({ credential, t, onClose }: { credential: AgentCredential; t: Translator; onClose: () => void }) {
  const [copied, setCopied] = useState(false)
  const titleID = useId()
  const dialogRef = (element: HTMLDialogElement | null) => { if (element && !element.open) element.showModal() }
  return <dialog className="dialog" ref={dialogRef} onKeyDown={(event) => { if (event.key === 'Escape') { event.preventDefault(); onClose() } }} onCancel={(event) => { event.preventDefault(); onClose() }} aria-labelledby={titleID}><div className="credential-dialog"><h2 id={titleID}>{t('agentToken')} · {credential.agent.display_name}</h2><p className="field-hint">{t('agentTokenHint')}</p><textarea className="credential-value" readOnly rows={4} value={credential.access_token} aria-label={t('agentToken')} onFocus={(event) => event.currentTarget.select()} /><div className="dialog-actions"><button className="button" type="button" onClick={async () => { try { await navigator.clipboard.writeText(credential.access_token); setCopied(true) } catch { setCopied(false) } }}>{copied ? t('copied') : t('copyToken')}</button><button className="button button-primary" type="button" onClick={onClose}>{t('done')}</button></div></div></dialog>
}

function APITokenCredentialDialog({ credential, t, onClose }: { credential: APITokenCredential; t: Translator; onClose: () => void }) {
  const [copied, setCopied] = useState(false)
  const titleID = useId()
  const dialogRef = (element: HTMLDialogElement | null) => { if (element && !element.open) element.showModal() }
  return <dialog className="dialog" ref={dialogRef} onKeyDown={(event) => { if (event.key === 'Escape') { event.preventDefault(); onClose() } }} onCancel={(event) => { event.preventDefault(); onClose() }} aria-labelledby={titleID}><div className="credential-dialog"><h2 id={titleID}>{t('apiToken')} · {credential.api_token.name}</h2><p className="field-hint">{t('apiTokenOnceHint')}</p><textarea className="credential-value" readOnly rows={4} value={credential.access_token} aria-label={t('apiToken')} onFocus={(event) => event.currentTarget.select()} /><div className="dialog-actions"><button className="button" type="button" onClick={async () => { try { await navigator.clipboard.writeText(credential.access_token); setCopied(true) } catch { setCopied(false) } }}>{copied ? t('copied') : t('copyToken')}</button><button className="button button-primary" type="button" onClick={onClose}>{t('done')}</button></div></div></dialog>
}

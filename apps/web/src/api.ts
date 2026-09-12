import { clearSessionExpiry, expireWebSession } from './session-recovery'

export type Infrastructure = {
  version: { version: string; release: string }
  cluster: { name: string; quorate: boolean; node_count: number }
  nodes: Array<{
    name: string
    status: string
    cpu_usage: number
    cpu_count: number
    memory_used: number
    memory_total: number
  }>
  virtual_machines: Array<{
    vmid: number
    name: string
    kind: string
    node: string
    status: string
    template: boolean
    managed: boolean
    cpu_count: number
    memory_total: number
  }>
  storage: Array<{
    name: string
    kind: string
    content: string
    shared: boolean
  }>
  mutations_enabled: boolean
}

export type SystemState = {
  name: string
  pve_configured: boolean
  database_configured: boolean
  initialized: boolean
  oidc_configured: boolean
  oidc_name: string
  uptime_seconds: number
  identity_capabilities: {
    managed_local: boolean
    linux_sssd: boolean
    windows_ad: boolean
    linux_sssd_oidc: boolean
    windows_entra_rdp: boolean
  }
}

export type DesktopAccessPolicy = {
  vmid: number
  privilege_mode: 'standard' | 'local_admin'
  clipboard_redirection: boolean
  drive_redirection: boolean
  managed_background: boolean
  desired_revision: number
  applied_revision: number
  state: 'pending' | 'applied' | 'failed'
  os_family: 'linux' | 'windows' | 'unknown'
  last_error?: string
  updated_at: string
}

export type AuditEvent = {
  id: string
  actor_id?: string
  actor_username?: string
  actor_display_name?: string
  event_type: string
  outcome: 'success' | 'failure'
  target_type?: string
  target_id?: string
  detail: Record<string, unknown>
  occurred_at: string
}

export type AuditEventPage = {
  events: AuditEvent[]
  next_cursor?: string
}

export type Session = {
  user: {
    id: string
    username: string
    display_name: string
    role: string
    identity_kind: 'local' | 'oidc'
  }
  csrf_token: string
}

export type UserAccount = {
  id: string
  username: string
  display_name: string
  role: 'platform_admin' | 'user'
  disabled: boolean
  identity_kind: 'local' | 'oidc'
  created_at: string
}

export type APIToken = {
  id: string
  name: string
  expires_at: string
  created_at: string
  last_used_at?: string
}

export type APITokenCredential = {
  api_token: APIToken
  access_token: string
}

export type ManagedDesktop = {
  vmid: number
  display_name: string
  node: string
  os_family: 'linux' | 'windows' | 'unknown'
  present: boolean
  enabled: boolean
  access_mode: 'personal' | 'shared'
  owner_user_id?: string
  identity_profile_id?: string
  identity_state: 'pending' | 'applied' | 'restart_required' | 'failed'
  applied_identity_profile_id?: string
  identity_last_error?: string
  identity_updated_at: string
  first_seen_at: string
  last_seen_at: string
  updated_at: string
}

export type AgentPrincipal = {
  id: string
  display_name: string
  enabled: boolean
  created_by?: string
  created_at: string
  updated_at: string
  last_used_at?: string
}

export type DesktopAssignment = {
  subject_type: 'user' | 'agent' | 'group'
  subject_id: string
  desktop_vmid: number
  created_by?: string
  created_at: string
}

export type IdentityGroup = {
  id: string
  provider_id?: string
  external_id?: string
  display_name: string
  source: 'local' | 'oidc' | 'scim'
  enabled: boolean
  member_count: number
  created_by?: string
  created_at: string
  updated_at: string
}

export type IdentityGroupMembership = {
  group_id: string
  user_id: string
  source: 'local' | 'oidc' | 'scim'
  created_at: string
  updated_at: string
}

export type IdentityProfileMode = 'managed_local' | 'linux_sssd_ad' | 'linux_sssd_freeipa' | 'linux_sssd_ldap' | 'linux_sssd_oidc' | 'windows_ad' | 'windows_entra'

export type IdentityProfile = {
  id: string
  display_name: string
  platform: 'linux' | 'windows'
  mode: IdentityProfileMode
  enabled: boolean
  experimental: boolean
  config: Record<string, unknown>
  created_by?: string
  created_at: string
  updated_at: string
}

export type GuestIdentityBinding = {
  desktop_vmid: number
  user_id: string
  profile_id?: string
  guest_username: string
  state: 'provisioning' | 'ready' | 'failed' | 'disabled'
  last_error?: string
  created_at: string
  updated_at: string
}

export type DesktopLease = {
  id: string
  agent_id: string
  desktop_id: string
  state: 'active' | 'released' | 'expired' | 'revoked'
  control_epoch: number
  expires_at: string
  created_at: string
  updated_at: string
}

export type AccessControl = {
  users: UserAccount[]
  agents: AgentPrincipal[]
  desktops: ManagedDesktop[]
  assignments: DesktopAssignment[]
  desktop_leases: DesktopLease[]
  groups: IdentityGroup[]
  group_memberships: IdentityGroupMembership[]
  identity_profiles: IdentityProfile[]
  guest_identity_bindings: GuestIdentityBinding[]
  api_tokens: APIToken[]
}

export type AgentCredential = {
  agent: AgentPrincipal
  access_token: string
}

export type Job = {
  id: string
  operation: string
  state: 'accepted' | 'running' | 'succeeded' | 'failed'
  source_vmid?: number
  target_vmid?: number
  target_node?: string
  upid?: string
  error?: string
  progress: number
  detail?: string
  request?: { image_profile_id?: string }
  created_at: string
  updated_at: string
}

export type ImageProfile = {
  id: string
  display_name: string
  os_family: 'linux' | 'windows'
  os_version: string
  architecture: string
  lifecycle: 'supported' | 'legacy'
  enabled: boolean
  source_node: string
  source_iso: string
  source_iso_checksum: string
  driver_iso: string
  driver_iso_checksum: string
  template_vmid?: number
  mirror_url: string
  security_mirror_url: string
  storage_pool: string
  bridge: string
  windows_image_name: string
  default_cores: number
  default_memory_mb: number
  default_disk_gb: number
  firmware: 'seabios' | 'uefi'
  tpm_version: 'none' | '2.0'
  agent_kind: 'linux' | 'windows'
  desktop_protocol: 'rdp'
  default_gpu_profile_id: string
  build_status: 'draft' | 'blocked' | 'building' | 'testing' | 'ready' | 'failed'
  status_detail: string
  updated_at: string
}

export type ImageProfileUpdate = Partial<Pick<ImageProfile, 'display_name' | 'enabled' | 'source_node' | 'source_iso' | 'source_iso_checksum' | 'driver_iso' | 'driver_iso_checksum' | 'template_vmid' | 'mirror_url' | 'security_mirror_url' | 'storage_pool' | 'bridge' | 'windows_image_name' | 'default_cores' | 'default_memory_mb' | 'default_disk_gb' | 'firmware' | 'tpm_version' | 'default_gpu_profile_id' | 'build_status' | 'status_detail'>>

export type GPUProfile = {
  id: string
  display_name: string
  mode: 'none' | 'pci_passthrough' | 'sriov' | 'mdev'
  vendor_id: string
  device_class: string
  resource_mapping: string
  mdev_type: string
  enabled: boolean
  exclusive: boolean
  allow_live_migration: boolean
  updated_at: string
}

export type MDevType = {
  type: string
  name: string
  available: number
  description: string
}

export type PCIResourceMapping = {
  id: string
  description: string
  mdev: boolean
  entries: Array<{
    node: string
    device_id: string
    device_paths?: string[]
    hardware_id?: string
    subsystem_id?: string
    iommu_group?: string
  }>
}

export type PCIResourceMappingInput = {
  id: string
  description: string
  mdev: boolean
  entries: Array<{ node: string; device_id: string }>
}

export type GPUDevice = {
  node: string
  id: string
  vendor_id: string
  device_id: string
  subsystem_vendor_id?: string
  subsystem_device_id?: string
  vendor_name: string
  device_name: string
  class: string
  iommu_group?: number
  mdev_capable: boolean
  mdev_types: MDevType[]
  assignable: boolean
}

export type PlatformConfig = {
  image_profiles: ImageProfile[]
  gpu_profiles: GPUProfile[]
  gpu_devices: GPUDevice[]
  pci_resource_mappings: PCIResourceMapping[]
  gpu_inventory_available: boolean
  gpu_mapping_inventory_available: boolean
  image_builder_available: boolean
  image_builds: Job[]
}

type APIError = { error?: { code?: string; message?: string } }

export class APIRequestError extends Error {
  constructor(public readonly status: number, public readonly code: string, message: string) {
    super(message)
    this.name = 'APIRequestError'
  }
}

export async function getInfrastructure(signal?: AbortSignal): Promise<Infrastructure> {
  return requestJSON<Infrastructure>('/api/v1/infrastructure', { signal })
}

export async function getSystem(signal?: AbortSignal): Promise<SystemState> {
  return requestJSON<SystemState>('/api/v1/system', { signal })
}

export async function getMe(signal?: AbortSignal): Promise<Session | null> {
  const response = await fetch('/api/v1/me', { signal })
  if (response.status === 401) return null
  return parseResponse<Session>(response)
}

export async function getAccessControl(signal?: AbortSignal): Promise<AccessControl> {
  return requestJSON<AccessControl>('/api/v1/access-control', { signal })
}

export async function changePassword(input: { current_password: string; new_password: string }, csrfToken: string): Promise<void> {
  const response = await fetch('/api/v1/me/password', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  })
  if (!response.ok) await parseResponse<never>(response)
}

export async function createAPIToken(input: { name: string; expires_in_days: number }, csrfToken: string): Promise<APITokenCredential> {
  return requestJSON<APITokenCredential>('/api/v1/api-tokens', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  })
}

export async function revokeAPIToken(id: string, csrfToken: string): Promise<void> {
  const response = await fetch(`/api/v1/api-tokens/${encodeURIComponent(id)}`, {
    method: 'DELETE',
    headers: { 'X-CSRF-Token': csrfToken },
  })
  if (!response.ok) await parseResponse<never>(response)
}

export async function reconcileAccessControl(csrfToken: string): Promise<AccessControl> {
  return requestJSON<AccessControl>('/api/v1/access-control/reconcile', {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
  })
}

export async function createLocalUser(input: { username: string; display_name: string; password: string }, csrfToken: string): Promise<UserAccount> {
  return requestJSON<UserAccount>('/api/v1/users', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  })
}

export async function updateUser(id: string, disabled: boolean, csrfToken: string): Promise<UserAccount> {
  return requestJSON<UserAccount>(`/api/v1/users/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify({ disabled }),
  })
}

export async function resetUserPassword(id: string, newPassword: string, csrfToken: string): Promise<void> {
  const response = await fetch(`/api/v1/users/${encodeURIComponent(id)}/password`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify({ new_password: newPassword }),
  })
  if (!response.ok) await parseResponse<never>(response)
}

export async function createAgentPrincipal(input: { id: string; display_name: string }, csrfToken: string): Promise<AgentCredential> {
  return requestJSON<AgentCredential>('/api/v1/agents', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  })
}

export async function updateAgentPrincipal(id: string, enabled: boolean, csrfToken: string): Promise<AgentPrincipal> {
  return requestJSON<AgentPrincipal>(`/api/v1/agents/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify({ enabled }),
  })
}

export async function rotateAgentToken(id: string, csrfToken: string): Promise<AgentCredential> {
  return requestJSON<AgentCredential>(`/api/v1/agents/${encodeURIComponent(id)}/token`, {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
  })
}

export async function setDesktopAssignment(subjectType: DesktopAssignment['subject_type'], subjectID: string, vmid: number, assigned: boolean, csrfToken: string): Promise<void> {
  const response = await fetch(`/api/v1/desktop-assignments/${subjectType}/${encodeURIComponent(subjectID)}/${vmid}`, {
    method: assigned ? 'PUT' : 'DELETE',
    headers: { 'X-CSRF-Token': csrfToken },
  })
  if (!response.ok) await parseResponse<never>(response)
}

export async function updateManagedDesktopIdentity(vmid: number, input: Pick<ManagedDesktop, 'access_mode'> & { owner_user_id?: string; identity_profile_id?: string }, csrfToken: string): Promise<ManagedDesktop> {
  return requestJSON<ManagedDesktop>(`/api/v1/managed-desktops/${vmid}/identity`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  })
}

export async function reconcileManagedDesktopIdentity(vmid: number, input: { join_username?: string; join_password?: string; client_secret?: string }, csrfToken: string): Promise<ManagedDesktop> {
  return requestJSON<ManagedDesktop>(`/api/v1/managed-desktops/${vmid}/identity/reconcile`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  })
}

export async function putIdentityProfile(id: string, input: Pick<IdentityProfile, 'display_name' | 'platform' | 'mode' | 'enabled' | 'experimental' | 'config'>, csrfToken: string): Promise<IdentityProfile> {
  return requestJSON<IdentityProfile>(`/api/v1/identity-profiles/${encodeURIComponent(id)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  })
}

export async function createIdentityGroup(input: Pick<IdentityGroup, 'id' | 'display_name' | 'source' | 'enabled'>, csrfToken: string): Promise<IdentityGroup> {
  return requestJSON<IdentityGroup>('/api/v1/identity-groups', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  })
}

export async function updateIdentityGroup(id: string, enabled: boolean, csrfToken: string): Promise<IdentityGroup> {
  return requestJSON<IdentityGroup>(`/api/v1/identity-groups/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify({ enabled }),
  })
}

export async function setIdentityGroupMembership(groupID: string, userID: string, member: boolean, csrfToken: string): Promise<void> {
  const response = await fetch(`/api/v1/identity-groups/${encodeURIComponent(groupID)}/members/${encodeURIComponent(userID)}`, {
    method: member ? 'PUT' : 'DELETE',
    headers: { 'X-CSRF-Token': csrfToken },
  })
  if (!response.ok) await parseResponse<never>(response)
}

export async function createInitialAdmin(input: { setup_token: string; username: string; display_name: string; password: string }): Promise<Session> {
  return requestJSON<Session>('/api/v1/setup/admin', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input) })
}

export async function login(input: { username: string; password: string }): Promise<Session> {
  const session = await requestJSON<Session>('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input) })
  clearSessionExpiry()
  return session
}

export async function logout(csrfToken: string): Promise<void> {
  const response = await fetch('/api/v1/auth/logout', { method: 'POST', headers: { 'X-CSRF-Token': csrfToken } })
  if (!response.ok) await parseResponse<never>(response)
}

export async function createDesktop(input: { name: string; source_vmid: number; target_node: string; storage: string; full: boolean; gpu_profile_id: string }, csrfToken: string): Promise<Job> {
  return requestJSON<Job>('/api/v1/desktop-instances', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'Idempotency-Key': crypto.randomUUID() },
    body: JSON.stringify(input),
  })
}

export async function changePower(vmid: number, action: 'start' | 'stop', csrfToken: string): Promise<Job> {
  return requestJSON<Job>(`/api/v1/virtual-machines/${vmid}/actions/${action}`, {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken, 'Idempotency-Key': crypto.randomUUID() },
  })
}

export async function getDesktopAccessPolicy(vmid: number, signal?: AbortSignal): Promise<DesktopAccessPolicy> {
  return requestJSON<DesktopAccessPolicy>(`/api/v1/virtual-machines/${vmid}/access-policy`, { signal })
}

export async function updateDesktopAccessPolicy(vmid: number, policy: Pick<DesktopAccessPolicy, 'privilege_mode' | 'clipboard_redirection' | 'drive_redirection' | 'managed_background'>, csrfToken: string): Promise<DesktopAccessPolicy> {
  return requestJSON<DesktopAccessPolicy>(`/api/v1/virtual-machines/${vmid}/access-policy`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(policy),
  })
}

export async function getJob(id: string, signal?: AbortSignal): Promise<Job> {
  return requestJSON<Job>(`/api/v1/jobs/${encodeURIComponent(id)}`, { signal })
}

export type CloneRecoveryEvidence = {
  job_id: string
  target_status: 'target_absent' | 'target_mismatch' | 'marker_matches' | 'target_locked'
  candidates: Array<{
    upid: string
    start_time: number
    status: 'running' | 'stopped'
    exit_status?: string
    log_target_status: 'matches' | 'mismatch' | 'unrecognized'
  }>
}

export async function getCloneRecoveryEvidence(id: string, signal?: AbortSignal): Promise<CloneRecoveryEvidence> {
  return requestJSON<CloneRecoveryEvidence>(`/api/v1/jobs/${encodeURIComponent(id)}/clone-candidates`, { signal, cache: 'no-store' })
}

// Never retry a mutation here. If its response is lost, read the original Job.
export async function recoverClone(id: string, input: { upid: string; reason: string }, csrfToken: string, signal?: AbortSignal): Promise<Job> {
  return requestJSON<Job>(`/api/v1/jobs/${encodeURIComponent(id)}/clone-recovery`, {
    method: 'POST', signal, cache: 'no-store',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  })
}

export async function getJobs(limit = 50, signal?: AbortSignal): Promise<Job[]> {
  const result = await requestJSON<{ jobs: Job[] }>(`/api/v1/jobs?limit=${limit}`, { signal })
  return result.jobs
}

export async function getAuditEvents(filters: { query?: string; outcome?: '' | AuditEvent['outcome']; cursor?: string; limit?: number } = {}, signal?: AbortSignal): Promise<AuditEventPage> {
  const parameters = new URLSearchParams()
  parameters.set('limit', String(filters.limit ?? 30))
  if (filters.query) parameters.set('q', filters.query)
  if (filters.outcome) parameters.set('outcome', filters.outcome)
  if (filters.cursor) parameters.set('cursor', filters.cursor)
  return requestJSON<AuditEventPage>(`/api/v1/audit-events?${parameters.toString()}`, { signal })
}

export async function getPlatformConfig(signal?: AbortSignal): Promise<PlatformConfig> {
  return requestJSON<PlatformConfig>('/api/v1/platform-config', { signal })
}

export async function updateImageProfile(id: string, input: ImageProfileUpdate, csrfToken: string): Promise<ImageProfile> {
  return requestJSON<ImageProfile>(`/api/v1/image-profiles/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  })
}

export async function startImageBuild(id: string, profile: ImageProfileUpdate, secrets: { builder_password: string; windows_product_key: string }, csrfToken: string): Promise<Job> {
  return requestJSON<Job>(`/api/v1/image-profiles/${encodeURIComponent(id)}/builds`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'Idempotency-Key': crypto.randomUUID() },
    body: JSON.stringify({ profile, ...secrets }),
  })
}

export async function updateGPUProfile(id: string, input: Partial<Pick<GPUProfile, 'display_name' | 'resource_mapping' | 'enabled'>>, csrfToken: string): Promise<GPUProfile> {
  return requestJSON<GPUProfile>(`/api/v1/gpu-profiles/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  })
}

export async function createPCIResourceMapping(input: PCIResourceMappingInput, csrfToken: string): Promise<PCIResourceMapping> {
  return requestJSON<PCIResourceMapping>('/api/v1/pci-resource-mappings', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  })
}

export async function updatePCIResourceMapping(id: string, input: Omit<PCIResourceMappingInput, 'id'>, csrfToken: string): Promise<PCIResourceMapping> {
  return requestJSON<PCIResourceMapping>(`/api/v1/pci-resource-mappings/${encodeURIComponent(id)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  })
}

async function requestJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const publicEndpoint = ['/api/v1/system', '/api/v1/auth/login', '/api/v1/setup/admin'].includes(path)
  return parseResponse<T>(await fetch(path, init), !publicEndpoint)
}

async function parseResponse<T>(response: Response, requiresSession = true): Promise<T> {
  if (!response.ok) {
    if (response.status === 401 && requiresSession) expireWebSession()
    const body = (await response.json().catch(() => ({}))) as APIError
    throw new APIRequestError(response.status, body.error?.code ?? 'request_failed', body.error?.message ?? `Request failed with ${response.status}`)
  }
  return response.json() as Promise<T>
}

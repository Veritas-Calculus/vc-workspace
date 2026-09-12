import { afterEach, describe, expect, it, vi } from 'vitest'
import { changePassword, createAPIToken, getAuditEvents, getCloneRecoveryEvidence, recoverClone, resetUserPassword, revokeAPIToken, setDesktopAssignment, startImageBuild, updateDesktopAccessPolicy } from './api'

afterEach(() => vi.unstubAllGlobals())

describe('clone recovery requests', () => {
  it('loads private evidence with an encoded job ID and cancellation', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({ job_id: 'job/a', candidates: [], target_status: 'target_absent' })))
    vi.stubGlobal('fetch', fetchMock)
    const signal = new AbortController().signal
    await getCloneRecoveryEvidence('job/a', signal)
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/jobs/job%2Fa/clone-candidates', { signal, cache: 'no-store' })
  })
  it('submits only the selected handle and reason with CSRF, without retries', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({ error: { code: 'recovery_commit_uncertain', message: 'Read original job' } }), { status: 503 }))
    vi.stubGlobal('fetch', fetchMock)
    const input = { upid: 'UPID:test:1:', reason: 'Checked target' }
    await expect(recoverClone('job/a', input, 'csrf')).rejects.toMatchObject({ status: 503, code: 'recovery_commit_uncertain' })
    expect(fetchMock).toHaveBeenCalledOnce()
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/jobs/job%2Fa/clone-recovery', expect.objectContaining({ method: 'POST', cache: 'no-store', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': 'csrf' }, body: JSON.stringify(input) }))
  })
})

describe('startImageBuild', () => {
  it('submits configuration and one-time secrets in a single idempotent request', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({
      id: 'job-test', operation: 'image.build', state: 'running', progress: 1,
      created_at: '2026-09-02T00:00:00Z', updated_at: '2026-09-02T00:00:00Z',
    }), { status: 202, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    await startImageBuild('debian-13-xfce', {
      template_vmid: 9200,
      mirror_url: 'http://mirror.example.com/debian',
      build_status: 'draft',
    }, {
      builder_password: 'BuildPass-123!',
      windows_product_key: '',
    }, 'csrf-token')

    expect(fetchMock).toHaveBeenCalledOnce()
    const [path, init] = fetchMock.mock.calls[0]
    expect(path).toBe('/api/v1/image-profiles/debian-13-xfce/builds')
    expect(init?.method).toBe('POST')
    expect(init?.headers).toMatchObject({
      'Content-Type': 'application/json',
      'X-CSRF-Token': 'csrf-token',
    })
    expect((init?.headers as Record<string, string>)['Idempotency-Key']).toBeTruthy()
    expect(JSON.parse(String(init?.body))).toEqual({
      profile: {
        template_vmid: 9200,
        mirror_url: 'http://mirror.example.com/debian',
        build_status: 'draft',
      },
      builder_password: 'BuildPass-123!',
      windows_product_key: '',
    })
  })
})

describe('updateDesktopAccessPolicy', () => {
  it('submits the complete session policy with CSRF protection', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({
      vmid: 158, privilege_mode: 'local_admin', clipboard_redirection: false,
      drive_redirection: false, managed_background: true, desired_revision: 2, applied_revision: 2,
      state: 'applied', os_family: 'linux', updated_at: '2026-09-02T00:00:00Z',
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    await updateDesktopAccessPolicy(158, {
      privilege_mode: 'local_admin',
      clipboard_redirection: false,
      drive_redirection: false,
      managed_background: true,
    }, 'csrf-token')

    expect(fetchMock).toHaveBeenCalledWith('/api/v1/virtual-machines/158/access-policy', expect.objectContaining({
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': 'csrf-token' },
      body: JSON.stringify({ privilege_mode: 'local_admin', clipboard_redirection: false, drive_redirection: false, managed_background: true }),
    }))
  })
})

describe('getAuditEvents', () => {
  it('encodes server-side search, outcome, cursor, and page size', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({ events: [], next_cursor: '41' }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))
    vi.stubGlobal('fetch', fetchMock)

    await getAuditEvents({ query: 'alice VM 158', outcome: 'failure', cursor: '42', limit: 20 })

    expect(fetchMock).toHaveBeenCalledWith('/api/v1/audit-events?limit=20&q=alice+VM+158&outcome=failure&cursor=42', { signal: undefined })
  })
})

describe('setDesktopAssignment', () => {
  it('encodes the authenticated subject and protects mutations with CSRF', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await setDesktopAssignment('agent', 'research:agent.one', 201, false, 'csrf-token')

    expect(fetchMock).toHaveBeenCalledWith('/api/v1/desktop-assignments/agent/research%3Aagent.one/201', {
      method: 'DELETE',
      headers: { 'X-CSRF-Token': 'csrf-token' },
    })
  })
})

describe('password management', () => {
  it('changes the current local password with CSRF protection', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await changePassword({ current_password: 'OldPassword-123', new_password: 'NewPassword-456' }, 'csrf-token')

    expect(fetchMock).toHaveBeenCalledWith('/api/v1/me/password', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': 'csrf-token' },
      body: JSON.stringify({ current_password: 'OldPassword-123', new_password: 'NewPassword-456' }),
    })
  })

  it('lets an administrator reset another local password', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await resetUserPassword('user/a', 'NewPassword-456', 'csrf-token')

    expect(fetchMock).toHaveBeenCalledWith('/api/v1/users/user%2Fa/password', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': 'csrf-token' },
      body: JSON.stringify({ new_password: 'NewPassword-456' }),
    })
  })
})

describe('IaC API credentials', () => {
  it('creates a time-bounded credential with CSRF protection', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({
      api_token: {
        id: 'token_123', name: 'Terraform production', expires_at: '2026-10-01T00:00:00Z',
        created_at: '2026-09-05T00:00:00Z',
      },
      access_token: 'vcwi_secret',
    }), { status: 201, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    const credential = await createAPIToken({ name: 'Terraform production', expires_in_days: 30 }, 'csrf-token')

    expect(credential.access_token).toBe('vcwi_secret')
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/api-tokens', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': 'csrf-token' },
      body: JSON.stringify({ name: 'Terraform production', expires_in_days: 30 }),
    })
  })

  it('revokes a credential by its encoded identifier', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await revokeAPIToken('token/a', 'csrf-token')

    expect(fetchMock).toHaveBeenCalledWith('/api/v1/api-tokens/token%2Fa', {
      method: 'DELETE',
      headers: { 'X-CSRF-Token': 'csrf-token' },
    })
  })
})

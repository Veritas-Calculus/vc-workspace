import { afterEach, describe, expect, it, vi } from 'vitest'
import { getAuditEvents, setDesktopAssignment, startImageBuild, updateDesktopAccessPolicy } from './api'

afterEach(() => vi.unstubAllGlobals())

describe('startImageBuild', () => {
  it('submits configuration and one-time secrets in a single idempotent request', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({
      id: 'job-test', operation: 'image.build', state: 'running', progress: 1,
      created_at: '2026-09-02T00:00:00Z', updated_at: '2026-09-02T00:00:00Z',
    }), { status: 202, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    await startImageBuild('debian-13-xfce', {
      template_vmid: 9200,
      mirror_url: 'http://10.31.0.2/debian',
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
        mirror_url: 'http://10.31.0.2/debian',
        build_status: 'draft',
      },
      builder_password: 'BuildPass-123!',
      windows_product_key: '',
    })
  })
})

describe('updateDesktopAccessPolicy', () => {
  it('submits one allowlisted privilege mode with CSRF protection', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify({
      vmid: 158, privilege_mode: 'local_admin', desired_revision: 2, applied_revision: 2,
      state: 'applied', os_family: 'linux', updated_at: '2026-09-02T00:00:00Z',
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    await updateDesktopAccessPolicy(158, 'local_admin', 'csrf-token')

    expect(fetchMock).toHaveBeenCalledWith('/api/v1/virtual-machines/158/access-policy', expect.objectContaining({
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': 'csrf-token' },
      body: JSON.stringify({ privilege_mode: 'local_admin' }),
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

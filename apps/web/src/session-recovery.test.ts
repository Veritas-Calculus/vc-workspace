import { afterEach, describe, expect, it, vi } from 'vitest'
import { getInfrastructure, getMe, login, setDesktopAssignment } from './api'
import { clearSessionExpiry } from './session-recovery'

afterEach(() => { clearSessionExpiry(); vi.unstubAllGlobals() })

describe('session expiry boundary', () => {
  function arrange(status = 401) {
    const replace = vi.fn()
    vi.stubGlobal('window', { location: { replace, search: '' }, history: { replaceState: vi.fn() } })
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: { code: 'session_expired', message: 'expired' } }), { status })))
    return replace
  }

  it('redirects expired protected reads and writes exactly once', async () => {
    const replace = arrange()
    await Promise.allSettled([getInfrastructure(), setDesktopAssignment('user', 'a', 1, false, 'csrf')])
    expect(replace).toHaveBeenCalledExactlyOnceWith('/console?session=expired')
  })

  it('does not redirect an anonymous bootstrap or invalid login', async () => {
    const replace = arrange()
    expect(await getMe()).toBeNull()
    await expect(login({ username: 'a', password: 'wrong' })).rejects.toMatchObject({ status: 401 })
    expect(replace).not.toHaveBeenCalled()
  })

  it('does not confuse forbidden with logged out', async () => {
    const replace = arrange(403)
    await expect(getInfrastructure()).rejects.toMatchObject({ status: 403 })
    expect(replace).not.toHaveBeenCalled()
  })
})

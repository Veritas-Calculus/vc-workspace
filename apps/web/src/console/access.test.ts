import { describe, expect, it } from 'vitest'

import type { IdentityProfile, ManagedDesktop } from '../api'
import { compatibleIdentityProfiles } from './access'

const desktop = { os_family: 'linux' } as ManagedDesktop

const profiles = [
  { id: 'managed-local-linux', platform: 'linux', mode: 'managed_local', enabled: true },
  { id: 'linux-ad', platform: 'linux', mode: 'linux_sssd_ad', enabled: true },
  { id: 'linux-disabled', platform: 'linux', mode: 'linux_sssd_ldap', enabled: false },
  { id: 'windows-ad', platform: 'windows', mode: 'windows_ad', enabled: true },
] as IdentityProfile[]

describe('compatibleIdentityProfiles', () => {
  it('only offers enabled, non-default profiles for the desktop OS', () => {
    expect(compatibleIdentityProfiles(desktop, profiles).map((profile) => profile.id)).toEqual(['linux-ad'])
  })

  it('keeps cross-platform profiles available only while the legacy OS is unknown', () => {
    expect(compatibleIdentityProfiles({ ...desktop, os_family: 'unknown' }, profiles).map((profile) => profile.id)).toEqual(['linux-ad', 'windows-ad'])
  })
})

import { describe, expect, it } from 'vitest'
import type { ImageProfile, Job, PlatformConfig } from './api'
import { applyImageBuildJob } from './image-build-state'

const profile = { id: 'debian-13-xfce', template_vmid: 9100, source_node: 'old-node', build_status: 'ready', source_iso: 'old.iso' } as ImageProfile
const other = { id: 'windows-11', build_status: 'blocked' } as ImageProfile
const platform = { image_profiles: [profile, other], image_builds: [] } as unknown as PlatformConfig
const job: Job = { id: 'job-image', operation: 'image.build', state: 'running', request: { image_profile_id: profile.id }, target_vmid: 9202, target_node: 'build-node', progress: 1, detail: 'Queued', created_at: '2026-09-08T00:00:00Z', updated_at: '2026-09-08T00:00:01Z' }

describe('acknowledged image build state', () => {
  it.each([['accepted', 'building'], ['running', 'building'], ['succeeded', 'testing'], ['failed', 'failed']] as const)('maps %s to %s without claiming a validated desktop', (state, expected) => {
    const result = applyImageBuildJob(platform, { ...job, state })
    expect(result.image_profiles[0]).toMatchObject({ template_vmid: 9202, source_node: 'build-node', build_status: expected, source_iso: 'old.iso' })
    expect(result.image_profiles[1]).toBe(other)
    expect(profile.build_status).toBe('ready')
    expect(result.image_builds).toHaveLength(1)
  })

  it('ignores unrelated or unbound jobs', () => {
    for (const candidate of [{ ...job, operation: 'pve.template_clone' }, { ...job, request: undefined }, { ...job, request: { image_profile_id: 'missing' } }]) {
      expect(applyImageBuildJob(platform, candidate)).toBe(platform)
    }
  })

  it('keeps the latest server observation and carries failure detail', () => {
    const failed = { ...job, state: 'failed' as const, error: 'Build failed', updated_at: '2026-09-08T00:00:02Z' }
    const result = applyImageBuildJob(platform, failed)
    expect(result.image_profiles[0].status_detail).toBe(failed.error)
    expect(applyImageBuildJob(result, job)).toBe(result)
    expect(applyImageBuildJob(result, failed).image_builds).toHaveLength(1)
  })
})

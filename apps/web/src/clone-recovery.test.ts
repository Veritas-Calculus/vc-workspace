import { describe, expect, it } from 'vitest'
import type { CloneRecoveryEvidence, Job } from './api'
import { canInspectCloneRecovery, cloneRecoveryChoices } from './clone-recovery'

const job: Job = { id: 'job-a', operation: 'pve.template_clone', state: 'accepted', progress: 0, created_at: '', updated_at: '' }
const candidate: CloneRecoveryEvidence['candidates'][number] = { upid: 'UPID:test:1:', start_time: 100, status: 'stopped', exit_status: 'OK', log_target_status: 'matches' }
const evidence: CloneRecoveryEvidence = { job_id: job.id, target_status: 'marker_matches', candidates: [candidate] }

describe('clone recovery presentation gate', () => {
  it('only offers unresolved clones and never selects a task automatically', () => {
    expect(canInspectCloneRecovery(job)).toBe(true)
    expect(cloneRecoveryChoices(job, evidence)).toEqual([candidate.upid])
    for (const update of [{ state: 'running' }, { upid: candidate.upid }, { operation: 'image.build' }]) {
      expect(cloneRecoveryChoices({ ...job, ...update } as Job, evidence)).toEqual([])
    }
  })
  it('rejects absent, stale, locked and ambiguous evidence', () => {
    expect(cloneRecoveryChoices(job, null)).toEqual([])
    expect(cloneRecoveryChoices(job, { ...evidence, job_id: 'other' })).toEqual([])
    for (const target_status of ['target_absent', 'target_mismatch', 'target_locked'] as const) expect(cloneRecoveryChoices(job, { ...evidence, target_status })).toEqual([])
    expect(cloneRecoveryChoices(job, { ...evidence, candidates: [candidate, candidate] })).toEqual([])
  })
  it('does not enable running, failed or unsupported candidates', () => {
    for (const patch of [{ status: 'running' }, { exit_status: 'failed' }, { log_target_status: 'unrecognized' }, { log_target_status: 'mismatch' }, { start_time: NaN }, { log_target_status: undefined }]) {
      expect(cloneRecoveryChoices(job, { ...evidence, candidates: [{ ...candidate, ...patch } as typeof candidate] })).toEqual([])
    }
  })
})

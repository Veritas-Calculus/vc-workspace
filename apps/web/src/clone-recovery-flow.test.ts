import { afterEach, describe, expect, it, vi } from 'vitest'
import type { CloneRecoveryEvidence, Job } from './api'
import { createCloneRecoveryFlow, type CloneRecoveryState } from './clone-recovery-flow'

const job: Job = { id: 'job', operation: 'pve.template_clone', state: 'accepted', progress: 0, created_at: '', updated_at: '' }
const evidence: CloneRecoveryEvidence = { job_id: 'job', target_status: 'marker_matches', candidates: [{ upid: 'UPID:test:', start_time: 100, status: 'stopped', exit_status: 'OK', log_target_status: 'matches' }] }
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (reason: unknown) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no }); return { promise, resolve, reject } }

describe('clone recovery flow', () => {
  afterEach(() => { vi.useRealTimers() })
  it.each(['resolve', 'reject'])('bounds evidence waiting and ignores a late %s after the deadline', async outcome => {
    vi.useFakeTimers()
    const pending = deferred<CloneRecoveryEvidence>()
    const api = { evidence: vi.fn().mockReturnValueOnce(pending.promise).mockResolvedValue(evidence), submit: vi.fn(), job: vi.fn() }
    const update = vi.fn(), flow = createCloneRecoveryFlow(job, 'csrf', update, api)
    const loading = flow.load()
    await vi.advanceTimersByTimeAsync(34_999)
    expect(update).toHaveBeenLastCalledWith({ kind: 'loading' })
    await vi.advanceTimersByTimeAsync(1); await loading
    expect(update).toHaveBeenLastCalledWith({ kind: 'error', error: expect.any(DOMException) })
    expect(api.evidence.mock.calls[0][1].aborted).toBe(true)
    await flow.load()
    if (outcome === 'resolve') pending.resolve({ ...evidence, job_id: 'stale' })
    else pending.reject(new Error('late transport failure'))
    await Promise.resolve()
    expect(update).toHaveBeenLastCalledWith({ kind: 'ready', evidence })
    flow.dispose(); expect(vi.getTimerCount()).toBe(0)
  })
  it('bounds submission and checking without retrying a possibly committed POST', async () => {
    vi.useFakeTimers()
    const submission = deferred<Job>(), check = deferred<Job>()
    const api = { evidence: vi.fn().mockResolvedValue(evidence), submit: vi.fn().mockReturnValue(submission.promise), job: vi.fn().mockReturnValueOnce(check.promise).mockResolvedValue({ ...job, state: 'running', upid: 'UPID:test:' }) }
    const update = vi.fn(), flow = createCloneRecoveryFlow(job, 'csrf', update, api)
    await flow.load()
    const submitting = flow.submit('UPID:test:', 'reviewed')
    await vi.advanceTimersByTimeAsync(35_000); await submitting
    expect(update).toHaveBeenLastCalledWith({ kind: 'uncertain', upid: 'UPID:test:' })
    const checking = flow.check()
    await vi.advanceTimersByTimeAsync(35_000); await checking
    expect(update).toHaveBeenLastCalledWith({ kind: 'uncertain', upid: 'UPID:test:' })
    await flow.load(); await flow.submit('UPID:test:', 'retry')
    expect(api.submit).toHaveBeenCalledOnce(); expect(api.evidence).toHaveBeenCalledOnce()
    await flow.check()
    submission.resolve(job); check.resolve(job); await Promise.resolve()
    expect(update).toHaveBeenLastCalledWith(expect.objectContaining({ kind: 'settled' }))
    flow.dispose(); expect(vi.getTimerCount()).toBe(0)
  })
  it('settles disposed waits and clears their deadline even if transport never settles', async () => {
    vi.useFakeTimers()
    const api = { evidence: vi.fn().mockReturnValue(new Promise(() => {})), submit: vi.fn(), job: vi.fn() }
    const update = vi.fn(), flow = createCloneRecoveryFlow(job, 'csrf', update, api)
    const loading = flow.load(); flow.dispose(); update.mockClear(); await loading
    expect(update).not.toHaveBeenCalled(); expect(vi.getTimerCount()).toBe(0)
  })
  it('ignores old evidence and disposed responses even when transport ignores abort', async () => {
    const old = deferred<CloneRecoveryEvidence>(), next = deferred<CloneRecoveryEvidence>()
    const update = vi.fn()
    const api = { evidence: vi.fn().mockReturnValueOnce(old.promise).mockReturnValueOnce(next.promise), submit: vi.fn(), job: vi.fn() }
    const flow = createCloneRecoveryFlow(job, 'csrf', update, api)
    const a = flow.load(), b = flow.load()
    next.resolve(evidence); await b
    old.resolve({ ...evidence, job_id: 'stale' }); await a
    expect(update).toHaveBeenLastCalledWith({ kind: 'ready', evidence })
    const pending = deferred<CloneRecoveryEvidence>(); api.evidence.mockReturnValueOnce(pending.promise)
    const c = flow.load(); flow.dispose(); update.mockClear(); pending.resolve(evidence); await c
    expect(update).not.toHaveBeenCalled()
  })
  it('submits once and resolves uncertainty only by reading the original job', async () => {
    const pending = deferred<Job>()
    const api = { evidence: vi.fn().mockResolvedValue(evidence), submit: vi.fn().mockReturnValue(pending.promise), job: vi.fn().mockResolvedValue({ ...job, state: 'running', upid: 'UPID:test:' }) }
    const states: CloneRecoveryState[] = []
    const flow = createCloneRecoveryFlow(job, 'csrf', s => states.push(s), api)
    await flow.load()
    const first = flow.submit('UPID:test:', ' Reviewed ')
    await flow.submit('UPID:test:', ' Reviewed ')
    expect(api.submit).toHaveBeenCalledOnce()
    pending.resolve({ ...job, id: 'wrong' }); await first
    expect(states.at(-1)?.kind).toBe('uncertain')
    await flow.submit('UPID:test:', 'retry'); await flow.load()
    expect(api.submit).toHaveBeenCalledOnce(); expect(api.evidence).toHaveBeenCalledOnce()
    await flow.check()
    expect(api.job).toHaveBeenCalledWith('job', expect.any(AbortSignal))
    expect(states.at(-1)?.kind).toBe('settled')
    flow.dispose()
  })
  it('keeps failed checks and ambiguous submission responses uncertain', async () => {
    const api = { evidence: vi.fn().mockResolvedValue(evidence), submit: vi.fn().mockRejectedValue(new TypeError('offline')), job: vi.fn().mockResolvedValue(job) }
    const update = vi.fn(); const flow = createCloneRecoveryFlow(job, 'csrf', update, api)
    await flow.load(); await flow.submit('UPID:test:', 'reviewed'); await flow.check()
    expect(update).toHaveBeenLastCalledWith({ kind: 'uncertain', upid: 'UPID:test:' })
    api.job.mockRejectedValue(new Error('offline')); await flow.check()
    expect(update).toHaveBeenLastCalledWith({ kind: 'uncertain', upid: 'UPID:test:' })
    expect(api.submit).toHaveBeenCalledOnce(); flow.dispose()
  })
  it('does not report another recovered handle as success', async () => {
    const api = { evidence: vi.fn().mockResolvedValue(evidence), submit: vi.fn().mockRejectedValue(new Error('lost response')), job: vi.fn().mockResolvedValue({ ...job, state: 'running', upid: 'UPID:other:' }) }
    const update = vi.fn(); const flow = createCloneRecoveryFlow(job, 'csrf', update, api)
    await flow.load(); await flow.submit('UPID:test:', 'reviewed'); await flow.check()
    expect(update).toHaveBeenLastCalledWith(expect.objectContaining({ kind: 'conflict' }))
    await flow.load(); expect(api.evidence).toHaveBeenCalledOnce(); flow.dispose()
  })
})

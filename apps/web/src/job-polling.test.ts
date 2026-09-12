import { afterEach, describe, expect, it, vi } from 'vitest'
import { APIRequestError, getJob, type Job } from './api'
import { pollJob } from './job-polling'

vi.mock('./api', async (original) => ({ ...await original<typeof import('./api')>(), getJob: vi.fn() }))
afterEach(() => { vi.useRealTimers(); vi.resetAllMocks() })

describe('job polling recovery', () => {
  it('recovers from a temporary failure and completes without resubmitting the job', async () => {
    vi.useFakeTimers()
    vi.mocked(getJob).mockRejectedValueOnce(new TypeError('offline')).mockResolvedValueOnce({ id: 'job-a', state: 'succeeded' } as Job)
    const update = vi.fn(), status = vi.fn(), complete = vi.fn()
    const stop = pollJob('job-a', update, status, complete)
    await vi.advanceTimersByTimeAsync(1200)
    expect(status).toHaveBeenLastCalledWith(true)
    await vi.advanceTimersByTimeAsync(2400)
    expect(status).toHaveBeenLastCalledWith(false)
    expect(complete).toHaveBeenCalledOnce()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(getJob).toHaveBeenCalledTimes(2)
    stop()
  })

  it('does not overlap slow requests or update after cleanup', async () => {
    vi.useFakeTimers()
    let resolve!: (job: Job) => void
    vi.mocked(getJob).mockReturnValue(new Promise((done) => { resolve = done }))
    const update = vi.fn(), status = vi.fn()
    const stop = pollJob('job-a', update, status, vi.fn())
    await vi.advanceTimersByTimeAsync(12_000)
    expect(getJob).toHaveBeenCalledOnce()
    const signal = vi.mocked(getJob).mock.calls[0][1]
    stop()
    expect(signal?.aborted).toBe(true)
    resolve({ id: 'job-a', state: 'succeeded' } as Job)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(update).not.toHaveBeenCalled()
    expect(getJob).toHaveBeenCalledOnce()
  })

  it('stops automatic retries after permission denial', async () => {
    vi.useFakeTimers()
    vi.mocked(getJob).mockRejectedValue(new APIRequestError(403, 'permission_denied', 'Forbidden'))
    const stop = pollJob('job-a', vi.fn(), vi.fn(), vi.fn())
    await vi.advanceTimersByTimeAsync(60_000)
    expect(getJob).toHaveBeenCalledOnce()
    stop()
  })

  it('aborts hung reads at the deadline and retries', async () => {
    vi.useFakeTimers()
    vi.mocked(getJob).mockImplementation((_id, signal) => new Promise((_resolve, reject) => {
      signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true })
    }))
    const stop = pollJob('job-a', vi.fn(), vi.fn(), vi.fn())
    await vi.advanceTimersByTimeAsync(18_600)
    expect(getJob).toHaveBeenCalledTimes(2)
    stop()
  })
})

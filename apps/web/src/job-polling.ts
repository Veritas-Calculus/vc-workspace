import { APIRequestError, getJob, type Job } from './api'

export function isPendingJob(job: Job | null): boolean {
  return job?.state === 'accepted' || job?.state === 'running'
}

// One request at a time. Transient failures back off, terminal failures stop;
// neither a slow response nor cleanup can schedule another overlapping poll.
export function pollJob(id: string, update: (job: Job) => void, status: (failed: boolean) => void, complete: () => void) {
  const lifetime = new AbortController()
  let timer: ReturnType<typeof setTimeout> | undefined
  let failures = 0
  const tick = async () => {
    let retry = true
    const request = new AbortController()
    const abort = () => request.abort()
    lifetime.signal.addEventListener('abort', abort, { once: true })
    const deadline = setTimeout(() => request.abort(), 15_000)
    try {
      const next = await getJob(id, request.signal)
      if (lifetime.signal.aborted) return
      failures = 0
      status(false)
      update(next)
      if (!isPendingJob(next)) { retry = false; complete() }
    } catch (error) {
      if (lifetime.signal.aborted) return
      failures += 1
      status(true)
      if (error instanceof APIRequestError && [400, 401, 403, 404, 410].includes(error.status)) retry = false
    } finally {
      clearTimeout(deadline)
      lifetime.signal.removeEventListener('abort', abort)
      if (retry && !lifetime.signal.aborted) timer = setTimeout(tick, Math.min(30_000, 1200 * 2 ** Math.min(failures, 5)))
    }
  }
  timer = setTimeout(tick, 1200)
  return () => { lifetime.abort(); clearTimeout(timer) }
}

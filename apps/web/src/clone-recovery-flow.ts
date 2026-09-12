import { getCloneRecoveryEvidence, getJob, recoverClone, type CloneRecoveryEvidence, type Job } from './api'
import { cloneRecoveryChoices } from './clone-recovery'

export type CloneRecoveryState =
  | { kind: 'idle' | 'loading' }
  | { kind: 'ready'; evidence: CloneRecoveryEvidence }
  | { kind: 'error'; error: unknown }
  | { kind: 'submitting' | 'uncertain' | 'checking'; upid: string }
  | { kind: 'settled' | 'conflict'; job: Job }

type Dependencies = { evidence: typeof getCloneRecoveryEvidence; submit: typeof recoverClone; job: typeof getJob }

// One owner for request ordering and duplicate suppression. The route owns
// translated presentation; this controller never stores drafts or retries POST.
export function createCloneRecoveryFlow(job: Job, csrf: string, update: (state: CloneRecoveryState) => void, api: Dependencies = { evidence: getCloneRecoveryEvidence, submit: recoverClone, job: getJob }) {
  let state: CloneRecoveryState = { kind: 'idle' }
  let revision = 0
  let disposed = false
  let active: AbortController | undefined
  const set = (next: CloneRecoveryState) => { state = next; update(next) }
  const request = () => {
    active?.abort()
    const controller = new AbortController()
    active = controller
    const version = ++revision
    let rejectCancelled!: (reason: unknown) => void
    const cancelled = new Promise<never>((_, reject) => { rejectCancelled = reject })
    const onAbort = () => rejectCancelled(new DOMException('Recovery request cancelled', 'AbortError'))
    controller.signal.addEventListener('abort', onAbort, { once: true })
    const timer = setTimeout(() => controller.abort(), 35_000)
    return {
      signal: controller.signal,
      // Bound UI waiting even if a transport ignores cancellation. Promise.race
      // also consumes a late rejection without letting it replace the result.
      wait: <T,>(operation: Promise<T>) => Promise.race([operation, cancelled]),
      current: () => !disposed && version === revision,
      finish: () => { clearTimeout(timer); controller.signal.removeEventListener('abort', onAbort) },
    }
  }
  return {
    async load() {
      if (disposed || ['submitting', 'checking', 'uncertain', 'settled', 'conflict'].includes(state.kind)) return
      const pending = request()
      set({ kind: 'loading' })
      try {
        const evidence = await pending.wait(api.evidence(job.id, pending.signal))
        if (pending.current()) set({ kind: 'ready', evidence })
      } catch (error) {
        if (pending.current()) set({ kind: 'error', error })
      } finally { pending.finish() }
    },
    async submit(upid: string, reason: string) {
      if (disposed || state.kind !== 'ready' || !cloneRecoveryChoices(job, state.evidence).includes(upid) || !reason.trim() || [...reason.trim()].length > 500) return
      const pending = request()
      set({ kind: 'submitting', upid })
      try {
        const result = await pending.wait(api.submit(job.id, { upid, reason: reason.trim() }, csrf, pending.signal))
        if (pending.current()) set(result.id === job.id && result.upid === upid && result.state !== 'accepted' ? { kind: 'settled', job: result } : { kind: 'uncertain', upid })
      } catch {
        // Even a timeout or abort can follow a committed database transaction.
        if (pending.current()) set({ kind: 'uncertain', upid })
      } finally { pending.finish() }
    },
    async check() {
      if (disposed || state.kind !== 'uncertain') return
      const upid = state.upid
      const pending = request()
      set({ kind: 'checking', upid })
      try {
        const result = await pending.wait(api.job(job.id, pending.signal))
        if (pending.current()) set(result.id === job.id && !!result.upid && result.state !== 'accepted' ? { kind: result.upid === upid ? 'settled' : 'conflict', job: result } : { kind: 'uncertain', upid })
      } catch {
        if (pending.current()) set({ kind: 'uncertain', upid })
      } finally { pending.finish() }
    },
    dispose() { disposed = true; revision++; active?.abort() },
  }
}

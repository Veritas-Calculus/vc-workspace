import { useEffect, useId, useRef, useState } from 'react'
import type { Job } from '../api'
import { cloneRecoveryChoices } from '../clone-recovery'
import { createCloneRecoveryFlow, type CloneRecoveryState } from '../clone-recovery-flow'
import { FormField } from '../components/form-field'
import type { Translator } from './workspaces'

export function CloneRecoveryForm({ job, csrf, t, onJob, onClose }: { job: Job; csrf: string; t: Translator; onJob: (job: Job) => void; onClose: () => void }) {
  const [state, setState] = useState<CloneRecoveryState>({ kind: 'loading' })
  const [upid, setUPID] = useState('')
  const [reason, setReason] = useState('')
  const selectID = useId()
  const heading = useRef<HTMLHeadingElement>(null)
  useEffect(() => { heading.current?.focus() }, [])
  const flow = useRef<ReturnType<typeof createCloneRecoveryFlow> | null>(null)
  const notify = useRef(onJob)
  notify.current = onJob
  useEffect(() => {
    const current = createCloneRecoveryFlow(job, csrf, next => {
      setState(next)
      if (next.kind === 'settled' || next.kind === 'conflict') notify.current(next.job)
    })
    flow.current = current
    void current.load()
    return () => { current.dispose(); flow.current = null }
  }, [job, csrf])
  const choices = state.kind === 'ready' ? cloneRecoveryChoices(job, state.evidence) : []
  const busy = state.kind === 'loading' || state.kind === 'submitting' || state.kind === 'checking'
  const canSubmit = choices.includes(upid) && !!reason.trim() && [...reason.trim()].length <= 500
  const refresh = () => { heading.current?.focus(); setUPID(''); void flow.current?.load() }
  return <section className="clone-recovery" aria-label={t('cloneRecovery')}>
    <div className="section-heading"><h3 ref={heading} tabIndex={-1}>{t('cloneRecovery')} · VM {job.target_vmid}</h3><button type="button" className="button" onClick={onClose}>{t('cloneRecoveryClose')}</button></div>
    <p className="field-hint">{t('cloneRecoveryHint')}</p>
    <div role="status" aria-live="polite">
      {busy && t(state.kind === 'submitting' ? 'cloneRecoverySubmitting' : 'cloneRecoveryLoading')}
      {state.kind === 'settled' && t('cloneRecoveryRecorded')}
      {state.kind === 'conflict' && t('cloneRecoveryConflict')}
      {state.kind === 'uncertain' && t('cloneRecoveryUncertain')}
    </div>
    {state.kind === 'error' && <p role="alert">{t('cloneRecoveryLoadFailed')}</p>}
    {(state.kind === 'error' || state.kind === 'ready') && <button type="button" className="button" onClick={refresh}>{t('refresh')}</button>}
    {state.kind === 'uncertain' && <button type="button" className="button" onClick={() => { heading.current?.focus(); void flow.current?.check() }}>{t('cloneRecoveryCheck')}</button>}
    {state.kind === 'ready' && (choices.length === 0 ? <p>{t('cloneRecoveryNoMatch')}</p> : <form onKeyDown={event => { if (event.key === 'Enter' && event.nativeEvent.isComposing) event.preventDefault() }} onSubmit={event => { event.preventDefault(); if (canSubmit) { heading.current?.focus(); void flow.current?.submit(upid, reason) } }}>
      <div className="form-field"><label htmlFor={selectID}>{t('cloneRecoveryCandidate')}</label><select id={selectID} value={upid} required onChange={event => setUPID(event.target.value)}><option value="">{t('cloneRecoveryChoose')}</option>{choices.map(value => <option key={value} value={value}>{value}</option>)}</select></div>
      <FormField label={t('cloneRecoveryReason')} value={reason} onChange={setReason} autoComplete="off" maxLength={500} />
      <button type="submit" className="button button-primary" aria-disabled={!canSubmit} onClick={event => { if (!canSubmit) event.preventDefault() }}>{t('cloneRecoverySubmit')}</button>
    </form>)}
  </section>
}

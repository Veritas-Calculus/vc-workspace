import type { CloneRecoveryEvidence, Job } from './api'

export function canInspectCloneRecovery(job: Job): boolean {
  return job.operation === 'pve.template_clone' && job.state === 'accepted' && !job.upid
}

// Presentation gating is not authorization: the server re-reads all evidence
// when submitting. Treat malformed/unknown responses as non-actionable.
export function cloneRecoveryChoices(job: Job, evidence: CloneRecoveryEvidence | null): string[] {
  if (!canInspectCloneRecovery(job) || evidence?.job_id !== job.id || evidence.target_status !== 'marker_matches' || !Array.isArray(evidence.candidates) || evidence.candidates.length > 50) return []
  const seen = new Set<string>()
  for (const candidate of evidence.candidates) {
    if (!candidate || typeof candidate.upid !== 'string' || !candidate.upid.startsWith('UPID:') || seen.has(candidate.upid) || !Number.isSafeInteger(candidate.start_time) || candidate.start_time <= 0) return []
    seen.add(candidate.upid)
  }
  return evidence.candidates.filter(candidate => candidate.status === 'stopped' && candidate.exit_status === 'OK' && candidate.log_target_status === 'matches').map(candidate => candidate.upid)
}

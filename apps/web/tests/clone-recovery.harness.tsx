import React, { useState } from 'react'
import { createRoot } from 'react-dom/client'
import { ActivityView } from '../src/console/workspaces'
import type { Job } from '../src/api'
import { message, type Locale } from '../src/i18n'
import '../src/styles.css'

const params = new URLSearchParams(location.search)
const locale: Locale = params.get('lang') === 'zh-CN' ? 'zh-CN' : 'en'
document.documentElement.lang = locale
document.documentElement.dataset.theme = params.get('theme') === 'dark' ? 'dark' : 'light'
const job: Job = { id: 'fixture', operation: 'pve.template_clone', state: 'accepted', target_vmid: 9204, progress: 0, created_at: '2026-09-08T00:00:00Z', updated_at: '2026-09-08T00:00:00Z' }
const upid = 'UPID:infra-node6:00398DBD:03646AC7:6A9F7F50:qmclone:9202:test@pve!isolated-recovery:'
window.fetch = async (path, init) => {
  if (String(path).endsWith('/clone-candidates')) {
    if (params.get('evidence') === 'error') return new Response('{}', { status: 503 })
    return new Response(JSON.stringify({ job_id: job.id, target_status: 'marker_matches', candidates: params.get('evidence') === 'empty' ? [] : [{ upid, start_time: 100, status: 'stopped', exit_status: 'OK', log_target_status: 'matches' }] }))
  }
  if (init?.method === 'POST' && params.get('outcome') === 'uncertain') throw new TypeError('fixture response loss')
  return new Response(JSON.stringify({ ...job, state: 'running', upid }))
}
function Harness() {
  const [current, setCurrent] = useState(job)
  return <ActivityView jobs={[current]} locale={locale} csrf="fixture-only" t={(key, variables) => message(locale, key, variables)} onJob={setCurrent} />
}
createRoot(document.getElementById('root')!).render(<Harness />)

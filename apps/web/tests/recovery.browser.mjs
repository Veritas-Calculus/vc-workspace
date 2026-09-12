import assert from 'node:assert/strict'
import { mkdtemp } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { chromium } from 'playwright'

const origin = process.env.VC_WORKSPACE_WEB_TEST_URL || 'http://127.0.0.1:5188'
const artifacts = await mkdtemp(join(tmpdir(), 'vcw-web-recovery-'))
const browser = await chromium.launch({ headless: true })
const system = { name: 'VC Workspace', initialized: true, database_configured: true, pve_configured: true, oidc_configured: false, identity_capabilities: {} }
const session = { user: { id: 'review', username: 'review', display_name: 'Review admin', role: 'platform_admin', identity_kind: 'local' }, csrf_token: 'test-only' }
const machine = { vmid: 9001, name: 'Recovery desktop', kind: 'qemu', node: 'test-node', status: 'stopped', managed: true, template: false, cpu_count: 2, memory_total: 2147483648 }
const infrastructure = { version: { version: 'test' }, cluster: { name: 'Test', quorate: true }, nodes: [], virtual_machines: [machine], storage: [], mutations_enabled: true }
const job = { id: 'job-test', operation: 'pve.vm_start', state: 'running', target_vmid: 9001, progress: 1, created_at: '2026-09-05T00:00:00Z', updated_at: '2026-09-05T00:00:00Z' }

try {
  for (const locale of ['en', 'zh-CN']) for (const theme of ['light', 'dark']) {
    const context = await browser.newContext({ viewport: { width: 1280, height: 900 } })
    await context.addInitScript(({ locale, theme }) => {
      localStorage.setItem('vc-workspace-locale', locale)
      localStorage.setItem('vc-workspace-theme', theme)
    }, { locale, theme })
    let expired = false, submissions = 0, reads = 0
    await context.route('**/api/v1/**', async (route) => {
      const path = new URL(route.request().url()).pathname
      const respond = (body, status = 200) => route.fulfill({ status, json: body })
      if (path === '/api/v1/system') return respond(system)
      if (path === '/api/v1/auth/login') { expired = false; return respond(session) }
      if (expired) return respond({ error: { code: 'session_expired', message: 'Authentication required' } }, 401)
      if (path === '/api/v1/me') return respond(session)
      if (path === '/api/v1/infrastructure') return respond(infrastructure)
      if (path === '/api/v1/platform-config') return respond({ image_profiles: [], gpu_profiles: [], gpu_devices: [], pci_resource_mappings: [], image_builds: [] })
      if (path === '/api/v1/access-control') return respond({ users: [], agents: [], groups: [], group_memberships: [], assignments: [], desktops: [], identity_profiles: [] })
      if (path === '/api/v1/jobs') return respond({ jobs: [] })
      if (path === '/api/v1/virtual-machines/9001/actions/start') { submissions++; return respond(job, 202) }
      if (path === '/api/v1/jobs/job-test') {
        reads++
        if (reads === 1) return respond({ error: { code: 'unavailable' } }, 502)
        return respond({ ...job, state: 'succeeded', progress: 100 })
      }
      throw new Error(`Uncovered API fixture: ${path}`)
    })
    const page = await context.newPage()
    const errors = []
    page.on('pageerror', (error) => errors.push(error.message))
    await page.goto(origin + '/console')
    await page.getByRole('button', { name: /^(Start|启动) Recovery desktop$/ }).click()
    await page.getByText(/Task updates are temporarily unavailable|任务状态暂时无法更新/).waitFor()
    for (const width of [1280, 320]) {
      await page.setViewportSize({ width, height: 900 })
      await page.screenshot({ path: join(artifacts, `poll-error.${width}.${theme}.${locale}.png`) })
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true)
    }
    await page.getByText(/PVE task completed|PVE 任务已完成/).waitFor({ timeout: 15_000 })
    assert.equal(submissions, 1, 'a read retry repeated the power mutation')
    assert.equal(reads, 2)
    expired = true
    await page.getByRole('button', { name: /^(Refresh|刷新)$/ }).click()
    await page.getByRole('form', { name: /^(Sign in|登录)$/ }).waitFor()
    assert.match(page.url(), /session=expired/)
    assert.equal(await page.getByText('Authentication required').count(), 0)
    for (const width of [1280, 320]) {
      await page.setViewportSize({ width, height: 900 })
      await page.screenshot({ path: join(artifacts, `expired.${width}.${theme}.${locale}.png`) })
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), true)
    }
    // Check every responsive width on the rendered route, not CSS element width.
    console.log(`geometry sweep: ${locale}/${theme}`)
    for (let width = 320; width <= 1280; width++) {
      await page.setViewportSize({ width, height: 900 })
      assert.equal(await page.evaluate(async () => {
        await new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done)))
        return document.documentElement.scrollWidth <= document.documentElement.clientWidth
      }), true, `overflow at ${width}/${locale}/${theme}`)
    }
    await page.getByLabel(locale === 'en' ? 'Username' : '用户名', { exact: true }).focus()
    await page.keyboard.type('review')
    await page.keyboard.press('Tab')
    assert.equal(await page.locator('input[type=password]').evaluate((input) => input === document.activeElement), true)
    await page.keyboard.type('fixture-only-password')
    await page.keyboard.press('Enter')
    await page.getByRole('button', { name: /^(Start|启动) Recovery desktop$/ }).waitFor()
    assert.equal(new URL(page.url()).search, '')
    assert.deepEqual(errors, [])
    console.log(`PASS expired-session recovery, keyboard login, job retry: ${locale}/${theme}`)
    await context.close()
  }
  console.log(`Screenshots: ${artifacts}`)
} finally {
  await browser.close()
}

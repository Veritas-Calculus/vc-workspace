// Human/CUA drives the unchanged .app. This operator tool provisions only the
// isolated lab, observes the real Guest/database, and uses normal authorization
// APIs for revocation. It never substitutes a mock Broker or creates RDP tickets.
import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import {createHash, randomBytes} from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import {gzipSync} from 'node:zlib';
import {setTimeout as delay} from 'node:timers/promises';
import {privateStateSafe, verifyGuestTarget} from '../deploy/kubernetes/infra-bootstrap.mjs';

const root = path.resolve('.cache/infra-gateway-lab');
const file = path.join(root, 'run.json');
const pending = path.join(root, 'qga-pending.json');
const origin = 'https://ws.infra.plz.ac';
const guestRoot = '/nodes/infra-node1/qemu/160';
const binary = '/usr/local/sbin/vc-workspace-guest-agent';
let state, cookie, csrf;
function save(name, data, exclusive = false) {
  const destination = path.join(root, name);
  if (exclusive) fs.writeFileSync(destination, JSON.stringify(data), {mode: 0o600, flag: 'wx'});
  else {
    const temporary = destination + '.' + randomBytes(6).toString('hex');
    fs.writeFileSync(temporary, JSON.stringify(data), {mode: 0o600, flag: 'wx'});
    fs.renameSync(temporary, destination);
  }
}
function readPrivate(location) {
  assert.ok(privateStateSafe(fs.lstatSync(location), process.getuid()));
  return JSON.parse(fs.readFileSync(location, 'utf8'));
}
async function api(route, method = 'GET', value) {
  const response = await fetch(origin + route, {method, redirect: 'error', signal: AbortSignal.timeout(30000),
    headers: {'Content-Type': 'application/json', Origin: origin, ...(cookie ? {Cookie: cookie, 'X-CSRF-Token': csrf} : {})},
    body: value === undefined ? undefined : JSON.stringify(value)});
  const body = response.status === 204 ? null : await response.json();
  assert.ok(response.ok, `API ${method} ${route}: ${response.status} ${body?.error?.code ?? ''}`);
  if (route === '/api/v1/auth/login') {
    cookie = response.headers.getSetCookie().map(s => s.split(';')[0]).join('; ');
    csrf = body.csrf_token;
  }
  return body;
}
const token = readPrivate(path.resolve('.cache/infra-bootstrap/pve-token.json'));
assert.ok(token.id.endsWith('!vc-workspace-infra'));
async function pve(route, method = 'GET', body) {
  assert.ok(route === guestRoot + '/config' || route === guestRoot + '/status/current' || route === guestRoot + '/agent/network-get-interfaces' ||
    route === guestRoot + '/agent/exec' || route.startsWith(guestRoot + '/agent/exec-status?pid='));
  assert.ok(method === 'GET' || (method === 'POST' && route === guestRoot + '/agent/exec'));
  const response = await fetch('https://pve.infra.plz.ac/api2/json' + route, {method, body, redirect: 'error',
    headers: {Authorization: `PVEAPIToken=${token.id}=${token.secret}`}, signal: AbortSignal.timeout(20000)});
  if (!response.ok) save(`pve-error-${Date.now()}.json`, {status: response.status, body: await response.text()}, true);
  assert.ok(response.ok, `Scoped PVE request failed: ${response.status}`);
  return (await response.json()).data;
}
async function pollQGA(record) {
  assert.ok(Number.isInteger(record.pid) && record.pid > 0, 'Unknown dispatch result: inspect Guest before any retry');
  for (let n = 0; n < 180; n++) {
    const result = await pve(guestRoot + '/agent/exec-status?pid=' + record.pid);
    if ([true, 1, '1'].includes(result.exited)) {
      save(`qga-${record.pid}-${Date.now()}.json`, result, true);
      save('last-qga.json', result);
      fs.unlinkSync(pending);
      assert.ok(Number.isInteger(result.exitcode) && result.exitcode === 0 && result.signal === undefined &&
        ![true, 1, '1'].includes(result['out-truncated']) && ![true, 1, '1'].includes(result['err-truncated']),
      'Guest command failed; private diagnostics retained, do not blindly repeat');
      return result['out-data'] ?? '';
    }
    assert.ok([false, 0, '0'].includes(result.exited), 'Invalid QGA completion flag');
    await delay(250);
  }
  throw new Error('QGA still running: use poll, never resubmit');
}
async function qga(command, input) {
  assert.ok(!fs.existsSync(pending), 'Unresolved Guest command: use poll or inspect the recorded dispatch');
  const form = new URLSearchParams();
  for (const arg of command) form.append('command', arg);
  if (input !== undefined) {
    const serialized = typeof input === 'string' ? input : JSON.stringify(input);
    assert.ok(Buffer.byteLength(serialized, 'utf8') <= 65536, 'PVE stdin limit exceeded before dispatch');
    form.set('input-data', serialized);
  }
  save('qga-pending.json', {dispatching: true, command_sha256: createHash('sha256').update(JSON.stringify(command)).digest('hex')}, true);
  const result = await pve(guestRoot + '/agent/exec', 'POST', form);
  save('qga-pending.json', {pid: result.pid});
  return pollQGA(result);
}
async function services(operation) {
  const units = Object.fromEntries(['vc-workspace-agent.service', 'vc-workspace-accounts.service', 'vc-workspace-accounts.timer']
    .map(name => [name, fs.readFileSync('deploy/guest/linux/' + name, 'utf8')]));
  const installer = fs.readFileSync('deploy/guest/linux/install-login-fence.py', 'utf8');
  return qga(['/usr/bin/python3', '-c', fs.readFileSync('internal/httpapi/guest_service_fixture.py', 'utf8'), operation, state.marker, 'native'],
    {units, login_fence_installer: installer});
}
async function account(operation, ...extra) {
  return qga(['/usr/bin/python3', '-c', fs.readFileSync('internal/httpapi/native_guest_fixture.py', 'utf8'), operation, state.marker, state.guest_username, ...extra]);
}
async function configuration(operation, ...extra) {
  return qga(['/usr/bin/python3', '-c', fs.readFileSync('tools/infra-gateway-guest-fixture.py', 'utf8'), operation, state.marker, ...extra]);
}
async function artifacts(operation, input) {
  return qga(['/usr/bin/python3', '-c', fs.readFileSync('tools/infra-gateway-artifacts.py', 'utf8'), operation, state.marker], input);
}
async function observe() {
  const result = JSON.parse(await qga([binary, 'computer-v2-native-account-inspect', '--guest-user', state.guest_username]));
  if (result.account?.exists && !state.bound) {
    await account('bind'); state.bound = true; save('run.json', state);
  }
  return result;
}
function sql(query) {
  assert.match(state.user_id, /^[A-Za-z0-9_-]{24}$/);
  return execFileSync('kubectl', ['--context', 'kubernetes-admin@infra.homelab', '-n', 'vc-workspace', 'exec', 'vc-workspace-postgres-0', '--',
    'psql', '-U', 'vc_workspace', '-d', 'vc_workspace', '-Atc', query], {encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe'], timeout: 10000}).trim();
}
async function main() {
  assert.equal(process.env.VC_WORKSPACE_LIVE_INFRA_MAC_LAB, 'true', 'Explicit live lab opt-in required');
  fs.mkdirSync(root, {mode: 0o700, recursive: true});
  assert.ok(privateStateSafe(fs.lstatSync(root), process.getuid(), true) && fs.realpathSync(root) === root);
  const action = process.argv[2];
  if (action === 'poll') { await pollQGA(readPrivate(pending)); console.log('Prior QGA command completed; no command was resubmitted'); return; }
  const vm = await pve(guestRoot + '/config');
  assert.equal(vm.name, 'vc-vdi-mvp-gvtg-check'); assert.equal(Number(vm.memory), 4096);
  assert.equal((await pve(guestRoot + '/status/current')).status, 'running');
  if (['prepare', 'prepare-next', 'resume-setup', 'preconnect', 'grant'].includes(action)) {
    const config = JSON.parse(execFileSync('kubectl', ['--context', 'kubernetes-admin@infra.homelab', '-n', 'vc-workspace',
      'get', 'configmap', 'vc-workspace-targets', '-o', 'json'], {encoding: 'utf8', timeout: 10000}));
    const cidr = config.data['allowed-targets'];
    assert.match(cidr, /^10\.31\.0\.[0-9]+\/32$/);
    verifyGuestTarget(cidr.slice(0, -3), vm, (await pve(guestRoot + '/agent/network-get-interfaces')).result);
  }
  const admin = readPrivate(path.resolve('.cache/infra-bootstrap/admin.json'));
  await api('/api/v1/auth/login', 'POST', {username: admin.username, password: admin.password});
  try {
    if (action === 'prepare' || action === 'prepare-next') {
      const inventory = await api('/api/v1/access-control');
      assert.ok(inventory.desktops.some(d => d.vmid === 160 && d.os_family === 'linux'));
      assert.ok(!inventory.assignments.some(a => a.desktop_vmid === 160));
      let floor;
      if (action === 'prepare-next') {
        assert.ok(!fs.existsSync(pending), 'Resolve the previous Guest operation before a new run');
        state = readPrivate(file);
        assert.equal(state.phase, 'cleaned'); assert.equal(state.password, '');
        assert.match(state.marker, /^svc[0-9a-f]{24}$/);
        const previous = inventory.users.find(user => user.id === state.user_id);
        assert.equal(previous?.username, state.username); assert.equal(previous.disabled, true);
        assert.equal(sql("SELECT count(*) FROM desktop_connection_sessions WHERE desktop_vmid=160 AND (state NOT IN ('revoked','expired') OR closed_at IS NULL);"), '0');
        floor = Number(sql('SELECT COALESCE(MAX(guest_uid),1000)+1 FROM (SELECT guest_uid FROM native_guest_accounts WHERE desktop_vmid=160 UNION ALL SELECT guest_uid FROM agent_guest_sessions WHERE desktop_vmid=160) u;'));
        assert.ok(Number.isSafeInteger(floor) && floor >= 1001 && floor <= 60000);
        save('completed-' + state.marker + '.json', state, true);
      } else assert.ok(!fs.existsSync(file), 'Existing lab state: inspect and clean it before creating another');
      state = {marker: 'svc' + randomBytes(12).toString('hex'), server: origin, username: 'gateway-' + randomBytes(6).toString('hex'),
        password: randomBytes(24).toString('base64url'), phase: 'user-create-pending'};
      save('run.json', state, action === 'prepare');
      const user = await api('/api/v1/users', 'POST', {username: state.username, password: state.password, display_name: 'Gateway acceptance'});
      state.user_id = user.id;
      state.guest_username = 'vcw' + createHash('sha256').update(user.id).digest('hex').slice(0, 12);
      state.phase = 'guest-setup-pending'; save('run.json', state);
      await qga(['/bin/sh', '-c', 'set -eu; test "$(. /etc/os-release; echo $VERSION_ID)" = 13; test -z "$(pgrep -x Xorg || true)"; test -z "$(pgrep -x xrdp-sesexec || true)"']);
      await configuration('backup');
      if (floor !== undefined) {
        await configuration('uid-floor', String(floor));
        state.uid_floor = floor; state.phase = 'services-setup-pending'; save('run.json', state);
        console.log('New isolated identity prepared; install-agent and resume-setup required before login');
        return;
      }
      await services('setup');
      await account('reserve');
      await api(`/api/v1/desktop-assignments/user/${user.id}/160`, 'PUT', {});
      state.phase = 'ready'; save('run.json', state);
      console.log(`Lab prepared, no OS account pre-created. Client credentials: ${file}`);
      return;
    }
    state = readPrivate(file);
    assert.match(state.marker, /^svc[0-9a-f]{24}$/); assert.match(state.guest_username, /^vcw[0-9a-f]{12}$/);
    assert.match(state.user_id, /^[A-Za-z0-9_-]{24}$/);
    if (action === 'install-agent' || action === 'resume-empty-upload') {
      assert.equal(state.phase, action === 'install-agent' ? 'services-setup-pending' : 'artifacts-begin-pending');
      await configuration('verify');
      const names = ['vc-workspace-guest-agent', 'pam_vcworkspace.so', 'pam_vcworkspace_native.so'];
      const payload = Object.fromEntries(names.map(name => [name, fs.readFileSync('.cache/infra-gateway-agent/' + name)]));
      if (process.env.VC_WORKSPACE_LAB_PATCHED_XRDP === 'true') {
        assert.equal(action, 'install-agent', 'Patched xrdp requires a fresh, idle lab installation');
        const checksum = fs.readFileSync('.cache/xrdp-resize-build/xrdp.sha256', 'utf8');
        assert.match(checksum, /^[a-f0-9]{64}  \/out\/root\/usr\/sbin\/xrdp\n$/);
        payload.xrdp = fs.readFileSync('.cache/xrdp-resize-build/root/usr/sbin/xrdp');
        assert.equal(createHash('sha256').update(payload.xrdp).digest('hex'), checksum.slice(0, 64));
        names.push('xrdp');
      }
      const hashes = Object.fromEntries(names.map(name => [name, createHash('sha256').update(payload[name]).digest('hex')]));
      if (action === 'install-agent') {
        state.phase = 'artifacts-begin-pending'; save('run.json', state);
        await artifacts('begin', hashes);
      } else {
        assert.deepEqual(state.upload, {name: names[0], offset: 0});
        await artifacts('verify-empty', hashes);
      }
      for (const name of names) {
        const compressed = gzipSync(payload[name]);
        for (let offset = 0; offset < compressed.length; offset += 32768) {
          state.upload = {name, offset}; save('run.json', state);
          await artifacts('chunk', {name, offset, data: compressed.subarray(offset, offset + 32768).toString('base64')});
        }
      }
      state.phase = 'artifacts-install-pending'; save('run.json', state);
      console.log((await artifacts('install')).trim());
      state.phase = 'guest-setup-pending'; state.artifacts_installed = true; delete state.upload; save('run.json', state);
    } else if (action === 'inspect-setup') {
      const script = `import os,pathlib,json,subprocess,sys
marker,username=sys.argv[1:]
paths=['/run/vc-workspace-infra-'+marker,'/run/vc-workspace-'+marker,'/var/lib/vc-workspace/computer-v2/users/'+username,'/home/'+username,'/etc/vc-workspace','/usr/share/backgrounds/vc-workspace','/etc/xdg/autostart']
paths += ['/etc/systemd/system/'+n for n in ('vc-workspace-agent.service','vc-workspace-accounts.service','vc-workspace-accounts.timer')]
result={}
for name in paths:
 p=pathlib.Path(name)
 result[name]={'exists':os.path.lexists(p),'symlink':p.is_symlink()}
 if p.is_dir() and not p.is_symlink(): result[name]['entries']=[v.name for v in p.iterdir()]
result['xorg']=subprocess.run(['pgrep','-x','Xorg'],capture_output=True,text=True).stdout.strip()
result['sesexec']=subprocess.run(['pgrep','-x','xrdp-sesexec'],capture_output=True,text=True).stdout.strip()
result['passwd_exit']=subprocess.run(['getent','passwd',username],capture_output=True).returncode
result['pam_has_fence']='computer-v2-pam-register' in pathlib.Path('/etc/pam.d/xrdp-sesman').read_text()
print(json.dumps(result))`;
      console.log(await qga(['/usr/bin/python3', '-c', script, state.marker, state.guest_username]));
    } else if (action === 'resume-setup') {
      assert.equal(state.phase, 'guest-setup-pending');
      await configuration('verify');
      state.phase = 'services-setup-pending'; save('run.json', state);
      await services('setup');
      state.phase = 'reserve-pending'; save('run.json', state);
      await account('reserve');
      state.phase = 'assignment-pending'; save('run.json', state);
      await api(`/api/v1/desktop-assignments/user/${state.user_id}/160`, 'PUT', {});
      state.phase = 'ready'; save('run.json', state);
      console.log('Resumed from verified configuration baseline; no user or OS account duplicated');
    } else if (action === 'transport-log') {
      console.log(await qga(['/usr/bin/python3', '-c', `import pathlib
lines=pathlib.Path('/var/log/xrdp.log').read_text().splitlines()
print('\\n'.join(lines[-80:]))`]));
    } else if (action === 'status') {
      const v = await observe();
      console.log(JSON.stringify({guest: {exists: v.account?.exists, identity: v.identity, phase: v.lifecycle?.phase, revision: v.lifecycle?.revision,
        disabled: v.account?.disabled, processes_absent: v.processes_absent, login_writers_absent: v.login_writers_absent}}));
      console.log(sql(`SELECT json_build_object('issued',count(*),'consumed',count(t.consumed_at),'active',count(*) FILTER (WHERE t.consumed_at IS NOT NULL AND t.closed_at IS NULL AND t.lease_until>now()),'closed',count(t.closed_at)) FROM gateway_session_tickets t JOIN desktop_connection_sessions c ON c.id=t.connection_id WHERE c.user_id='${state.user_id}';`));
    } else if (action === 'desktop') {
      await observe(); const d = JSON.parse(await account('desktop'));
      console.log(JSON.stringify({uid: d.uid, pid: d.pid, ticks: d.ticks}));
    } else if (action === 'preconnect') {
      assert.equal(state.phase, 'ready');
      console.log((await account('preconnect', '1')).trim());
    } else if (action === 'checkpoint' || action === 'connected') {
      await observe(); assert.match(process.argv[3], /^[1-9]$/);
      console.log((await account(action, process.argv[3])).trim());
    } else if (action === 'seal-policy') {
      assert.ok(!state.policy_sealed); await configuration('seal'); state.policy_sealed = true; save('run.json', state);
    } else if (action === 'revoke' || action === 'grant') {
      await api(`/api/v1/desktop-assignments/user/${state.user_id}/160`, action === 'grant' ? 'PUT' : 'DELETE', action === 'grant' ? {} : undefined);
      console.log(`Real authorization API: ${action}`);
    } else if (action === 'cleanup') {
      const inventory = await api('/api/v1/access-control');
      const owner = inventory.users.find(user => user.id === state.user_id);
      assert.equal(owner?.username, state.username);
      if (!owner.disabled) await api(`/api/v1/users/${state.user_id}`, 'PATCH', {disabled: true});
      if (inventory.assignments.some(a => a.desktop_vmid === 160 && a.subject_id === state.user_id)) {
        await api(`/api/v1/desktop-assignments/user/${state.user_id}/160`, 'DELETE');
      }
      const v = await observe();
      assert.ok(v.account?.exists && v.lifecycle?.phase === 'revoked' && v.account.disabled && v.processes_absent && v.login_writers_absent,
        'Await product revocation, then rerun cleanup; Guest artifacts retained');
      const pendingCount = sql(`SELECT (SELECT count(*) FROM native_guest_accounts WHERE user_id='${state.user_id}' AND (state<>'applied' OR operation<>'revoke')) + (SELECT count(*) FROM guest_identity_revocations WHERE user_id='${state.user_id}' AND requested_revision>completed_revision) + (SELECT count(*) FROM desktop_connection_sessions WHERE user_id='${state.user_id}' AND (state NOT IN ('revoked','expired') OR closed_at IS NULL)) + (SELECT count(*) FROM gateway_session_tickets t JOIN desktop_connection_sessions c ON c.id=t.connection_id WHERE c.user_id='${state.user_id}' AND t.consumed_at IS NOT NULL AND t.closed_at IS NULL);`);
      assert.equal(pendingCount, '0', 'Await database retirement/revocation receipts before cleanup');
      await services('stop'); await account('nodes-absent'); await account('remove');
      await services('restore');
      if (state.artifacts_installed) await artifacts('restore');
      await configuration('restore');
      state.phase = 'cleaned'; state.password = ''; save('run.json', state);
      console.log('Owned OS account/Home and temporary services removed; configuration restored. Disabled platform identity retained for audit.');
    } else throw new Error('Use prepare/prepare-next, inspect-setup, install-agent, resume-setup, status, desktop, preconnect, checkpoint/connected 1..9, seal-policy, revoke, grant, poll or cleanup');
  } finally {
    await api('/api/v1/auth/logout', 'POST', {});
  }
}
main().catch(e => { console.error(e.status !== undefined ? 'Isolated database observation failed' : e.message); process.exitCode = 1; });

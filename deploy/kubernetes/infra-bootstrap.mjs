// Explicit, private first-install bootstrap for the deployment authorized by the
// operator. Never changes kubectl's current context or uploads the PVE password.
import { execFileSync } from 'node:child_process';
import { randomBytes, X509Certificate } from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { isIP } from 'node:net';
import { pathToFileURL } from 'node:url';

const context = 'kubernetes-admin@infra.homelab';
const namespace = 'vc-workspace';
const registry = 'harbor.infra.plz.ac';
const stateDir = path.resolve('.cache/infra-bootstrap');
const tokenName = 'vc-workspace-infra';
const owned = 'infra-bootstrap-v1';
function check(ok, message) { if (!ok) throw new Error(message); }
export function privateStateSafe(st, uid, directory = false) {
  return (directory ? st.isDirectory() : st.isFile()) && st.uid === uid && (st.mode & 0o777) === (directory ? 0o700 : 0o600);
}
export function peerDocument(fingerprint) {
  check(/^[0-9a-f]{64}$/.test(fingerprint), 'Invalid Gateway leaf fingerprint');
  return {'gw_infra_gateway': [fingerprint]};
}
export function guestTargetConfig(address) {
  check(isIP(address) === 4 && /^10\.31\.0\./.test(address) && Number(address.split('.')[3]) >= 1 && Number(address.split('.')[3]) <= 254,
    'Expected one canonical VM160 IPv4 address in the audited lab subnet');
  const cidr = address + '/32';
  return {data: {'allowed-targets': cidr}, spec: {
    podSelector: {matchLabels: {'app.kubernetes.io/name': 'vc-workspace-session-gateway'}},
    policyTypes: ['Egress'], egress: [{to: [{ipBlock: {cidr}}], ports: [{protocol: 'TCP', port: 3389}]}],
  }};
}
export function verifyGuestTarget(address, vm, interfaces) {
  const config = guestTargetConfig(address);
  check(vm.name === 'vc-vdi-mvp-gvtg-check' && Number(vm.memory) === 4096 &&
    typeof vm.net0 === 'string' && vm.net0.toLowerCase().split(',').includes('virtio=bc:24:11:94:30:d0'), 'VM160 identity mismatch');
  const nic = interfaces.filter(i => i['hardware-address']?.toLowerCase() === 'bc:24:11:94:30:d0');
  check(nic.length === 1 && nic[0]['ip-addresses']?.filter(a => a['ip-address-type'] === 'ipv4').length === 1 &&
    nic[0]['ip-addresses'].some(a => a['ip-address'] === address && a['ip-address-type'] === 'ipv4'), 'VM160 address observation mismatch');
  return config;
}
function kubectl(args, input) {
  try {
    return execFileSync('kubectl', ['--context', context, '-n', namespace, ...args], {
      input, encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe'], timeout: 30000,
    });
  } catch { throw new Error(`Kubernetes operation failed: ${args.slice(0, 3).join(' ')}`); }
}
function object(kind, name) {
  const raw = kubectl(['get', kind, name, '--ignore-not-found', '-o', 'json']);
  return raw ? JSON.parse(raw) : null;
}
function put(kind, name, fields, apiVersion = 'v1', labels = {}) {
  const previous = object(kind, name);
  check(!previous || previous.metadata.labels?.['vc-workspace.io/bootstrap'] === owned,
    `Refusing to overwrite unowned ${kind} ${name}`);
  const doc = { apiVersion, kind, metadata: { name, namespace,
    labels: { ...labels, 'vc-workspace.io/bootstrap': owned } }, ...fields };
  // Server-side apply does not duplicate credentials in last-applied annotations.
  kubectl(['apply', '--server-side', '--field-manager=vc-workspace-bootstrap', '-f', '-'], JSON.stringify(doc));
}
function privateFile(name, make) {
  const target = path.join(stateDir, name);
  if (!fs.existsSync(target)) fs.writeFileSync(target, JSON.stringify(make()), {mode: 0o600, flag: 'wx'});
  const st = fs.lstatSync(target);
  check(privateStateSafe(st, process.getuid()),
    `Unsafe private state permissions: ${target}`);
  return JSON.parse(fs.readFileSync(target, 'utf8'));
}
async function main() {
  check(process.env.VC_WORKSPACE_DEPLOY_INFRA === 'true', 'Set VC_WORKSPACE_DEPLOY_INFRA=true explicitly');
  const ns = object('namespace', namespace);
  check(ns?.metadata.labels?.['app.kubernetes.io/part-of'] === namespace, 'Namespace ownership mismatch');
  fs.mkdirSync(stateDir, { recursive: true, mode: 0o700 });
  const dir = fs.lstatSync(stateDir);
  check(privateStateSafe(dir, process.getuid(), true) && fs.realpathSync(stateDir) === stateDir,
    'State directory must be canonical, owned and mode 0700');
  const local = privateFile('admin.json', () => ({ server: 'https://ws.infra.plz.ac', username: 'admin',
    password: randomBytes(24).toString('base64url'), setup_token: randomBytes(32).toString('base64url'),
    database_password: randomBytes(32).toString('hex'), internal_token: randomBytes(32).toString('base64url') }));

  // This account can read infrastructure metadata, but can only run Guest Agent
  // operations on the isolated VM160. No power/configuration/clone privileges.
  const credentialFile = process.env.VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE;
  check(!!credentialFile, 'VC_WORKSPACE_LIVE_PVE_CREDENTIAL_FILE unset');
  const fields = fs.readFileSync(credentialFile, 'utf8').trim().split(/\s+/);
  check(fields.length === 3 && fields[1] === '/', 'Unexpected explicit PVE credential file format');
  let ticket;
  async function pve(route, method = 'GET', data) {
    const headers = {};
    if (ticket) { headers.Cookie = `PVEAuthCookie=${ticket.ticket}`; headers.CSRFPreventionToken = ticket.CSRFPreventionToken; }
    const response = await fetch(`https://pve.infra.plz.ac/api2/json${route}`, {
      method, headers, body: data ? new URLSearchParams(data) : undefined, signal: AbortSignal.timeout(20000),
    });
    check(response.ok, `PVE ${method} ${route} returned ${response.status}; inspect before retrying mutations`);
    return (await response.json()).data;
  }
  ticket = await pve('/access/ticket', 'POST', {username: fields[0], password: fields[2]});
  const vm = await pve('/nodes/infra-node1/qemu/160/config');
  check(vm.name === 'vc-vdi-mvp-gvtg-check' && Number(vm.memory) === 4096, 'VM160 ownership mismatch');
  const roles = await pve('/access/roles');
  for (const [roleid, privs] of [
    ['VCWorkspaceInfraRead', 'Sys.Audit,Datastore.Audit'],
    ['VCWorkspaceInfraGuest', 'VM.Audit,VM.GuestAgent.Audit,VM.GuestAgent.Unrestricted'],
  ]) {
    const existing = roles.find(r => r.roleid === roleid);
    if (existing) check(existing.privs.split(',').sort().join(',') === privs.split(',').sort().join(','), 'Existing PVE role differs');
    else await pve('/access/roles', 'POST', {roleid, privs});
  }
  const tokenFile = path.join(stateDir, 'pve-token.json');
  if (!fs.existsSync(tokenFile)) {
    const tokens = await pve(`/access/users/${encodeURIComponent(fields[0])}/token`);
    check(!tokens.some(t => t.tokenid === tokenName), 'PVE token already exists without private state; do not regenerate blindly');
    const result = await pve(`/access/users/${encodeURIComponent(fields[0])}/token/${tokenName}`, 'POST', {
      privsep: '1', expire: String(Math.floor(Date.now() / 1000) + 90 * 86400),
      comment: 'VC Workspace infra deployment; VM160 Guest only',
    });
    privateFile('pve-token.json', () => ({id: result['full-tokenid'], secret: result.value}));
  }
  const token = privateFile('pve-token.json', () => { throw new Error('PVE token missing'); });
  check(token.id === `${fields[0]}!${tokenName}` && token.secret, 'PVE token state mismatch');
  for (const [aclPath, role] of [['/', 'VCWorkspaceInfraRead'], ['/vms/160', 'VCWorkspaceInfraGuest']]) {
    await pve('/access/acl', 'PUT', {path: aclPath, tokens: token.id, roles: role, propagate: '1'});
  }
  // Check resulting token permissions independently, including the business VM.
  for (const [vmid, allowed] of [[160, true], [158, false]]) {
    const r = await fetch(`https://pve.infra.plz.ac/api2/json/nodes/${vmid === 160 ? 'infra-node1' : 'infra-node6'}/qemu/${vmid}/config`, {
      headers: {Authorization: `PVEAPIToken=${token.id}=${token.secret}`}, signal: AbortSignal.timeout(10000),
    });
    check(allowed ? r.ok : r.status === 403, `Scoped token VM${vmid} permission check failed (${r.status})`);
  }
  put('Secret', 'vc-workspace-postgres', {stringData: {POSTGRES_PASSWORD: local.database_password}});
  put('Secret', 'vc-workspace-control-plane', {stringData: {
    VC_WORKSPACE_DATABASE_URL: `postgres://vc_workspace:${local.database_password}@vc-workspace-postgres:5432/vc_workspace?sslmode=disable`,
    VC_WORKSPACE_SETUP_TOKEN: local.setup_token, VC_WORKSPACE_INTERNAL_API_TOKEN: local.internal_token,
    VC_WORKSPACE_PVE_TOKEN_ID: token.id, VC_WORKSPACE_PVE_TOKEN_SECRET: token.secret,
  }});
  put('Secret', 'vc-workspace-mcp', {stringData: {VC_WORKSPACE_INTERNAL_API_TOKEN: local.internal_token}});

  const registrySecret = object('secret', 'vc-workspace-registry');
  check(!registrySecret || registrySecret.metadata.labels?.['vc-workspace.io/bootstrap'] === owned, 'Unowned image pull Secret');
  if (!registrySecret) {
    const robotPath = path.join(stateDir, 'registry-robot.json');
    let robot;
    if (fs.existsSync(robotPath)) {
      robot = privateFile('registry-robot.json', () => { throw new Error('Robot state missing'); });
    } else {
    const credential = JSON.parse(execFileSync('docker-credential-osxkeychain', ['get'], {
      input: registry + '\n', encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe'], timeout: 20000,
    }));
    const auth = 'Basic ' + Buffer.from(`${credential.Username}:${credential.Secret}`).toString('base64');
    const response = await fetch(`https://${registry}/api/v2.0/robots`, {method: 'POST',
      headers: {Authorization: auth, 'Content-Type': 'application/json'}, signal: AbortSignal.timeout(20000),
      body: JSON.stringify({name: 'kubernetes-pull', description: 'VC Workspace namespace image pulls', duration: 90, level: 'project',
        permissions: [{kind: 'project', namespace, access: [{resource: 'repository', action: 'pull'}]}]}),
    });
    check(response.status === 201, `Harbor pull robot creation failed (${response.status}); inspect before retrying`);
    robot = await response.json();
    // Persist the result before Kubernetes writes so a failed Secret creation is recoverable.
    privateFile('registry-robot.json', () => robot);
    }
    check(robot.name === 'robot$vc-workspace+kubernetes-pull' && robot.secret, 'Unexpected project pull robot identity');
    put('Secret', 'vc-workspace-registry', {type: 'kubernetes.io/dockerconfigjson', stringData: {
      '.dockerconfigjson': JSON.stringify({auths: {[registry]: {auth: Buffer.from(`${robot.name}:${robot.secret}`).toString('base64')}}}),
    }});
  }
  const client = object('secret', 'vc-workspace-gateway-client');
  check(client?.data?.['tls.crt'] && client.data['ca.crt'], 'Wait for internal cert-manager certificates');
  const leaf = new X509Certificate(Buffer.from(client.data['tls.crt'], 'base64'));
  const fingerprint = leaf.fingerprint256.replaceAll(':', '').toLowerCase();
  const peerJSON = JSON.stringify(peerDocument(fingerprint));
  const previousPeers = object('configmap', 'vc-workspace-gateway-peers');
  check(!previousPeers || previousPeers.data?.['peers.json'] === peerJSON,
    'Gateway certificate changed: perform explicit coordinated peer enrollment and rollout, not bootstrap');
  put('ConfigMap', 'vc-workspace-gateway-peers', {data: {'peers.json': peerJSON}});
  const active = object('secret', 'vc-workspace-gateway-active-client');
  check(!active || (active.metadata.labels?.['vc-workspace.io/certificate-role'] === 'active-gateway-client' &&
    ['tls.crt', 'tls.key', 'ca.crt'].every(key => active.data?.[key] === client.data[key])),
    'Active Gateway identity differs; use the explicit certificate promotion workflow');
  put('Secret', 'vc-workspace-gateway-active-client', {type: 'kubernetes.io/tls', data: client.data}, 'v1',
    {'vc-workspace.io/certificate-role': 'active-gateway-client'});
  put('Secret', 'vc-workspace-upstream-ca', {data: {'ca.crt': client.data['ca.crt']}});
  const interfaces = (await pve('/nodes/infra-node1/qemu/160/agent/network-get-interfaces')).result;
  const target = verifyGuestTarget(process.env.VC_WORKSPACE_INFRA_GUEST_IP ?? '10.31.0.166', vm, interfaces);
  put('ConfigMap', 'vc-workspace-targets', {data: target.data});
  put('NetworkPolicy', 'gateway-guest', {spec: target.spec}, 'networking.k8s.io/v1');
  console.log(`Bootstrap prepared; private administrator state: ${path.join(stateDir, 'admin.json')}`);
  console.log('PVE token independently verified: VM160 allowed, VM158 denied. No Guest mutation was executed.');
}
if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  main().catch(error => { console.error(error.message); process.exitCode = 1; });
}

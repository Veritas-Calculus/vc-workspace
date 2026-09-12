// Scoped, resumable leaf promotion for the explicitly authorized infra lab.
// Issuance and activation are separate: cert-manager never writes the active
// client Secret. No PVE/Guest access, root-CA rotation, or automatic rollback.
import assert from 'node:assert/strict';
import {execFileSync, spawn} from 'node:child_process';
import {createHash, createPrivateKey, randomUUID, X509Certificate} from 'node:crypto';
import fs from 'node:fs';
import https from 'node:https';
import path from 'node:path';
import {pathToFileURL} from 'node:url';
import {privateStateSafe} from './infra-bootstrap.mjs';

const namespace = 'vc-workspace';
const context = 'kubernetes-admin@infra.homelab';
const identity = 'gw_infra_gateway';
const candidateName = 'vc-workspace-gateway-client';
export const activeName = 'vc-workspace-gateway-active-client';
const peersName = 'vc-workspace-gateway-peers';
const ownership = 'infra-bootstrap-v1';
const stateDir = path.resolve('.cache/infra-bootstrap');
const journalPath = path.join(stateDir, 'certificate-rotation.json');
const sha = value => createHash('sha256').update(value).digest('hex');
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
let stopping = false;

export function material(secret, minimumValidityMS = 0) {
  assert.ok(Number.isFinite(minimumValidityMS) && minimumValidityMS >= 0 && minimumValidityMS <= 86400000);
  assert.equal(secret?.type, 'kubernetes.io/tls');
  assert.deepEqual(Object.keys(secret.data ?? {}).sort(), ['ca.crt', 'tls.crt', 'tls.key']);
  const decode = name => {
    const text = secret.data[name];
    assert.ok(typeof text === 'string' && text.length <= 128*1024);
    const result = Buffer.from(text, 'base64');
    assert.equal(result.toString('base64'), text);
    return result;
  };
  const cert = decode('tls.crt'), key = decode('tls.key'), ca = decode('ca.crt');
  const leaf = new X509Certificate(cert), root = new X509Certificate(ca);
  assert.equal(leaf.ca, false); assert.equal(root.ca, true);
  assert.equal(leaf.subject, 'CN=infra-gateway');
  assert.deepEqual(leaf.keyUsage, ['1.3.6.1.5.5.7.3.2']);
  assert.ok(leaf.checkPrivateKey(createPrivateKey(key)) && leaf.verify(root.publicKey));
  assert.ok(root.verify(root.publicKey));
  const now = Date.now();
  for (const certificate of [leaf, root]) {
    assert.ok(Date.parse(certificate.validFrom) <= now && Date.parse(certificate.validTo) > now + minimumValidityMS);
  }
  return {cert, key, ca, fingerprint: sha(leaf.raw), caFingerprint: sha(root.raw), pemSHA256: sha(cert), expires: leaf.validTo};
}

export function peerPins(resource) {
  assert.equal(resource?.metadata?.labels?.['vc-workspace.io/bootstrap'], ownership);
  assert.deepEqual(Object.keys(resource.data ?? {}), ['peers.json']);
  const raw = resource.data['peers.json'];
  assert.ok(raw.length <= 1024);
  const parsed = JSON.parse(raw);
  assert.deepEqual(Object.keys(parsed), [identity]);
  const pins = parsed[identity];
  assert.ok(Array.isArray(pins) && pins.length >= 1 && pins.length <= 2 && new Set(pins).size === pins.length);
  for (const pin of pins) assert.match(pin, /^[0-9a-f]{64}$/);
  // Reject duplicate object keys and alternate unexpected JSON structures.
  assert.equal(raw, JSON.stringify(parsed));
  return pins;
}

export function rotationState(active, pins, oldPin, newPin) {
  assert.notEqual(oldPin, newPin);
  assert.ok([oldPin, newPin].includes(active));
  assert.ok(pins.every(pin => [oldPin, newPin].includes(pin)));
  if (active === oldPin && pins.length === 1 && pins[0] === oldPin) return 'prepared';
  if (pins.length === 2 && pins.includes(oldPin) && pins.includes(newPin)) return active === oldPin ? 'enrolled' : 'activated';
  if (active === newPin && pins.length === 1 && pins[0] === newPin) return 'retired';
  throw new Error('Unrecognized rotation state');
}

function kubectl(args, input) {
  try {
    return execFileSync('kubectl', ['--context', context, '-n', namespace, ...args],
      {input, encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe'], timeout: 30000, maxBuffer: 2*1024*1024});
  } catch { throw new Error('Scoped Kubernetes operation failed'); }
}
function get(kind, name) {
  const raw = kubectl(['get', kind, name, '--ignore-not-found', '-o', 'json']);
  return raw ? JSON.parse(raw) : null;
}
function owned(resource, name) {
  assert.equal(resource?.metadata?.name, name);
  assert.equal(resource.metadata.namespace, namespace);
  assert.equal(resource.metadata.labels?.['vc-workspace.io/bootstrap'], ownership);
  assert.ok(resource.metadata.uid && resource.metadata.resourceVersion);
}
function candidate() {
  const secret = get('secret', candidateName);
  assert.equal(secret?.metadata?.annotations?.['cert-manager.io/certificate-name'], candidateName);
  assert.equal(secret.metadata.annotations['cert-manager.io/issuer-name'], 'vc-workspace-internal');
  const certificate = get('certificate', candidateName);
  assert.equal(certificate.spec.secretName, candidateName);
  assert.ok(certificate.status?.conditions?.some(c => c.type === 'Ready' && c.status === 'True' && c.observedGeneration === certificate.metadata.generation));
  material(secret, 86400000); // Never activate a candidate with less than one day remaining.
  return secret;
}
function activeSecret() {
  const secret = get('secret', activeName);
  if (secret) {
    owned(secret, activeName);
    assert.equal(secret.metadata.labels['vc-workspace.io/certificate-role'], 'active-gateway-client');
    material(secret);
  }
  return secret;
}
function cas(resource, data) {
  kubectl(['patch', resource.kind.toLowerCase(), resource.metadata.name, '--type=json', '--patch-file=/dev/stdin'], JSON.stringify([
    {op: 'test', path: '/metadata/uid', value: resource.metadata.uid},
    {op: 'test', path: '/metadata/resourceVersion', value: resource.metadata.resourceVersion},
    {op: 'replace', path: '/data', value: data},
  ]));
}
function pinData(pins) { return {'peers.json': JSON.stringify({[identity]: pins})}; }
function persist(journal) {
  const temporary = journalPath + '.' + randomUUID();
  const fd = fs.openSync(temporary, 'wx', 0o600);
  try { fs.writeFileSync(fd, JSON.stringify(journal)); fs.fsyncSync(fd); } finally { fs.closeSync(fd); }
  fs.renameSync(temporary, journalPath);
}
function readJournal() {
  if (!fs.existsSync(journalPath)) return null;
  assert.ok(privateStateSafe(fs.lstatSync(journalPath), process.getuid()));
  const journal = JSON.parse(fs.readFileSync(journalPath, 'utf8'));
  assert.equal(journal.version, 1); assert.equal(journal.namespace, namespace);
  return journal;
}

function pod(name, activeRequired) {
  const deployment = get('deployment', name);
  assert.equal(deployment?.spec.replicas, 1);
  assert.equal(deployment.status.observedGeneration, deployment.metadata.generation);
  assert.equal(deployment.status.readyReplicas, 1);
  assert.equal(deployment.status.updatedReplicas, 1);
  assert.equal(deployment.spec.template.spec.automountServiceAccountToken, false);
  if (activeRequired) assert.equal(deployment.spec.template.spec.volumes.find(v => v.name === 'client-tls')?.secret?.secretName, activeName);
  const listed = JSON.parse(kubectl(['get', 'pods', '-l', 'app.kubernetes.io/name=' + name, '-o', 'json'])).items;
  assert.equal(listed.length, 1);
  const current = listed[0];
  assert.ok(!current.metadata.deletionTimestamp && current.status.phase === 'Running');
  assert.ok(current.status.containerStatuses?.every(c => c.ready && c.state.running));
  return {name: current.metadata.name, uid: current.metadata.uid, restarts: current.status.containerStatuses.map(c => c.restartCount)};
}
function samePod(previous, deployment, activeRequired) {
  assert.deepEqual(pod(deployment, activeRequired), previous);
}

// The loopback port is allocated by kubectl; TLS still verifies the real service
// DNS identity and the specific internal CA. No trust is added to the Mac.
async function forward(podName, remotePort) {
  const child = spawn('kubectl', ['--context', context, '-n', namespace, 'port-forward', '--address=127.0.0.1', 'pod/' + podName, ':' + remotePort],
    {stdio: ['ignore', 'pipe', 'pipe']});
  const ended = new Promise(resolve => child.once('close', resolve));
  const close = async () => {
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
    await Promise.race([ended, sleep(3000)]);
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
    await ended;
  };
  try {
    const port = await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('Port forward deadline')), 10000);
      let output = '';
      child.stdout.on('data', chunk => {
        output = (output + chunk).slice(-4096);
        const match = output.match(/Forwarding from 127\.0\.0\.1:([0-9]+) ->/);
        if (match) { clearTimeout(timer); resolve(Number(match[1])); }
      });
      child.stderr.on('data', () => {});
      child.once('error', () => { clearTimeout(timer); reject(new Error('Port forward failed')); });
      child.once('close', () => { clearTimeout(timer); reject(new Error('Port forward closed')); });
    });
    return {port, close};
  } catch (error) { await close(); throw error; }
}
function agent(m, servername) {
  return new https.Agent({keepAlive: true, maxSockets: 1, minVersion: 'TLSv1.3', rejectUnauthorized: true,
    servername, ca: m.ca, cert: m.cert, key: m.key});
}
function ping(port, tlsAgent, control = true) {
  return new Promise((resolve, reject) => {
    const request = https.get({hostname: '127.0.0.1', port, path: control ? '/internal/gateway/v1/ready' : '/ready', agent: tlsAgent,
      headers: {Host: control ? 'vc-workspace-control-plane.vc-workspace.svc' : 'ws.infra.plz.ac'}, timeout: 4000}, response => {
      const rawCertificate = response.socket.getPeerCertificate()?.raw;
      const verified = response.socket.authorized && response.socket.getProtocol() === 'TLSv1.3' && Buffer.isBuffer(rawCertificate);
      const fingerprint = verified ? sha(rawCertificate) : '';
      response.resume();
      response.once('end', () => verified ? resolve({status: response.statusCode, reused: request.reusedSocket, fingerprint}) : reject(new Error('TLS verification failed')));
    });
    request.once('error', () => reject(new Error('Verified readiness request failed')));
    request.once('timeout', () => request.destroy(new Error('Readiness deadline')));
  });
}
async function waitFor(label, check) {
  const deadline = Date.now() + 180000;
  let reported = Date.now();
  while (Date.now() < deadline) {
    assert.equal(stopping, false, 'Operator cancellation');
    if (await check()) return;
    if (Date.now() - reported >= 20000) { console.log('Waiting for mounted configuration: ' + label); reported = Date.now(); }
    await sleep(1000);
  }
  throw new Error('Projection verification deadline');
}

async function main() {
  const mode = process.argv[2];
  assert.ok(['status', 'verify', 'initialize', 'promote'].includes(mode));
  assert.equal(process.env.VC_WORKSPACE_DEPLOY_INFRA, 'true');
  assert.equal(get('namespace', namespace)?.metadata.labels?.['app.kubernetes.io/part-of'], namespace);
  const issued = candidate(), next = material(issued), active = activeSecret(), peers = get('configmap', peersName);
  const pins = peerPins(peers);
  if (mode === 'status') {
    const current = active ? material(active) : null;
    console.log(JSON.stringify({candidate: next.fingerprint, candidateExpires: next.expires,
      active: current?.fingerprint ?? null, activeExpires: current?.expires ?? null,
      promotionPending: current !== null && current.fingerprint !== next.fingerprint, enrolled: pins}, null, 2));
    return;
  }
  if (mode === 'verify') {
    assert.ok(active);
    const activeMaterial = material(active);
    assert.ok(pins.includes(activeMaterial.fingerprint));
    for (const [deployment, certificate, port, servername, control] of [
      ['vc-workspace-control-plane', 'vc-workspace-control-tls', 8444, 'vc-workspace-control-plane.vc-workspace.svc', true],
      ['vc-workspace-session-gateway', 'vc-workspace-gateway-tls', 8443, 'ws.infra.plz.ac', false],
    ]) {
      const observed = pod(deployment, !control);
      const secret = get('secret', certificate);
      assert.equal(secret?.metadata?.annotations?.['cert-manager.io/certificate-name'], certificate);
      const expected = sha(new X509Certificate(Buffer.from(secret.data['tls.crt'], 'base64')).raw);
      const local = await forward(observed.name, port);
      try {
        await waitFor(certificate, async () => {
          // A new handshake is required to observe the current server leaf.
          const probe = agent(activeMaterial, servername);
          try { const result = await ping(local.port, probe, control); return result.status === 204 && result.fingerprint === expected; }
          finally { probe.destroy(); }
        });
        samePod(observed, deployment, !control);
        const current = get('secret', certificate);
        assert.equal(current.metadata.uid, secret.metadata.uid);
        assert.equal(sha(new X509Certificate(Buffer.from(current.data['tls.crt'], 'base64')).raw), expected);
        console.log(`${certificate}: trusted TLS 1.3, active identity, current Secret leaf ${expected}; Pod unchanged`);
      } finally { await local.close(); }
    }
    return;
  }
  assert.ok(privateStateSafe(fs.lstatSync(stateDir), process.getuid(), true) && fs.realpathSync(stateDir) === stateDir);
  const lock = path.join(stateDir, 'certificate-rotation.lock');
  fs.mkdirSync(lock, {mode: 0o700}); // Existing/stale lock requires independent operator inspection.
  const ownerPath = path.join(lock, 'owner.json');
  fs.writeFileSync(ownerPath, JSON.stringify({pid: process.pid, operation: mode, at: new Date().toISOString()}), {flag: 'wx', mode: 0o600});
  try {
    if (mode === 'initialize') {
      assert.deepEqual(pins, [next.fingerprint]);
      if (active) { assert.deepEqual(active.data, issued.data); console.log('Active client Secret already initialized'); return; }
      kubectl(['create', '-f', '-'], JSON.stringify({apiVersion: 'v1', kind: 'Secret', type: 'kubernetes.io/tls',
        metadata: {name: activeName, namespace, labels: {'vc-workspace.io/bootstrap': ownership, 'vc-workspace.io/certificate-role': 'active-gateway-client'}}, data: issued.data}));
      assert.deepEqual(activeSecret().data, issued.data);
      console.log('Created active client Secret with the already enrolled identity; no deployment restarted');
      return;
    }
    assert.ok(active);
    const controlPod = pod('vc-workspace-control-plane', false), gatewayPod = pod('vc-workspace-session-gateway', true);
    let journal = readJournal();
    if (material(active).fingerprint === next.fingerprint && pins.length === 1 && pins[0] === next.fingerprint &&
      (!journal || journal.phase === 'complete')) {
      console.log('Issued certificate is already active and exclusively enrolled; nothing to promote');
      return;
    }
    if (journal?.phase === 'complete') {
      fs.renameSync(journalPath, path.join(stateDir, `certificate-complete-${journal.id}.json`)); journal = null;
    }
    if (!journal) {
      const old = material(active);
      assert.equal(old.caFingerprint, next.caFingerprint, 'This operation does not rotate root CAs');
      assert.deepEqual(pins, [old.fingerprint]); assert.notEqual(old.fingerprint, next.fingerprint);
      journal = {version: 1, namespace, id: randomUUID(), phase: 'prepared', started: new Date().toISOString(),
        activeUID: active.metadata.uid, peersUID: peers.metadata.uid, candidateUID: issued.metadata.uid,
        old: active.data, next: issued.data, oldPin: old.fingerprint, newPin: next.fingerprint};
      persist(journal);
    }
    assert.equal(journal.activeUID, active.metadata.uid); assert.equal(journal.peersUID, peers.metadata.uid);
    assert.equal(journal.candidateUID, issued.metadata.uid); assert.deepEqual(journal.next, issued.data);
    const old = material({type: 'kubernetes.io/tls', data: journal.old});
    assert.equal(old.fingerprint, journal.oldPin); assert.equal(next.fingerprint, journal.newPin);
    assert.equal(old.caFingerprint, next.caFingerprint);
    rotationState(material(active).fingerprint, pins, old.fingerprint, next.fingerprint);
    const cp = await forward(controlPod.name, 8444);
    let gw;
    const oldAgent = agent(old, 'vc-workspace-control-plane.vc-workspace.svc');
    const nextAgent = agent(next, 'vc-workspace-control-plane.vc-workspace.svc');
    const gatewayAgent = new https.Agent({keepAlive: true, minVersion: 'TLSv1.3', rejectUnauthorized: true, ca: next.ca, servername: 'ws.infra.plz.ac'});
    try {
      gw = await forward(gatewayPod.name, 8443);
      let currentPeers = get('configmap', peersName);
      assert.equal(currentPeers.metadata.uid, journal.peersUID);
      const state = rotationState(material(activeSecret()).fingerprint, peerPins(currentPeers), old.fingerprint, next.fingerprint);
      if (state === 'prepared') {
        assert.equal(stopping, false);
        assert.equal((await ping(cp.port, oldAgent)).status, 204);
        cas(currentPeers, pinData([old.fingerprint, next.fingerprint]));
      }
      if (state !== 'retired') {
        console.log('Verifying staged fingerprint on the running control-plane Pod');
        await waitFor('dual enrollment', async () => {
          const a = await ping(cp.port, oldAgent), b = await ping(cp.port, nextAgent);
          return a.status === 204 && b.status === 204;
        });
        samePod(controlPod, 'vc-workspace-control-plane', false);
        journal.phase = 'enrolled'; persist(journal);
        assert.equal(stopping, false);
        const current = activeSecret();
        assert.equal(current.metadata.uid, journal.activeUID);
        if (material(current).fingerprint === old.fingerprint) cas(current, journal.next);
        journal.phase = 'activating'; persist(journal);
        console.log('Waiting for active Secret projection and Gateway readiness');
        await waitFor('active client certificate', async () => {
          // Preserve an authenticated old connection across the projection
          // delay so retirement tests revocation, not just a new handshake.
          assert.equal((await ping(cp.port, oldAgent)).status, 204);
          const sum = kubectl(['exec', gatewayPod.name, '--', 'sha256sum', '/tls/client/tls.crt']).trim().split(/\s+/)[0];
          return sum === next.pemSHA256 && (await ping(gw.port, gatewayAgent, false)).status === 204;
        });
        samePod(gatewayPod, 'vc-workspace-session-gateway', true);
        journal.phase = 'activated'; persist(journal);
        currentPeers = get('configmap', peersName);
        assert.equal(stopping, false);
        assert.equal(rotationState(material(activeSecret()).fingerprint, peerPins(currentPeers), old.fingerprint, next.fingerprint), 'activated');
        assert.equal(currentPeers.metadata.uid, journal.peersUID);
        cas(currentPeers, pinData([next.fingerprint]));
      }
      console.log('Verifying withdrawn identity rejection and live Gateway readiness');
      let reuseObserved = false;
      await waitFor('old fingerprint retirement', async () => {
        const oldResult = await ping(cp.port, oldAgent);
        reuseObserved ||= oldResult.status === 403 && oldResult.reused;
        return oldResult.status === 403 && (await ping(cp.port, nextAgent)).status === 204 &&
          (await ping(gw.port, gatewayAgent, false)).status === 204;
      });
      // A resumed post-retirement operation may not own a pre-retirement socket.
      assert.ok(state === 'retired' || reuseObserved, 'Old keep-alive rejection not observed');
      samePod(controlPod, 'vc-workspace-control-plane', false); samePod(gatewayPod, 'vc-workspace-session-gateway', true);
      assert.equal(rotationState(material(activeSecret()).fingerprint, peerPins(get('configmap', peersName)), old.fingerprint, next.fingerprint), 'retired');
      journal.phase = 'complete'; journal.completed = new Date().toISOString(); journal.oldKeepAliveRejected = reuseObserved;
      journal.controlPod = controlPod; journal.gatewayPod = gatewayPod;
      delete journal.old; delete journal.next; persist(journal);
      console.log('Certificate promotion complete: old identity rejected, new identity and Gateway ready; both Pods unchanged');
    } finally {
      oldAgent.destroy(); nextAgent.destroy(); gatewayAgent.destroy();
      if (gw) await gw.close(); await cp.close();
    }
  } finally { fs.unlinkSync(ownerPath); fs.rmdirSync(lock); }
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  process.once('SIGINT', () => { stopping = true; });
  process.once('SIGTERM', () => { stopping = true; });
  main().catch(() => { console.error('Certificate operation stopped. Inspect scoped resources and the private journal before resuming; no automatic rollback was performed.'); process.exitCode = 1; });
}

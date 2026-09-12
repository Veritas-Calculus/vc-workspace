import assert from 'node:assert/strict';
import {execFileSync} from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import {material, peerPins, rotationState, activeName} from './infra-certificates.mjs';

const old = 'a'.repeat(64), next = 'b'.repeat(64), other = 'c'.repeat(64);
const peers = pins => ({metadata: {labels: {'vc-workspace.io/bootstrap': 'infra-bootstrap-v1'}},
  data: {'peers.json': JSON.stringify({gw_infra_gateway: pins})}});
test('rotation state accepts only ordered known identities and resumes acknowledged writes', () => {
  assert.equal(rotationState(old, [old], old, next), 'prepared');
  assert.equal(rotationState(old, [next, old], old, next), 'enrolled');
  assert.equal(rotationState(next, [old, next], old, next), 'activated');
  assert.equal(rotationState(next, [next], old, next), 'retired');
  for (const [active, pins] of [[old, [next]], [next, [old]], [other, [old, next]], [old, [old, other]], [old, []]]) {
    assert.throws(() => rotationState(active, pins, old, next));
  }
});
test('peer enrollment rejects unowned, duplicate, unexpected and malformed identities', () => {
  assert.deepEqual(peerPins(peers([old, next])), [old, next]);
  for (const pins of [[], [old, old], [old, next, other], ['A'.repeat(64)], ['b'.repeat(63)]]) assert.throws(() => peerPins(peers(pins)));
  const bad = peers([old]); bad.metadata.labels = {}; assert.throws(() => peerPins(bad));
  const duplicate = peers([old]); duplicate.data['peers.json'] = `{"gw_infra_gateway":["${next}"],"gw_infra_gateway":["${old}"]}`;
  assert.throws(() => peerPins(duplicate));
});
test('issued material validates actual CA signature, key, identity, EKU and expiry', t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'vc-workspace-certificate-test-'));
  t.after(() => fs.rmSync(root, {recursive: true})); // Exact test-owned temp root.
  const run = args => execFileSync('openssl', args, {cwd: root, stdio: ['ignore', 'pipe', 'pipe'], timeout: 10000});
  run(['req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes', '-days', '3',
    '-subj', '/CN=isolated-test-ca', '-addext', 'basicConstraints=critical,CA:TRUE', '-keyout', 'ca.key', '-out', 'ca.crt']);
  run(['req', '-new', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256', '-nodes', '-subj', '/CN=infra-gateway',
    '-keyout', 'client.key', '-out', 'client.csr']);
  fs.writeFileSync(path.join(root, 'extensions'), 'basicConstraints=CA:FALSE\nextendedKeyUsage=clientAuth\n', {mode: 0o600});
  run(['x509', '-req', '-in', 'client.csr', '-CA', 'ca.crt', '-CAkey', 'ca.key', '-CAcreateserial', '-days', '2',
    '-extfile', 'extensions', '-out', 'client.crt']);
  const read = file => fs.readFileSync(path.join(root, file)).toString('base64');
  const secret = {type: 'kubernetes.io/tls', data: {'tls.crt': read('client.crt'), 'tls.key': read('client.key'), 'ca.crt': read('ca.crt')}};
  assert.match(material(secret).fingerprint, /^[0-9a-f]{64}$/);
  for (const data of [
    {...secret.data, 'tls.key': read('ca.key')}, {...secret.data, 'ca.crt': read('client.crt')},
    {...secret.data, 'tls.crt': 'invalid'}, {...secret.data, 'tls.crt': secret.data['tls.crt'] + '\n'},
    {...secret.data, extra: 'unexpected'},
  ]) assert.throws(() => material({...secret, data}));
  fs.writeFileSync(path.join(root, 'extensions'), 'basicConstraints=CA:FALSE\nextendedKeyUsage=serverAuth\n', {mode: 0o600});
  run(['x509', '-req', '-in', 'client.csr', '-CA', 'ca.crt', '-CAkey', 'ca.key', '-CAcreateserial', '-days', '2', '-extfile', 'extensions', '-out', 'server.crt']);
  assert.throws(() => material({...secret, data: {...secret.data, 'tls.crt': read('server.crt')}}));
  fs.writeFileSync(path.join(root, 'extensions'), 'basicConstraints=CA:FALSE\nextendedKeyUsage=clientAuth\n', {mode: 0o600});
  run(['x509', '-req', '-in', 'client.csr', '-CA', 'ca.crt', '-CAkey', 'ca.key', '-CAcreateserial', '-days', '0', '-extfile', 'extensions', '-out', 'expired.crt']);
  assert.throws(() => material({...secret, data: {...secret.data, 'tls.crt': read('expired.crt')}}));
});
test('runtime projects the active Secret separately from cert-manager issuance', () => {
  const source = fs.readFileSync('deploy/kubernetes/overlays/infra/gateway.yaml', 'utf8');
  assert.ok(source.includes('secretName: ' + activeName));
  assert.ok(!source.includes('secretName: vc-workspace-gateway-client,'));
  const operation = fs.readFileSync('deploy/kubernetes/infra-certificates.mjs', 'utf8');
  assert.ok(!operation.includes('rejectUnauthorized: false'));
  assert.ok(!operation.includes('rollout'));
  assert.ok(operation.includes("path: '/metadata/resourceVersion'"));
  assert.ok(operation.includes('delete journal.old; delete journal.next'));
});

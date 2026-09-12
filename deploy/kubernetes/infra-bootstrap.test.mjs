import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import test from 'node:test';
import { peerDocument, privateStateSafe, guestTargetConfig, verifyGuestTarget } from './infra-bootstrap.mjs';

test('Guest DHCP maintenance pins one audited VM/MAC and one explicit /32', () => {
  const vm = {name: 'vc-vdi-mvp-gvtg-check', memory: 4096, net0: 'virtio=BC:24:11:94:30:D0,bridge=vmbr0'};
  const nic = {'hardware-address': 'bc:24:11:94:30:d0', 'ip-addresses': [{'ip-address-type': 'ipv4', 'ip-address': '10.31.0.167'}]};
  const target = verifyGuestTarget('10.31.0.167', vm, [nic]);
  assert.deepEqual(target.data, {'allowed-targets': '10.31.0.167/32'});
  assert.deepEqual(target.spec.egress, [{to: [{ipBlock: {cidr: '10.31.0.167/32'}}], ports: [{protocol: 'TCP', port: 3389}]}]);
  for (const address of ['', '10.31.0.167/32', '10.31.0.0/24', '10.31.0.0', '10.31.0.255', '10.31.0.0167', '10.31.1.167', '127.0.0.1', 'guest.example']) {
    assert.throws(() => guestTargetConfig(address));
  }
  for (const change of [{name: 'business'}, {memory: 8192}, {net0: 'virtio=bc:24:11:94:30:d1'}, {net0: 'notvirtio=bc:24:11:94:30:d0'}]) {
    assert.throws(() => verifyGuestTarget('10.31.0.167', {...vm, ...change}, [nic]));
  }
  assert.throws(() => verifyGuestTarget('10.31.0.166', vm, [nic]));
  assert.throws(() => verifyGuestTarget('10.31.0.167', vm, [nic, nic]));
  assert.throws(() => verifyGuestTarget('10.31.0.167', vm, [{...nic, 'ip-addresses': [...nic['ip-addresses'], {'ip-address-type': 'ipv4', 'ip-address': '10.31.0.168'}]}]));
});

test('private state rejects symlinks, foreign owners and permissive modes', () => {
  const file = {isFile: () => true, isDirectory: () => false, uid: 501, mode: 0o100600};
  assert.equal(privateStateSafe(file, 501), true);
  for (const change of [{uid: 0}, {mode: 0o100644}, {mode: 0o100660}, {isFile: () => false}]) {
    assert.equal(privateStateSafe({...file, ...change}, 501), false);
  }
  assert.equal(privateStateSafe({...file, isFile: () => false, isDirectory: () => true, mode: 0o40700}, 501, true), true);
});

test('Gateway enrollment emits a valid bounded identity and lowercase SHA-256', () => {
  const peers = peerDocument('a'.repeat(64));
  assert.match(Object.keys(peers)[0], /^gw_[a-z0-9_-]{8,64}$/);
  for (const bad of ['', 'A'.repeat(64), 'a'.repeat(63), 'a'.repeat(65), 'g'.repeat(64)]) {
    assert.throws(() => peerDocument(bad));
  }
});

test('infra rendered manifests retain transport, isolation and immutable image boundaries', () => {
  const yaml = execFileSync('kubectl', ['kustomize', 'deploy/kubernetes/overlays/infra'], {encoding: 'utf8'});
  const docs = JSON.parse(execFileSync('ruby', ['-ryaml', '-rjson', '-e', 'puts JSON.generate(YAML.load_stream(STDIN.read))'], {input: yaml, encoding: 'utf8'}));
  const find = (kind, name) => docs.find(d => d.kind === kind && d.metadata.name === name);
  for (const d of docs.filter(d => ['Deployment', 'StatefulSet'].includes(d.kind))) {
    assert.equal(d.spec.template.spec.automountServiceAccountToken, false);
    assert.equal(d.spec.template.spec.hostNetwork, undefined);
    for (const c of d.spec.template.spec.containers) {
      assert.match(c.image, /^harbor\.infra\.plz\.ac\/vc-workspace\/[^@]+@sha256:[0-9a-f]{64}$/);
      assert.equal(c.securityContext.allowPrivilegeEscalation, false);
      assert.equal(c.securityContext.readOnlyRootFilesystem, true);
    }
  }
  for (const d of docs.filter(d => d.kind === 'Service')) assert.ok(!d.spec.type || d.spec.type === 'ClusterIP');
  const gateway = find('Ingress', 'vc-workspace-gateway');
  assert.equal(gateway.spec.rules[0].host, 'ws.infra.plz.ac');
  assert.equal(gateway.spec.rules[0].http.paths[0].pathType, 'Exact');
  assert.equal(gateway.metadata.annotations['nginx.ingress.kubernetes.io/proxy-ssl-verify'], 'on');
  assert.equal(gateway.metadata.annotations['nginx.ingress.kubernetes.io/proxy-ssl-protocols'], 'TLSv1.3');
  const relay = find('Deployment', 'vc-workspace-session-gateway').spec.template.spec.containers[0];
  assert.equal(relay.env.find(e => e.name === 'VC_WORKSPACE_GATEWAY_ID').value, Object.keys(peerDocument('a'.repeat(64)))[0]);
  const cp = find('Deployment', 'vc-workspace-control-plane').spec.template.spec.containers[0];
  assert.equal(cp.env.find(e => e.name === 'VC_WORKSPACE_NATIVE_GATEWAY_ID').value, Object.keys(peerDocument('a'.repeat(64)))[0]);
  assert.equal(find('ConfigMap', 'vc-workspace-control-plane').data.VC_WORKSPACE_IMAGE_BUILDER_ENABLED, 'false');
  assert.deepEqual(find('NetworkPolicy', 'default-deny').spec.podSelector, {});
  const egress = find('NetworkPolicy', 'control-plane').spec.egress;
  assert.ok(egress.every(e => e.to?.length > 0));
  assert.ok(!yaml.includes('BEGIN PRIVATE KEY'));
});

test('container build stages execute on the builder architecture', () => {
  for (const app of ['control-plane', 'session-gateway', 'mcp', 'web']) {
    const source = fs.readFileSync(`deploy/container/${app}.Dockerfile`, 'utf8');
    assert.match(source, /FROM --platform=\$BUILDPLATFORM/);
    if (app !== 'web') assert.ok(source.includes('CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH'));
  }
});

test('base, secret and restore templates parse offline with unique resources', () => {
  const base = execFileSync('kubectl', ['kustomize', 'deploy/kubernetes/base'], {encoding: 'utf8'});
  for (const source of [base, ...['secret.example.yaml', 'restore-check.yaml'].map(p => fs.readFileSync(`deploy/kubernetes/${p}`, 'utf8'))]) {
    const docs = JSON.parse(execFileSync('ruby', ['-ryaml', '-rjson', '-e', 'puts JSON.generate(YAML.load_stream(STDIN.read))'], {input: source, encoding: 'utf8'}));
    const ids = new Set();
    for (const doc of docs) {
      assert.ok(doc.apiVersion && doc.kind && doc.metadata?.name);
      const id = `${doc.kind}/${doc.metadata.namespace ?? ''}/${doc.metadata.name}`;
      assert.ok(!ids.has(id)); ids.add(id);
    }
  }
  const restore = fs.readFileSync('deploy/kubernetes/restore-check.yaml', 'utf8');
  assert.ok(!restore.includes('persistentVolumeClaim:') && !restore.includes('hostPath:'));
  assert.ok(restore.includes("listen_addresses="));
  const verify = fs.readFileSync('deploy/kubernetes/verify.sh', 'utf8');
  assert.ok(!verify.split('\n').some(line => /^kubectl (create|apply|get)\b/.test(line)));
});

// Explicit VM160 DHCP address maintenance, never a broad network allowlist.
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {execFileSync} from 'node:child_process';
import {privateStateSafe, guestTargetConfig, verifyGuestTarget} from './infra-bootstrap.mjs';

const root = path.resolve('.cache/infra-bootstrap');
function kubectl(args, input) {
  try {
    return execFileSync('kubectl', ['--context', 'kubernetes-admin@infra.homelab', '-n', 'vc-workspace', ...args],
      {input, encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe'], timeout: 50000});
  } catch { throw new Error('Scoped Kubernetes operation failed; inspect journal and live resources, do not blindly rerun'); }
}
const get = (kind, name) => JSON.parse(kubectl(['get', kind, name, '-o', 'json']));
async function main() {
  assert.equal(process.env.VC_WORKSPACE_DEPLOY_INFRA, 'true');
  const address = process.argv[2];
  guestTargetConfig(address);
  assert.ok(privateStateSafe(fs.lstatSync(root), process.getuid(), true) && fs.realpathSync(root) === root);
  const tokenFile = path.join(root, 'pve-token.json');
  assert.ok(privateStateSafe(fs.lstatSync(tokenFile), process.getuid()));
  const token = JSON.parse(fs.readFileSync(tokenFile, 'utf8'));
  assert.equal(token.id, 'terraform-prov@pve!vc-workspace-infra');
  async function pve(suffix) {
    const response = await fetch('https://pve.infra.plz.ac/api2/json/nodes/infra-node1/qemu/160/' + suffix,
      {redirect: 'error', signal: AbortSignal.timeout(15000), headers: {Authorization: `PVEAPIToken=${token.id}=${token.secret}`}});
    assert.ok(response.ok, 'Read-only VM160 observation failed');
    return (await response.json()).data;
  }
  const vm = await pve('config');
  assert.equal((await pve('status/current')).status, 'running');
  const target = verifyGuestTarget(address, vm, (await pve('agent/network-get-interfaces')).result);
  assert.equal(get('namespace', 'vc-workspace').metadata.labels?.['app.kubernetes.io/part-of'], 'vc-workspace');
  const config = get('configmap', 'vc-workspace-targets');
  const policy = get('networkpolicy', 'gateway-guest');
  for (const object of [config, policy]) assert.equal(object.metadata.labels?.['vc-workspace.io/bootstrap'], 'infra-bootstrap-v1');
  const previousCIDR = config.data['allowed-targets'];
  assert.match(previousCIDR, /^10\.31\.0\.[0-9]+\/32$/);
  const previous = guestTargetConfig(previousCIDR.slice(0, -3));
  assert.deepEqual(config.data, previous.data); assert.deepEqual(policy.spec, previous.spec);
  assert.notEqual(previousCIDR, target.data['allowed-targets'], 'Already configured: inspect rollout, no restart was requested');
  assert.equal(kubectl(['exec', 'vc-workspace-postgres-0', '--', 'psql', '-U', 'vc_workspace', '-d', 'vc_workspace', '-Atc',
    'SELECT count(*) FROM gateway_session_tickets WHERE consumed_at IS NOT NULL AND closed_at IS NULL AND lease_until>now();']).trim(), '0', 'Drain active tunnels before changing the target');
  const journal = {from: previousCIDR, to: target.data['allowed-targets'], config, policy, phase: 'prepared'};
  const journalPath = path.join(root, `target-${Date.now()}.json`);
  const persist = () => fs.writeFileSync(journalPath, JSON.stringify(journal), {mode: 0o600});
  fs.writeFileSync(journalPath, JSON.stringify(journal), {mode: 0o600, flag: 'wx'});
  // Narrow the network first. Any partial failure leaves the stale route denied.
  for (const [kind, name, resource, field, value] of [
    ['networkpolicy', 'gateway-guest', policy, 'spec', target.spec],
    ['configmap', 'vc-workspace-targets', config, 'data', target.data],
  ]) {
    journal.phase = `updating-${kind}`; persist();
    kubectl(['patch', kind, name, '--type=json', '--patch-file=/dev/stdin'], JSON.stringify([
      {op: 'test', path: '/metadata/resourceVersion', value: resource.metadata.resourceVersion},
      {op: 'replace', path: '/' + field, value},
    ]));
  }
  for (const deployment of ['vc-workspace-control-plane', 'vc-workspace-session-gateway']) {
    journal.phase = `restarting-${deployment}`; persist();
    kubectl(['rollout', 'restart', 'deployment/' + deployment]);
    kubectl(['rollout', 'status', 'deployment/' + deployment, '--timeout=45s']);
  }
  journal.phase = 'complete'; persist();
  console.log(`Verified VM160 route moved ${journal.from} → ${journal.to}; both deployments ready. Journal: ${journalPath}`);
}
main().catch(() => { console.error('Target update stopped; inspect private journal and current state before another mutation'); process.exitCode = 1; });

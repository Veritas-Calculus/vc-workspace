// Generates a private custom-format backup. Restore is restricted to the named
// disposable Pod/database in restore-check.yaml, never the source database.
import {execFileSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import {privateStateSafe} from './infra-bootstrap.mjs';

const args = ['--context', 'kubernetes-admin@infra.homelab', '-n', 'vc-workspace'];
function run(command, input) {
  return execFileSync('kubectl', [...args, ...command], {input, stdio: ['pipe', 'pipe', 'pipe'], timeout: 45000, maxBuffer: 64 * 1024 * 1024});
}
try {
  if (process.env.VC_WORKSPACE_DEPLOY_INFRA !== 'true') throw new Error('Explicit infra deployment opt-in required');
  const operation = process.argv[2];
  const directory = path.resolve('.cache/infra-bootstrap');
  if (!privateStateSafe(fs.lstatSync(directory), process.getuid(), true) || fs.realpathSync(directory) !== directory) throw new Error('Unsafe backup directory');
  if (operation === 'backup') {
    const archive = run(['exec', 'vc-workspace-postgres-0', '--', 'pg_dump', '-U', 'vc_workspace', '-d', 'vc_workspace', '-Fc', '--no-owner', '--no-acl']);
    if (archive.subarray(0, 5).toString() !== 'PGDMP') throw new Error('Not a PostgreSQL custom archive');
    const output = path.join(directory, `postgres-${new Date().toISOString().replaceAll(':', '-')}.dump`);
    fs.writeFileSync(output, archive, {mode: 0o600, flag: 'wx'});
    console.log(JSON.stringify({backup: output, bytes: archive.length, sha256: createHash('sha256').update(archive).digest('hex')}));
  } else if (operation === 'restore-check') {
    const source = path.resolve(process.argv[3] ?? '');
    if (path.dirname(source) !== directory || !privateStateSafe(fs.lstatSync(source), process.getuid()) || fs.realpathSync(source) !== source) throw new Error('Unsafe backup path');
    const pod = JSON.parse(run(['get', 'pod', 'vc-workspace-restore-check', '-o', 'json']));
    if (pod.metadata.labels?.['vc-workspace.io/acceptance'] !== 'restore-check' || pod.spec.volumes.some(v => v.persistentVolumeClaim || v.hostPath)) throw new Error('Restore target is not the disposable acceptance Pod');
    const count = run(['exec', 'vc-workspace-restore-check', '--', 'psql', '-h', '/var/run/postgresql', '-U', 'vc_workspace', '-d', 'vc_workspace_restore', '-Atc', "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'"]).toString().trim();
    if (count !== '0') throw new Error('Restore target is not empty; never overwrite a database');
    run(['exec', '-i', 'vc-workspace-restore-check', '--', 'pg_restore', '-h', '/var/run/postgresql', '-U', 'vc_workspace', '-d', 'vc_workspace_restore', '--exit-on-error', '--no-owner', '--no-acl'], fs.readFileSync(source));
    console.log(run(['exec', 'vc-workspace-restore-check', '--', 'psql', '-h', '/var/run/postgresql', '-U', 'vc_workspace', '-d', 'vc_workspace_restore', '-Atc', "SELECT 'migrations='||count(*) FROM schema_migrations; SELECT 'users='||count(*) FROM users; SELECT 'managed_desktops='||count(*) FROM managed_desktops; SELECT 'gateway_tickets='||count(*) FROM gateway_session_tickets;"]).toString().trim());
  } else throw new Error('Use backup or restore-check <private-archive>');
} catch (error) {
  // Subprocess errors can contain PostgreSQL diagnostics: never print archive,
  // connection secrets, SQL values or raw subprocess output.
  console.error(error.status !== undefined ? 'Backup/restore command failed; inspect the isolated Pod' : error.message);
  process.exitCode = 1;
}

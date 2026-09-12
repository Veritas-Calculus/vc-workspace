// Offline smoke test for the disposable RDP acceptance client, not a Windows
// desktop/login test. No host mounts, real credentials or external network.
import assert from "node:assert/strict";
import { randomBytes } from "node:crypto";
import { spawnSync } from "node:child_process";

const image = "vc-workspace-windows-session-test:local";
const fixturePassword = "VcwPublicFixtureOnly1!";
const base = [
  "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL",
  "--security-opt", "no-new-privileges", "--tmpfs", "/tmp:rw,nosuid,nodev",
  "--tmpfs", "/run:rw,nosuid,nodev", "--tmpfs", "/root:rw,nosuid,nodev,mode=700",
];

function docker(args, options = {}) {
  return spawnSync("docker", args, {
    encoding: "utf8", timeout: 20_000, maxBuffer: 65_536, ...options,
  });
}

for (const mode of ["version", "stdin"]) {
  const marker = randomBytes(12).toString("hex");
  const name = `vcw-rdp-client-check-${marker}`;
  assert.notEqual(docker(["inspect", name]).status, 0, "test container collision");
  const input = mode === "stdin" ? [
    "/v:127.0.0.1:9", "/u:fixture", `/p:${fixturePassword}`, "/cert:deny",
    "/log-level:ERROR", "/size:1280x720", "-clipboard",
  ].join("\n") + "\n" : undefined;
  try {
    const result = docker([
      ...base, "--name", name, "--label", `vc-workspace.test-instance=${marker}`,
      ...(input ? ["-i"] : []), image, mode === "version" ? "/version" : "/args-from:stdin",
    ], { input });
    const output = `${result.stdout ?? ""}\n${result.stderr ?? ""}`;
    assert(!output.includes(fixturePassword), "fixture password was echoed");
    assert(!result.error, `RDP client ${mode} failed to complete within its deadline`);
    if (mode === "version") {
      assert.equal(result.status, 0, "RDP client failed to start under Xvfb/tini");
      assert.match(output, /FreeRDP version 3\./);
    } else {
      // No network/server exists here. Both failure codes are emitted by
      // FreeRDP for a closed endpoint, depending on its transport path. Require
      // a connection attempt, not merely any nonzero exit/argument parser error.
      assert.notEqual(result.status, 0);
      assert.match(output, /ERRCONNECT_CONNECT_(?:TRANSPORT_)?FAILED/);
    }
    console.log(`PASS RDP client ${mode}: bounded startup, no credential echo`);
  } finally {
    const remaining = docker([
      "inspect", "--format", '{{index .Config.Labels "vc-workspace.test-instance"}}', name,
    ]);
    if (remaining.status === 0) {
      assert.equal(remaining.stdout.trim(), marker, "refuse cleanup of an unrelated container");
      const stopped = docker(["stop", "-t", "3", name]);
      assert.equal(stopped.status, 0, "failed to stop test container");
    }
    assert.notEqual(docker(["inspect", name]).status, 0, "test container remains");
  }
}

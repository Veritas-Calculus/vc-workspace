"""Native shadow/process/crash contract in a disposable Linux container.

The private Rust test child bypasses the PAM installation prerequisite only in
this container. This is deliberately NOT xrdp login or default Native acceptance.
The shipped command must reject issue/retire while Native PAM is unavailable.
"""
import ctypes
import fcntl
import grp
import json
import os
from pathlib import Path
import pwd
import signal
import stat
import subprocess
import time

BINARY = "/out/vc-workspace-guest-agent"
TEST_BINARY = "/out/vc-workspace-guest-agent-tests"
CHILD = "computer::session::account::native::tests::native_account_contract_child"
USERNAME = "vcw665544332211"
BASE = Path("/var/lib/vc-workspace/computer-v2/users") / USERNAME
SNAPSHOT = BASE / "native-account-lifecycle.json"
SECRETS = ["Vcw1!disposable-native-credential-one", "Vcw2!disposable-native-credential-two"]


def safe_output(result):
    assert all(s not in result.stdout and s not in result.stderr for s in SECRETS)


def cli(operation, payload=None, *, check=True, username=USERNAME):
    args = [BINARY, "computer-v2-" + operation]
    if username:
        args += ["--guest-user", username]
    result = subprocess.run(args, input=json.dumps(payload) if payload else None,
                            capture_output=True, text=True, timeout=25)
    safe_output(result)
    if check:
        assert result.returncode == 0, (operation, result.stderr)
    return result


def observe():
    return json.loads(cli("native-account-inspect").stdout)


def request(identity, operation, revision, *, connection=None, expiry=None, secret=0):
    value = dict(schema_version=1, identity=identity, operation=operation,
                 revision=revision, connection_id=connection or f"conn_native_{revision:08d}",
                 expires_unix_seconds=expiry or int(time.time()) + 240)
    if operation == "issue":
        value["password"] = SECRETS[secret]
    elif operation == "revoke":
        value["expires_unix_seconds"] = 0
    return value


def apply(value, *, checkpoint=0, check=True):
    result = subprocess.run([TEST_BINARY, "--ignored", "--exact", CHILD, "--nocapture"],
        env={**os.environ, "VC_WORKSPACE_NATIVE_TEST_CHECKPOINT": str(checkpoint)},
        input=json.dumps(value), capture_output=True, text=True, timeout=25)
    safe_output(result)
    if not check:
        return result
    if checkpoint:
        assert result.returncode == 86, (result.returncode, result.stdout, result.stderr)
        return None
    assert result.returncode == 0, (result.returncode, result.stdout, result.stderr)
    rows = [line.removeprefix("NATIVE_RECEIPT=") for line in result.stdout.splitlines()
            if line.startswith("NATIVE_RECEIPT=")]
    assert len(rows) == 1, result.stdout
    receipt = json.loads(rows[0])
    expected = {k: v for k, v in value.items() if k not in ("operation", "password")}
    expected["phase"] = dict(issue="issued", retire="retired", revoke="revoked")[value["operation"]]
    assert receipt == expected
    return receipt


def shadow():
    result = subprocess.run(["getent", "-s", "files", "shadow", USERNAME],
                            capture_output=True, check=True, text=True, timeout=5)
    return result.stdout.split(":")[1]


def matches(secret=0):
    hashed = shadow().encode()
    library = ctypes.CDLL("libcrypt.so.1")
    library.crypt.argtypes = [ctypes.c_char_p, ctypes.c_char_p]
    library.crypt.restype = ctypes.c_char_p
    return library.crypt(SECRETS[secret].encode(), hashed) == hashed


def sleeper(uid):
    process = subprocess.Popen(["runuser", "-u", USERNAME, "--", "sleep", "240"],
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    for _ in range(50):
        if subprocess.run(["pgrep", "-u", str(uid)], capture_output=True).returncode == 0:
            return process
        time.sleep(0.05)
    raise AssertionError("fixture UID process did not start")


def reconcile():
    # The production offline heartbeat: no control-plane network or edited
    # lifecycle state is substituted for process-crash recovery evidence.
    result = subprocess.run([BINARY, "--once", "--state-dir", "/var/lib/vcw-native-test-state"],
                            capture_output=True, text=True, timeout=25)
    safe_output(result)
    assert result.returncode == 0, result.stderr


def closed(revision):
    current = observe()
    assert current["lifecycle"]["revision"] == revision
    assert current["lifecycle"]["phase"] == "revoked"
    assert current["account"]["disabled"] and current["processes_absent"]
    # Missing login birth protection is unknown, never false-positive closure.
    assert current["login_writers_absent"] is None


def orphaned_writer(value):
    parent = None
    writer = None
    with Path("/etc/.pwd.lock").open("a") as password_lock:
        fcntl.lockf(password_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        try:
            parent = subprocess.Popen([TEST_BINARY, "--ignored", "--exact", CHILD, "--nocapture"],
                env={**os.environ, "VC_WORKSPACE_NATIVE_TEST_CHECKPOINT": "0"},
                stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, text=True)
            parent.stdin.write(json.dumps(value))
            parent.stdin.close()
            until = time.monotonic() + 5
            while time.monotonic() < until:
                # Rust's test harness invokes the fixture in a worker thread.
                children = set()
                for task in Path(f"/proc/{parent.pid}/task").iterdir():
                    children.update((task / "children").read_text().split())
                for pid in children:
                    if Path(f"/proc/{pid}/comm").read_text().strip() == "chpasswd":
                        writer = int(pid)
                        break
                if writer:
                    break
                time.sleep(0.025)
            assert writer, "actual Native shadow writer not observed"
            os.kill(writer, signal.SIGSTOP)
            gate_inode = (BASE / "account.lock").stat().st_ino
            assert any(p.stat().st_ino == gate_inode for p in Path(f"/proc/{writer}/fd").iterdir())
            parent.kill()
            parent.wait(timeout=5)
            blocked = request(value["identity"], "revoke", value["revision"] + 1,
                              connection=value["connection_id"])
            assert cli("native-account-credential", blocked, check=False).returncode
        finally:
            fcntl.lockf(password_lock, fcntl.LOCK_UN)
            if writer:
                os.kill(writer, signal.SIGCONT)
            if parent and parent.poll() is None:
                parent.kill()
                parent.wait(timeout=5)
    until = time.monotonic() + 5
    while time.monotonic() < until:
        if cli("accounts-reconcile", username=None, check=False).returncode == 0:
            break
        time.sleep(0.05)
    closed(value["revision"])


def suite():
    assert os.geteuid() == 0 and Path("/.dockerenv").is_file()
    assert os.environ.get("VC_WORKSPACE_DISPOSABLE_TEST") == "1"
    assert not BASE.exists() and not any(u.pw_name == USERNAME for u in pwd.getpwall())
    assert not Path("/dev/dri").exists()
    Path("/dev/dri").mkdir(mode=0o755)
    # A disposable null device exercises permission policy only, not GPU
    # rendering or host passthrough. No host device is mounted in this suite.
    os.mknod("/dev/dri/renderD128", stat.S_IFCHR | 0o660, os.makedev(1, 3))
    if not any(g.gr_name == "render" for g in grp.getgrall()):
        subprocess.run(["groupadd", "render"], check=True, capture_output=True)
    cli("native-account-provision")
    cli("native-account-provision")
    groups = {g.gr_name for g in grp.getgrall() if USERNAME in g.gr_mem}
    assert "render" in groups and not groups.intersection({"video", "sudo", "admin", "ssl-cert"})
    identity = observe()["identity"]
    assert identity == dict(username=USERNAME, uid=pwd.getpwnam(USERNAME).pw_uid, sid="")
    assert observe()["account"]["disabled"] and observe()["lifecycle"] is None
    assert not (BASE / "account-lifecycle.json").exists()
    for operation in ("account-provision", "account-inspect", "account-enable", "account-disable", "native-account-crash"):
        assert cli(operation, check=False).returncode
    assert cli("native-account-provision", username="vca665544332211", check=False).returncode
    one = request(identity, "issue", 1)
    # Caller environment cannot enable the test-only bypass on release CLI.
    os.environ["VC_WORKSPACE_NATIVE_TEST_CHECKPOINT"] = "0"
    assert cli("native-account-credential", one, check=False).returncode
    assert observe()["lifecycle"] is None and observe()["account"]["disabled"]
    first = apply(one)
    assert matches() and not observe()["account"]["disabled"]
    assert observe()["account"]["expiry_day"] == (one["expires_unix_seconds"] + 86399) // 86400
    desktop = sleeper(identity["uid"])
    two = request(identity, "retire", 2, connection=one["connection_id"], expiry=one["expires_unix_seconds"])
    assert cli("native-account-credential", two, check=False).returncode
    assert observe()["lifecycle"] == first and matches()
    second = apply(two)
    assert desktop.poll() is None and not matches()
    retired_hash = shadow()
    assert apply(two) == second and shadow() == retired_hash, "retire retry rewrote password"
    three = request(identity, "issue", 3, secret=1)
    with (BASE / "account.lock").open("rb") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        assert apply(three, check=False).returncode
    assert observe()["lifecycle"] == second
    third = apply(three)
    assert desktop.poll() is None and matches(1) and not matches(), "reconnect lost desktop or credential"
    for stale in (one, two, three, {**three, "identity": {**identity, "uid": identity["uid"] + 1}}):
        assert apply(stale, check=False).returncode
    assert observe()["lifecycle"] == third
    four = request(identity, "revoke", 4, connection=three["connection_id"])
    cli("native-account-credential", four)
    desktop.wait(timeout=5)
    closed(4)
    cli("native-account-credential", four)
    print("PASS Native provision, production PAM prerequisite, real password rotation, process-preserving retirement/reconnect and revoke", flush=True)

    # Abrupt process exit before/after an actual OS credential write. A failed
    # pending operation is never replayed; the same revision becomes revoked.
    revision = 4
    for operation, checkpoint in (("issue", 1), ("issue", 2), ("retire", 1), ("retire", 2)):
        revision += 1
        value = request(identity, "issue", revision)
        process = None
        if operation == "retire":
            apply(value)
            process = sleeper(identity["uid"])
            revision += 1
            value = request(identity, "retire", revision, connection=value["connection_id"], expiry=value["expires_unix_seconds"])
        apply(value, checkpoint=checkpoint)
        assert observe()["lifecycle"]["phase"] == ("issuing" if operation == "issue" else "retiring")
        assert apply(value, check=False).returncode
        reconcile()
        closed(revision)
        if process:
            process.wait(timeout=5)
    print("PASS Native real process crashes at four write boundaries, production offline recovery and stale request refusal", flush=True)

    revision += 1
    value = request(identity, "issue", revision)
    apply(value)
    revision += 1
    orphaned_writer(request(identity, "retire", revision, connection=value["connection_id"], expiry=value["expires_unix_seconds"]))
    print("PASS Native real chpasswd retains kernel account gate after dispatcher SIGKILL", flush=True)

    revision += 1
    value = request(identity, "issue", revision, expiry=int(time.time()) + 4)
    apply(value)
    process = sleeper(identity["uid"])
    revision += 1
    apply(request(identity, "retire", revision, connection=value["connection_id"], expiry=value["expires_unix_seconds"]))
    time.sleep(max(0, value["expires_unix_seconds"] - time.time()) + 0.1)
    assert apply(request(identity, "issue", revision + 1), check=False).returncode
    reconcile()
    closed(revision)
    process.wait(timeout=5)
    # Control plane reserved a new connection, but its issue never arrived.
    # A higher revoke must fence it despite the Guest retaining an older ID.
    revision += 2
    unknown = request(identity, "revoke", revision, connection="conn_never_delivered")
    cli("native-account-credential", unknown)
    closed(revision)
    assert cli("native-account-credential", {**unknown, "connection_id": "conn_wrong_retry"}, check=False).returncode
    print("PASS Native exact deadline, no expired-desktop resurrection, and higher revocation of an undelivered connection", flush=True)

    revision += 1
    apply(request(identity, "issue", revision))
    process = sleeper(identity["uid"])
    SNAPSHOT.write_text("{malformed")
    SNAPSHOT.chmod(0o644)
    assert cli("accounts-reconcile", username=None, check=False).returncode
    process.wait(timeout=5)
    assert shadow().startswith("!") and SNAPSHOT.read_text() == "{malformed"
    assert apply(request(identity, "issue", revision + 1), check=False).returncode
    assert not (BASE / "account-lifecycle.json").exists()
    print("PASS corrupt Native journal preserves evidence and closes OS account without fabricating a new version", flush=True)


if __name__ == "__main__":
    suite()

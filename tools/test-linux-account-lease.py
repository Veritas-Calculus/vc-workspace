"""Actual local shadow/UID/process fence tests. Disposable Linux container only.

No PVE/desktop login or default MCP acceptance is implied by this fixture.
"""
import ctypes
import fcntl
import json
import os
from pathlib import Path
import pwd
import signal
import subprocess
import time

BINARY = "/out/vc-workspace-guest-agent"
TEST_BINARY = "/out/vc-workspace-guest-agent-tests"
USERNAME = "vca665544332211"
BASE = Path("/var/lib/vc-workspace/computer-v2/users") / USERNAME
SECRET = "Vcw1!disposable-account-fence-test"


def cli(operation, payload=None, *, check=True, username=USERNAME):
    args = [BINARY, "computer-v2-" + operation]
    if username:
        args.extend(["--guest-user", username])
    result = subprocess.run(args, input=json.dumps(payload) if payload else None,
                            capture_output=True, text=True, timeout=20)
    assert SECRET not in result.stdout and SECRET not in result.stderr
    if check:
        assert result.returncode == 0, (operation, result.stderr)
    return result


def observe():
    return json.loads(cli("account-inspect").stdout)


def request(identity, operation="open", epoch=10, generation=1, expiry=None):
    value = dict(schema_version=1, identity=identity, lease_id=f"lease_test_{epoch}",
                 control_epoch=epoch, login_generation=generation,
                 expires_unix_seconds=expiry or int(time.time()) + 120, operation=operation)
    if operation == "open":
        value["password"] = SECRET
    elif operation == "revoke":
        value["expires_unix_seconds"] = 0
    return value


def apply(value):
    receipt = json.loads(cli("account-lease", value).stdout)
    expected = {k: v for k, v in value.items() if k not in ("operation", "password")}
    expected["phase"] = dict(open="open", seal="sealed", revoke="revoked")[value["operation"]]
    assert receipt == expected
    return receipt


def hash_password_matches():
    # Compare against the real local shadow verifier without printing the hash
    # or using root's passwordless `su` as false credential acceptance evidence.
    output = subprocess.run(["getent", "-s", "files", "shadow", USERNAME],
                            capture_output=True, check=True, text=True, timeout=5)
    hashed = output.stdout.split(":")[1]
    library = ctypes.CDLL("libcrypt.so.1")
    library.crypt.argtypes = [ctypes.c_char_p, ctypes.c_char_p]
    library.crypt.restype = ctypes.c_char_p
    return library.crypt(SECRET.encode(), hashed.encode()) == hashed.encode()


def child_process(uid):
    process = subprocess.Popen(["runuser", "-u", USERNAME, "--", "sleep", "120"],
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    for _ in range(50):
        if subprocess.run(["pgrep", "-u", str(uid)], capture_output=True).returncode == 0:
            return process
        time.sleep(0.05)
    raise AssertionError("fixture process did not start")


def crash(value, checkpoint):
    result = subprocess.run([TEST_BINARY, "--ignored", "--exact",
        "computer::session::account::lease::tests::account_lease_crash_child"],
        env={**os.environ, "VC_WORKSPACE_ACCOUNT_TEST_CHECKPOINT": str(checkpoint)},
        input=json.dumps(value), capture_output=True, text=True, timeout=20)
    assert result.returncode == 86, (result.returncode, result.stdout, result.stderr)
    assert SECRET not in result.stdout and SECRET not in result.stderr


def os_writer_outlives_dispatcher(identity, epoch, operation, program):
    # Block the real shadow-utils writer at the system password lock. Stopping
    # it once observed makes the crash window deterministic without substituting
    # the OS writer or adding a fault flag to the shipped executable.
    value = request(identity, epoch=epoch)
    if operation == "seal":
        apply(value)
        value["operation"] = "seal"
        value.pop("password")
    parent = None
    writer = None
    with Path("/etc/.pwd.lock").open("a") as password_lock:
        fcntl.lockf(password_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        try:
            parent = subprocess.Popen([BINARY, "computer-v2-account-lease", "--guest-user", USERNAME],
                stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, text=True)
            parent.stdin.write(json.dumps(value))
            parent.stdin.close()
            until = time.monotonic() + 5
            while time.monotonic() < until:
                children = Path(f"/proc/{parent.pid}/task/{parent.pid}/children").read_text().split()
                for pid in children:
                    if Path(f"/proc/{pid}/comm").read_text().strip() == program:
                        writer = int(pid)
                        break
                if writer:
                    break
                time.sleep(0.025)
            assert writer, "actual OS writer not observed"
            os.kill(writer, signal.SIGSTOP)
            gate_inode = (BASE / "account.lock").stat().st_ino
            assert any(p.stat().st_ino == gate_inode for p in Path(f"/proc/{writer}/fd").iterdir()), "OS writer did not inherit gate"
            parent.kill()
            parent.wait(timeout=5)
            assert cli("account-lease", request(identity, epoch=epoch+1), check=False).returncode, "new write passed orphaned OS writer"
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
    state = observe()
    assert state["lifecycle"]["control_epoch"] == epoch and state["lifecycle"]["phase"] == "revoked"
    assert state["account"]["disabled"]
    print(f"PASS actual {program} retains account gate after dispatcher SIGKILL; recovery before next login", flush=True)


def suite():
    assert os.geteuid() == 0 and Path("/.dockerenv").is_file()
    assert os.environ.get("VC_WORKSPACE_DISPOSABLE_TEST") == "1"
    assert not BASE.exists() and not any(u.pw_name == USERNAME for u in pwd.getpwall())
    cli("account-provision")
    # The test-only crash entry point must never be part of the release CLI.
    assert cli("account-crash", check=False).returncode
    identity = observe()["identity"]
    assert identity == dict(username=USERNAME, uid=pwd.getpwnam(USERNAME).pw_uid, sid="")
    assert observe()["account"]["disabled"] and observe()["lifecycle"] is None
    abandoned = BASE / (".account-" + "a" * 32 + ".tmp")
    abandoned.write_text("non-secret interrupted snapshot")
    abandoned.chmod(0o600)
    observe()
    assert not abandoned.exists()
    deadline = int(time.time()) + 120
    opening = request(identity, expiry=deadline)
    apply(opening)
    assert hash_password_matches() and not observe()["account"]["disabled"]
    assert observe()["account"]["expiry_day"] == (deadline + 86399) // 86400
    sleeper = child_process(identity["uid"])
    sealed = apply(request(identity, "seal", expiry=deadline))
    assert sleeper.poll() is None and not hash_password_matches(), "seal destroyed process or retained credential"
    assert apply(request(identity, "seal", expiry=deadline)) == sealed
    # Kernel lock contention cannot admit a stale request after a prior read.
    with (BASE / "account.lock").open("rb") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        assert cli("account-lease", request(identity, generation=2, expiry=deadline), check=False).returncode
    assert observe()["lifecycle"] == sealed
    assert cli("account-enable", check=False).returncode
    assert cli("account-disable", check=False).returncode
    assert cli("account-lease", {**opening, "identity": {**identity, "uid": identity["uid"] + 1}}, check=False).returncode
    apply(request(identity, generation=2, expiry=deadline))
    sleeper.wait(timeout=5)
    assert hash_password_matches(), "reconnect did not install new one-use credential"
    for operation in ("open", "seal", "revoke"):
        assert cli("account-lease", request(identity, operation, expiry=deadline), check=False).returncode
    apply(request(identity, "revoke", generation=2))
    assert observe()["account"]["disabled"] and observe()["processes_absent"]
    assert cli("account-lease", request(identity, generation=3, expiry=deadline), check=False).returncode
    print("PASS Linux real shadow password/retirement, generation reconnect, stale writes, kernel gate and UID checks", flush=True)

    # Actual process exit, not editing a journal to claim a crash. Each stage
    # leaves a durable pending phase and kernel-released gate for recovery.
    for epoch, operation, checkpoint in [(11, "open", 1), (12, "open", 2), (13, "seal", 2)]:
        value = request(identity, epoch=epoch)
        if operation == "seal":
            apply(value)
            value.pop("password")
            value["operation"] = "seal"
        crash(value, checkpoint)
        assert observe()["lifecycle"]["phase"] == ("opening" if operation == "open" else "sealing")
        assert cli("account-lease", value, check=False).returncode
        # This is the same reconciler used by the offline heartbeat, with a
        # real --once process, no PVE/control-plane network and no journal edit.
        result = subprocess.run([BINARY, "--once", "--state-dir", "/var/lib/vcw-account-test-state"],
                                capture_output=True, text=True, timeout=20)
        assert result.returncode == 0, result.stderr
        observed = observe()
        assert observed["account"]["disabled"] and observed["processes_absent"]
        assert observed["lifecycle"]["phase"] == "revoked"
    print("PASS Linux process exits after durable intent/shadow open/shadow seal; offline heartbeat converged", flush=True)

    # Precise expiry is enforced by the root heartbeat, not Linux's coarse day
    # field. Wait the original deadline; do not shorten a journal to simulate it.
    expiry = int(time.time()) + 3
    apply(request(identity, epoch=14, expiry=expiry))
    sleeper = child_process(identity["uid"])
    daemon = subprocess.Popen([BINARY, "--interval-seconds", "5", "--state-dir", "/var/lib/vcw-account-test-state"],
                              stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        until = time.monotonic() + 12
        while time.monotonic() < until:
            observation = cli("account-inspect", check=False)
            if observation.returncode == 0 and json.loads(observation.stdout)["lifecycle"]["phase"] == "revoked":
                break
            time.sleep(0.1)
        sleeper.wait(timeout=5)
        assert daemon.poll() is None
        assert observe()["lifecycle"]["phase"] == "revoked" and observe()["account"]["disabled"]
    finally:
        daemon.terminate()
        daemon.wait(timeout=5)
    assert cli("account-lease", request(identity, epoch=14, generation=2), check=False).returncode
    os_writer_outlives_dispatcher(identity, 15, "open", "usermod")
    os_writer_outlives_dispatcher(identity, 16, "seal", "chpasswd")

    # Fail closed on corrupt or user-writable lifecycle records, without replacing
    # them from an old useradd marker, and without touching a foreign UID.
    journal = BASE / "account-lifecycle.json"
    original = journal.read_bytes()
    journal.write_text("{")
    assert cli("account-lease", request(identity, epoch=17), check=False).returncode
    assert cli("account-inspect", check=False).returncode
    assert cli("accounts-reconcile", username=None, check=False).returncode
    assert journal.read_text() == "{"
    journal.write_bytes(original)
    journal.chmod(0o664)
    assert cli("account-lease", request(identity, epoch=17), check=False).returncode
    journal.chmod(0o644)
    owned = BASE / "account.json"
    owned.chmod(0o644)
    assert cli("account-inspect", check=False).returncode, "ownership record is not private"
    owned.chmod(0o600)
    saved = owned.read_bytes()
    owned.write_text("{")
    assert cli("account-provision", check=False).returncode
    owned.write_bytes(saved)
    # Preserve the immutable UID when the OS account is deleted. Reuse by an
    # unrelated new account must not be mistaken for the old owned identity.
    subprocess.run(["userdel", USERNAME], check=True, capture_output=True)
    assert cli("account-provision", check=False).returncode
    assert observe()["identity"] == identity and not observe()["account"]["exists"]
    subprocess.run(["useradd", "--uid", str(identity["uid"]), "vcw-foreign-fixture"], check=True, capture_output=True)
    assert cli("account-lease", request(identity, "revoke", epoch=17), check=False).returncode
    assert cli("account-inspect", check=False).returncode
    assert pwd.getpwnam("vcw-foreign-fixture").pw_uid == identity["uid"]
    subprocess.run(["userdel", "vcw-foreign-fixture"], check=True, capture_output=True)
    print("PASS Linux original expiry, corrupt journal/privacy, deleted identity and UID reuse rejection", flush=True)


if __name__ == "__main__":
    subprocess.run([TEST_BINARY, "--ignored", "--exact",
                    "computer::session::account::lease::birth::tests::kernel_handles_and_parent_death_prevent_stale_process_signals"],
                   check=True, timeout=30)
    suite()

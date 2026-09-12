"""Actual release CLI + isolated Unix nodes. Never run on a host /tmp."""
import json
import os
from pathlib import Path
import pwd
import socket
import subprocess

if not __debug__ or os.geteuid() != 0 or not Path("/.dockerenv").is_file() or os.environ.get("VC_WORKSPACE_DISPOSABLE_TEST") != "1":
    raise SystemExit("requires an unoptimized, explicitly opted-in disposable Linux container")
agent = "/out/vc-workspace-guest-agent"
username = "vcwabcdef123456"

def cli(command, value=None):
    return subprocess.run([agent, "computer-v2-" + command, "--guest-user", username], input=json.dumps(value) if value else None,
                          capture_output=True, text=True, timeout=25)

assert cli("native-account-provision").returncode == 0
uid = pwd.getpwnam(username).pw_uid
assert uid >= 1000
other = uid + 1
sockets = Path("/tmp/.X11-unix")
assert not sockets.exists()
sockets.mkdir(mode=0o1777); sockets.chmod(0o1777)
revision = 0

def revoke(ok=True):
    global revision
    revision += 1
    result = cli("native-account-credential", {"schema_version": 1, "identity": {"username": username, "uid": uid, "sid": ""},
                 "connection_id": "conn_display_cleanup_test", "revision": revision, "expires_unix_seconds": 0, "operation": "revoke"})
    assert (result.returncode == 0) == ok, result.stderr
    state = json.loads(cli("native-account-inspect").stdout)
    assert state["account"]["disabled"] and state["processes_absent"]

def pair(display, lock_owner=uid, socket_owner=uid, pid=None):
    lock, path = Path(f"/tmp/.X{display}-lock"), sockets / f"X{display}"
    assert not lock.exists() and not path.exists()
    if pid is None:
        child = subprocess.Popen(["/bin/true"]); pid = child.pid; child.wait()
    lock.write_text(f"{pid:10d}\n"); lock.chmod(0o444); os.chown(lock, lock_owner, lock_owner)
    sock = socket.socket(socket.AF_UNIX); sock.bind(str(path)); sock.close(); os.chown(path, socket_owner, socket_owner)
    return lock, path

own = pair(70)
foreign = pair(71, other, other)
ignored = Path("/tmp/.X0072-lock"); ignored.write_text("unrelated"); os.chown(ignored, uid, uid)
revoke()
assert all(not p.exists() for p in own) and all(p.exists() for p in foreign) and ignored.read_text() == "unrelated"
print("PASS actual revoke: exact dead own nodes removed; foreign UID and noncanonical files preserved")

mixed = pair(73, uid, other)
revoke(False)
assert all(p.exists() for p in mixed)
for p in mixed: p.unlink()

live = pair(74, pid=os.getpid())
revoke(False)
assert all(p.exists() for p in live)
for p in live: p.unlink()

target = Path("/tmp/private-target"); target.write_text("preserve"); target.chmod(0o600)
link = Path("/tmp/.X75-lock"); link.symlink_to(target); os.chown(link, uid, uid, follow_symlinks=False)
revoke(False)
assert link.is_symlink() and target.read_text() == "preserve"
link.unlink()

hard = Path("/tmp/.X76-lock"); os.link(target, hard); os.chown(target, uid, uid)
revoke(False)
assert hard.exists() and target.read_text() == "preserve"
hard.unlink()

sockets.chmod(0o777)
revoke(False)
assert all(p.exists() for p in foreign)
sockets.chmod(0o1777)
orphan = sockets / "X77"
sock = socket.socket(socket.AF_UNIX); sock.bind(str(orphan)); sock.close(); os.chown(orphan, uid, uid)
revoke()
assert not orphan.exists() and all(p.exists() for p in foreign)
print("PASS mixed-owner/live PID/symlink/hardlink/non-sticky refusal; closed-UID socket-only crash recovery")

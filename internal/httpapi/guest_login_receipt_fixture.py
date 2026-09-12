"""Isolated acceptance only: keep one logind user-manager job in progress.

No production Guest flag or login bypass. The delay belongs to a newly owned
UID, not to the system-wide user@ template or an existing desktop.
"""
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import select
import signal
import stat
import subprocess
import sys

operation, marker, username, number = sys.argv[1:]
assert os.geteuid() == 0 and re.fullmatch(r"svc[0-9a-f]{24}", marker)
assert re.fullmatch(r"vca[0-9a-f]{12}", username)
uid = int(number); assert 1000 <= uid < 2**32-1 and str(uid) == number
base = Path("/run") / ("vc-workspace-" + marker)
directory = base / "receipt"
unit = f"user@{uid}.service"
dropdir = Path("/run/systemd/system") / (unit + ".d")
dropfile = dropdir / "90-vc-workspace-receipt.conf"
account = Path("/var/lib/vc-workspace/computer-v2/users") / username
args = ["/usr/bin/python3", str(directory / "hold.py"), "hold", marker, username, number]
content = ("[Service]\nTimeoutStartSec=120\nTimeoutStopSec=5\nExecStartPre=+" + " ".join(args) + "\n").encode()

def run(*command, check=True):
    return subprocess.run(command, check=check, capture_output=True, text=True, timeout=20)

def trusted(path):
    for parent in (path, *path.parents):
        m = parent.lstat()
        assert stat.S_ISDIR(m.st_mode) and m.st_uid == 0 and not m.st_mode & 0o022

def read(path):
    trusted(path.parent)
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as f:
        m = os.fstat(f.fileno())
        assert stat.S_ISREG(m.st_mode) and m.st_uid == 0 and m.st_nlink == 1 and stat.S_IMODE(m.st_mode) == 0o600 and m.st_size <= 32768
        value = f.read(32769); assert len(value) <= 32768
        return value

def create(path, value):
    trusted(path.parent)
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "wb") as f:
        f.write(value); f.flush(); os.fsync(f.fileno())

def identity(pid):
    raw = Path(f"/proc/{pid}/stat").read_text()
    head, tail = raw.rsplit(") ", 1); assert head.startswith(f"{pid} (")
    fields = tail.split()
    return {"pid": pid, "start_ticks": int(fields[19]), "boot_id": Path("/proc/sys/kernel/random/boot_id").read_text().strip()}

def exited(fd):
    p = select.poll(); p.register(fd, select.POLLIN | select.POLLHUP)
    return bool(p.poll(0))

def pin(value):
    assert set(value) == {"pid", "start_ticks", "boot_id"} and value["pid"] > 1
    if value["boot_id"] != Path("/proc/sys/kernel/random/boot_id").read_text().strip(): return None
    try: fd = os.pidfd_open(value["pid"])
    except ProcessLookupError: return None
    if exited(fd) or identity(value["pid"]) != value: os.close(fd); return None
    assert Path(f'/proc/{value["pid"]}/cmdline').read_bytes().split(b"\0")[:-1] == [v.encode() for v in args]
    assert os.path.samefile(f'/proc/{value["pid"]}/exe', args[0])
    assert any(line.split() == ["Uid:", "0", "0", "0", "0"] for line in Path(f'/proc/{value["pid"]}/status').read_text().splitlines())
    return fd

def property(name):
    return run("systemctl", "show", unit, "--property=" + name, "--value").stdout.strip()

assert json.loads(read(account / "account.json")) == {"username": username, "uid": uid}
assert run("getent", "-s", "files", "passwd", username).stdout.split(":")[2] == number

if operation == "setup":
    source = sys.stdin.buffer.read(32769); assert 0 < len(source) <= 32768
    trusted(base); assert read(base / "manifest.json")
    assert not os.path.lexists(directory) and not os.path.lexists(dropdir)
    assert property("ActiveState") == "inactive" and property("Job") in ("", "0")
    directory.mkdir(mode=0o700)
    create(directory / "manifest.json", json.dumps({"marker": marker, "uid": uid, "username": username,
           "dropin": base64.b64encode(content).decode(), "source_sha256": hashlib.sha256(source).hexdigest()}).encode())
    create(directory / "hold.py", source)
    dropdir.mkdir(mode=0o755); create(dropfile, content)
    run("systemctl", "daemon-reload")
    print('{"stage":"armed"}'); sys.exit(0)

if operation == "restore" and not directory.exists():
    print('{"stage":"absent"}'); sys.exit(0)
manifest = json.loads(read(directory / "manifest.json"))
assert (manifest["marker"], manifest["uid"], manifest["username"]) == (marker, uid, username)
assert base64.b64decode(manifest["dropin"], validate=True) == content
assert hashlib.sha256(read(directory / "hold.py")).hexdigest() == manifest["source_sha256"]

if operation == "hold":
    journal = json.loads(read(account / "login-writers.json"))
    assert journal["schema_version"] == 2 and len(journal["writers"]) == 1 and journal["writers"][0]["scope"] is None
    group = Path("/proc/self/cgroup").read_text().strip()
    expected = f"0::/user.slice/user-{uid}.slice/{unit}"
    assert group in (expected, expected + "/.control")
    assert property("Job") not in ("", "0")
    create(directory / "held.json", json.dumps({"process": identity(os.getpid()), "creator": journal["writers"][0]["process"], "group": group}).encode())
    os.kill(os.getpid(), signal.SIGSTOP)
    create(directory / "resumed.json", b"{}")
elif operation == "probe":
    if not (directory / "held.json").exists(): print('{"stage":"waiting"}'); sys.exit(0)
    held = json.loads(read(directory / "held.json")); fd = pin(held["process"])
    assert fd is not None
    try:
        assert not exited(fd) and property("Job") not in ("", "0")
        print(json.dumps({"stage": "pending", "root_pid": held["process"]["pid"], "job": property("Job")}))
    finally: os.close(fd)
elif operation in ("release", "restore"):
    if dropfile.exists():
        assert read(dropfile) == content
        dropfile.unlink(); dropdir.rmdir(); run("systemctl", "daemon-reload")
    if (directory / "held.json").exists():
        fd = pin(json.loads(read(directory / "held.json"))["process"])
        if fd is not None:
            try:
                signal.pidfd_send_signal(fd, signal.SIGCONT if operation == "release" else signal.SIGKILL)
                p = select.poll(); p.register(fd, select.POLLIN | select.POLLHUP); assert p.poll(10000)
            finally: os.close(fd)
    if operation == "release":
        assert (directory / "resumed.json").exists()
        print('{"stage":"released"}')
    else:
        # No later lease is admitted during fixture teardown; this exact UID
        # and drop-in were proved absent/owned before installation.
        run("systemctl", "stop", unit)
        assert property("ActiveState") in ("inactive", "failed") and property("Job") in ("", "0")
        run("systemctl", "reset-failed", unit, check=False)
        assert property("ActiveState") == "inactive"
        for p in directory.iterdir():
            assert p.name in ("manifest.json", "hold.py", "held.json", "resumed.json")
            read(p); p.unlink()
        directory.rmdir()
        print('{"stage":"restored"}')
else:
    raise AssertionError("unknown receipt fixture operation")

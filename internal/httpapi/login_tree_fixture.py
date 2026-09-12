"""Opt-in isolated Guest kernel proof, not a production login implementation.

Demonstrates the fork-before-setuid gap and tests systemd's PIDFDs/cgroup.kill
boundary without replacing PAM, changing host cgroups, or using numeric kills.
"""
import json
import os
import pathlib
import re
import select
import signal
import socket
import stat
import subprocess
import sys
import time

operation, marker = sys.argv[1:3]
assert re.fullmatch(r"tree[0-9a-f]{24}", marker)
base = pathlib.Path("/run") / ("vc-workspace-" + marker)
actor_path = base / "actor.py"

if operation == "actor":
    # This process has no GLib threads or bus connection when it forks. Its
    # child remains root until the test explicitly releases the UID change.
    uid, gid, descriptor = map(int, sys.argv[3:])
    assert os.geteuid() == 0 and uid >= 1000 and gid >= 1000
    channel = socket.socket(fileno=descriptor)
    assert channel.recv(16) == b"fork"
    if os.fork() == 0:
        channel.send(json.dumps({"pid": os.getpid(), "uid": os.geteuid(), "parent": os.getppid()}).encode())
        assert channel.recv(16) == b"advance"
        os.setgroups([]); os.setgid(gid); os.setuid(uid)
        channel.send(json.dumps({"pid": os.getpid(), "uid": os.geteuid(), "parent": os.getppid()}).encode())
    channel.close()
    while True: signal.pause()

assert os.geteuid() == 0
from gi.repository import Gio, GLib

def trusted(path):
    for parent in (path, *path.parents):
        meta = parent.lstat()
        assert stat.S_ISDIR(meta.st_mode) and meta.st_uid == 0 and not meta.st_mode & 0o022

def read(path, maximum=65536):
    trusted(path.parent)
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as stream:
        meta = os.fstat(stream.fileno())
        assert stat.S_ISREG(meta.st_mode) and meta.st_uid == 0 and meta.st_nlink == 1 and not meta.st_mode & 0o022 and meta.st_size <= maximum
        raw = stream.read(maximum + 1); assert len(raw) <= maximum
        return raw

def create(path, raw):
    trusted(path.parent)
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "wb") as stream:
        stream.write(raw); stream.flush(); os.fsync(stream.fileno())

def run(*args, check=True):
    return subprocess.run(args, check=check, capture_output=True, text=True, timeout=15)

def account_name(index): return "vcwct" + marker[4:16] + str(index)
def scope_name(index): return "vcworkspace-" + marker + "-" + str(index) + ".scope"
slice_name = "vcworkspacetest" + marker[4:] + ".slice"
boot = pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip()

def identity(pid):
    head, tail = pathlib.Path(f"/proc/{pid}/stat").read_text().rsplit(") ", 1)
    assert head.startswith(f"{pid} (")
    return {"pid": pid, "start": int(tail.split()[19]), "boot": boot}

def exited(fd):
    poll = select.poll(); poll.register(fd, select.POLLIN | select.POLLHUP)
    return bool(poll.poll(0))

def pin(value):
    assert set(value) == {"pid", "start", "boot"} and value["pid"] > 1 and value["start"] > 0
    if value["boot"] != boot: return None
    try: fd = os.pidfd_open(value["pid"])
    except ProcessLookupError: return None
    try:
        if exited(fd) or identity(value["pid"]) != value or exited(fd): os.close(fd); return None
        return fd
    except FileNotFoundError:
        assert exited(fd); os.close(fd); return None

def wait_exit(fd):
    poll = select.poll(); poll.register(fd, select.POLLIN | select.POLLHUP)
    assert poll.poll(5000), "pinned process did not exit"

def wait(condition):
    deadline = time.monotonic() + 5
    while not condition():
        assert time.monotonic() < deadline, "kernel observation did not converge"
        time.sleep(0.02)

def kill(value):
    fd = pin(value)
    if fd is not None:
        try:
            try: signal.pidfd_send_signal(fd, signal.SIGKILL)
            except ProcessLookupError: pass
            wait_exit(fd)
        finally: os.close(fd)

bus = Gio.bus_get_sync(Gio.BusType.SYSTEM, None)
destination = "org.freedesktop.systemd1"
manager_path = "/org/freedesktop/systemd1"

def call(member, signature, values, result=None, path=manager_path, interface=destination + ".Manager"):
    return bus.call_sync(destination, path, interface, member,
                         GLib.Variant(signature, values), GLib.VariantType.new(result) if result else None,
                         Gio.DBusCallFlags.NONE, 5000, None).unpack()

def absent_error(error):
    return Gio.DBusError.get_remote_error(error) in ("org.freedesktop.systemd1.NoSuchUnit", "org.freedesktop.systemd1.LoadFailed")

def unit_path(name):
    try: return call("GetUnit", "(s)", (name,), "(o)")[0]
    except GLib.Error as error:
        if absent_error(error): return None
        raise

def populated(index):
    name = scope_name(index)
    path = unit_path(name)
    if path is None: return False
    values = call("GetAll", "(s)", (destination + ".Scope",), "(a{sv})", path, "org.freedesktop.DBus.Properties")[0]
    expected = "/" + slice_name + "/" + name
    group = values.get("ControlGroup", "")
    # ControlGroup is inherited from Unit's cgroup interface on some versions.
    if not group:
        group = call("Get", "(ss)", (destination + ".Scope", "ControlGroup"), "(v)", path, "org.freedesktop.DBus.Properties")[0]
    if not group: return False
    assert group == expected, "scope points outside this exact fixture"
    root = pathlib.Path("/sys/fs/cgroup") / group.lstrip("/")
    try:
        trusted(root)
        assert (root / "cgroup.type").read_text().strip() == "domain"
        assert (root / "cgroup.kill").exists(), "kernel subtree kill required; enumeration is not accepted"
        flags = dict(line.split() for line in (root / "cgroup.events").read_text().splitlines())
        return flags["populated"] == "1"
    except FileNotFoundError:
        return False

def kill_scope(index):
    try: call("KillUnit", "(ssi)", (scope_name(index), "all", int(signal.SIGKILL)))
    except GLib.Error as error:
        if not absent_error(error): raise

def attach(index, fd):
    assert unit_path(scope_name(index)) is None, "never reuse a scope name"
    descriptors = Gio.UnixFDList.new()
    slot = descriptors.append(fd)
    properties = [
        ("PIDFDs", GLib.Variant("ah", [slot])),
        ("Slice", GLib.Variant("s", slice_name)),
        ("CollectMode", GLib.Variant("s", "inactive-or-failed")),
        ("KillMode", GLib.Variant("s", "control-group")),
        ("TimeoutStopUSec", GLib.Variant("t", 5000000)),
    ]
    reply, _ = bus.call_with_unix_fd_list_sync(destination, manager_path, destination + ".Manager", "StartTransientUnit",
        GLib.Variant("(ssa(sv)a(sa(sv)))", (scope_name(index), "fail", properties, [])),
        GLib.VariantType.new("(o)"), Gio.DBusCallFlags.NONE, 5000, descriptors, None)
    assert reply.unpack()[0].startswith(manager_path + "/job/")
    # The job receipt is not the attachment proof. Inspect the actual kernel
    # membership before permitting the creator to fork.
    return "/" + slice_name + "/" + scope_name(index)

def uid_absent(uid):
    result = run("pgrep", "-u", str(uid), check=False)
    assert result.returncode in (0, 1)
    return result.returncode == 1

def account(index):
    result = run("getent", "-s", "files", "passwd", account_name(index), check=False)
    if result.returncode == 2: return None
    assert result.returncode == 0
    fields = result.stdout.strip().split(":")
    assert len(fields) == 7 and fields[0] == account_name(index) and fields[4] == marker and fields[6] == "/usr/sbin/nologin"
    assert int(fields[2]) >= 1000 and int(fields[3]) >= 1000
    return int(fields[2]), int(fields[3])

if operation == "setup":
    source = sys.stdin.buffer.read(32769); assert 0 < len(source) <= 32768
    assert not os.path.lexists(base) and unit_path(slice_name) is None
    for i in range(2):
        assert run("getent", "passwd", account_name(i), check=False).returncode == 2
        assert not os.path.lexists(pathlib.Path("/home") / account_name(i))
    for i in range(4): assert unit_path(scope_name(i)) is None
    base.mkdir(mode=0o700)
    create(base / "manifest.json", json.dumps({"marker": marker, "boot": boot, "accounts": [account_name(i) for i in range(2)], "scopes": [scope_name(i) for i in range(4)]}).encode())
    create(actor_path, source)
    for i in range(2):
        run("useradd", "--user-group", "--no-create-home", "--shell", "/usr/sbin/nologin", "--comment", marker, account_name(i))
        assert account(i) is not None
    print('{"stage":"ready"}')
elif operation == "test":
    assert json.loads(read(base / "manifest.json"))["boot"] == boot
    channels = []
    processes = []
    handles = []
    def actor(index, identity_pair, scoped):
        uid, gid = identity_pair
        parent_channel, child_channel = socket.socketpair(socket.AF_UNIX, socket.SOCK_SEQPACKET)
        parent_channel.settimeout(5)
        process = subprocess.Popen(["/usr/bin/python3", str(actor_path), "actor", marker, str(uid), str(gid), str(child_channel.fileno())],
            pass_fds=(child_channel.fileno(),), stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        child_channel.close(); channels.append(parent_channel); processes.append(process)
        creator = identity(process.pid); creator_fd = pin(creator); assert creator_fd is not None
        handles.append(creator_fd)
        create(base / f"creator-{index}.json", json.dumps(creator).encode())
        if scoped:
            expected = attach(index, creator_fd)
            wait(lambda: pathlib.Path(f"/proc/{process.pid}/cgroup").read_text().strip() == "0::" + expected)
        parent_channel.send(b"fork")
        child = json.loads(parent_channel.recv(4096))
        assert child["uid"] == 0 and child["parent"] == process.pid
        child_identity = identity(child["pid"]); child_fd = pin(child_identity); assert child_fd is not None
        handles.append(child_fd)
        create(base / f"child-{index}.json", json.dumps(child_identity).encode())
        if scoped:
            assert pathlib.Path(f"/proc/{child['pid']}/cgroup").read_text().strip() == "0::" + expected
            assert populated(index)
        return creator_fd, child_fd, parent_channel

    first_account, control_account = account(0), account(1)
    assert first_account and control_account
    first_uid, control_uid = first_account[0], control_account[0]
    assert first_uid != control_uid
    try:
        # Negative control: parent pidfd + current UID absence is insufficient.
        creator, child, channel = actor(0, first_account, False)
        signal.pidfd_send_signal(creator, signal.SIGKILL); wait_exit(creator)
        assert not exited(child) and uid_absent(first_uid)
        channel.send(b"advance")
        advanced = json.loads(channel.recv(4096))
        assert advanced["uid"] == first_uid and not uid_absent(first_uid)
        signal.pidfd_send_signal(child, signal.SIGKILL); wait_exit(child)
        assert uid_absent(first_uid)

        creator, child, channel = actor(1, first_account, True)
        control_parent, control_child, control_channel = actor(2, control_account, True)
        signal.pidfd_send_signal(creator, signal.SIGKILL); wait_exit(creator)
        assert not exited(child) and uid_absent(first_uid) and populated(1)
        kill_scope(1); wait_exit(child)
        wait(lambda: not populated(1))
        assert uid_absent(first_uid)
        journal = run("journalctl", "--unit", scope_name(1), "--no-pager", "--output=cat", "--lines=40").stdout
        assert "Killed unit cgroup with SIGKILL on client request." in journal, "systemd did not confirm its kernel cgroup.kill path"
        assert not exited(control_parent) and not exited(control_child) and populated(2)
        control_channel.send(b"advance")
        assert json.loads(control_channel.recv(4096))["uid"] == control_uid

        # An old unique scope name cannot address a new login of the same UID.
        fresh_parent, fresh_child, fresh_channel = actor(3, first_account, True)
        kill_scope(1)
        assert not exited(fresh_parent) and not exited(fresh_child) and populated(3)
        fresh_channel.send(b"advance")
        assert json.loads(fresh_channel.recv(4096))["uid"] == first_uid
        kill_scope(3); wait_exit(fresh_parent); wait_exit(fresh_child)
        kill_scope(2); wait_exit(control_parent); wait_exit(control_child)
        assert uid_absent(first_uid) and uid_absent(control_uid)
        print(json.dumps({"stage": "verified", "orphan_root_reproduced": True, "late_uid_reproduced": True,
                          "pidfd_attachment": True, "kernel_subtree_kill": True, "control_survived": True, "fresh_scope_survived_old_kill": True}))
    finally:
        for channel in channels: channel.close()
        for fd in handles: os.close(fd)
        for process in processes:
            if process.poll() is not None: process.wait()
elif operation == "cleanup":
    if not base.exists(): print('{"stage":"absent"}'); sys.exit(0)
    value = json.loads(read(base / "manifest.json"))
    assert value["marker"] == marker and value["accounts"] == [account_name(i) for i in range(2)] and value["scopes"] == [scope_name(i) for i in range(4)]
    for index in range(1, 4): kill_scope(index)
    for path in base.iterdir():
        if re.fullmatch(r"(?:creator|child)-[0-3]\.json", path.name): kill(json.loads(read(path)))
    # Also recover a creator/child if interruption preceded its record write.
    # Match exact interpreter, private file and marker, not a process prefix.
    for path in pathlib.Path("/proc").iterdir():
        if not path.name.isdecimal(): continue
        try:
            argv = (path / "cmdline").read_bytes().split(b"\0")[:-1]
            if len(argv) == 7 and argv[:4] == [b"/usr/bin/python3", str(actor_path).encode(), b"actor", marker.encode()]:
                kill(identity(int(path.name)))
        except (FileNotFoundError, ProcessLookupError): continue
    for i in range(2):
        bound = account(i)
        if bound is not None:
            assert uid_absent(bound[0])
            run("userdel", account_name(i))
            assert account(i) is None
    for i in range(4):
        name = scope_name(i)
        if unit_path(name) is not None: run("systemctl", "stop", name)
    if unit_path(slice_name) is not None: run("systemctl", "stop", slice_name)
    for i in range(1, 4): assert not populated(i)
    for path in base.iterdir():
        assert path.name in ("manifest.json", "actor.py") or re.fullmatch(r"(?:creator|child)-[0-3]\.json", path.name)
        read(path); path.unlink()
    base.rmdir()
    print('{"stage":"clean"}')
else:
    raise AssertionError("unknown fixture operation")

"""Private PVE acceptance only: pause one owned PAM login after registration.

Never embedded in the Guest binary or installed in production images. Signals
use pinned pidfds, not journal PIDs. Recovery keeps exact pre-write PAM bytes.
"""
import base64
import json
import os
import pathlib
import re
import select
import signal
import stat
import sys
import time

operation, marker, username, checkpoint = sys.argv[1:]
assert os.geteuid() == 0 and re.fullmatch(r"svc[0-9a-f]{24}", marker)
assert re.fullmatch(r"vca[0-9a-f]{12}", username)
assert checkpoint in ("before_auth", "after_auth", "orphan_after_auth")
base = pathlib.Path("/run") / ("vc-workspace-" + marker)
directory = base / "birth"
pam = pathlib.Path("/etc/pam.d/xrdp-sesman")
stage = pam.parent / (".vcw-" + marker + "-birth")
account = pathlib.Path("/var/lib/vc-workspace/computer-v2/users") / username
binary = "/usr/local/sbin/vc-workspace-guest-agent"
boot = pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip()

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
        return raw, stat.S_IMODE(meta.st_mode)

def create(path, raw):
    trusted(path.parent)
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "wb") as stream:
        stream.write(raw); stream.flush(); os.fsync(stream.fileno())

def write_json(name, value):
    create(directory / name, json.dumps(value).encode())

def read_json(name):
    raw, mode = read(directory / name)
    assert mode == 0o600
    return json.loads(raw)

def replace_pam(expected, replacement, mode):
    assert read(pam)[0] == expected, "PAM changed outside owned fixture"
    create(stage, replacement)
    try:
        os.chmod(stage, mode)
        assert read(pam)[0] == expected
        os.replace(stage, pam)
        fd = os.open(pam.parent, os.O_RDONLY | os.O_DIRECTORY)
        try: os.fsync(fd)
        finally: os.close(fd)
    finally:
        if os.path.lexists(stage):
            assert read(stage)[0] == replacement; stage.unlink()

def proc(pid):
    text = pathlib.Path(f"/proc/{pid}/stat").read_text()
    head, tail = text.rsplit(") ", 1)
    assert head.startswith(f"{pid} (")
    fields = tail.split()
    return {"pid": pid, "start_ticks": int(fields[19]), "boot_id": boot}, fields[0], int(fields[1])

def exited(fd):
    poll = select.poll(); poll.register(fd, select.POLLIN | select.POLLHUP)
    return bool(poll.poll(0))

def pin(identity):
    assert set(identity) == {"pid", "start_ticks", "boot_id"} and identity["pid"] > 1 and identity["start_ticks"] > 0
    if identity["boot_id"] != boot: return None
    try: fd = os.pidfd_open(identity["pid"])
    except ProcessLookupError: return None
    try:
        if exited(fd): os.close(fd); return None
        if proc(identity["pid"])[0] != identity: os.close(fd); return None
        if exited(fd): os.close(fd); return None
        return fd
    except FileNotFoundError:
        assert exited(fd); os.close(fd); return None

def capture(pid, argv=None):
    identity = proc(pid)[0]
    fd = pin(identity); assert fd is not None
    try:
        status = pathlib.Path(f"/proc/{pid}/status").read_text().splitlines()
        assert any(line.split() == ["Uid:", "0", "0", "0", "0"] for line in status)
        if argv is not None:
            assert pathlib.Path(f"/proc/{pid}/cmdline").read_bytes().split(b"\0")[:-1] == [v.encode() for v in argv]
            assert os.path.samefile(f"/proc/{pid}/exe", argv[0])
        assert not exited(fd)
        return identity
    finally: os.close(fd)

def find(argv):
    found = []
    for path in pathlib.Path("/proc").iterdir():
        if not path.name.isdecimal(): continue
        try:
            if (path / "cmdline").read_bytes().split(b"\0")[:-1] == [v.encode() for v in argv]:
                found.append(capture(int(path.name), argv))
        except (FileNotFoundError, ProcessLookupError): continue
    assert len(found) <= 1, "ambiguous owned process"
    return found[0] if found else None

def wait_exit(fd):
    poll = select.poll(); poll.register(fd, select.POLLIN | select.POLLHUP)
    assert poll.poll(5000), "owned process did not exit"

def terminate(identity):
    fd = pin(identity)
    if fd is not None:
        try:
            try: signal.pidfd_send_signal(fd, signal.SIGKILL)
            except ProcessLookupError: pass
            wait_exit(fd)
        finally: os.close(fd)

dispatcher_argv = [binary, "computer-v2-account-login", "--guest-user", username]
client_argv = ["/usr/bin/xrdp-sesrun", "-t", "Xorg", "-g", "1600x900", "-F", "0", username]
hook_argv = ["/usr/bin/python3", str(directory / "hook.py"), "hold", marker, username, checkpoint]
delayed_argv = ["/usr/bin/python3", str(directory / "hook.py"), "delay", marker, username, checkpoint]

if operation == "setup":
    source = sys.stdin.buffer.read(32769); assert 0 < len(source) <= 32768
    trusted(base); assert read(base / "manifest.json")[1] == 0o600
    assert not os.path.lexists(directory) and not os.path.lexists(stage)
    original, mode = read(pam)
    lines = original.splitlines(keepends=True)
    assert lines[0] == b"# VC Workspace login birth fence v2\n" and b"pam_vcworkspace.so" in lines[1] and b"pam_vcworkspace.so" in lines[2]
    line = ("auth requisite pam_exec.so quiet " + " ".join(hook_argv) + "\n").encode()
    position = 3
    if checkpoint in ("after_auth", "orphan_after_auth"):
        # Debian's real common-auth stack has already verified the password;
        # the xrdp caller has not yet received the PAM result or started Xorg.
        positions = [i for i, text in enumerate(lines) if text.strip() == b"@include common-auth"]
        assert len(positions) == 1 and positions[0] >= 3
        position = positions[0] + 1
    changed = b"".join(lines[:position]) + line + b"".join(lines[position:])
    directory.mkdir(mode=0o700)
    write_json("manifest.json", {"marker": marker, "username": username, "checkpoint": checkpoint, "original": base64.b64encode(original).decode(), "changed": base64.b64encode(changed).decode(), "mode": mode})
    create(directory / "hook.py", source)
    replace_pam(original, changed, mode)
    print('{"stage":"armed"}')
    sys.exit(0)

if operation == "restore" and not directory.exists():
    print('{"stage":"absent"}'); sys.exit(0)
value = read_json("manifest.json")
assert value["marker"] == marker and value["username"] == username and value["checkpoint"] == checkpoint
original = base64.b64decode(value["original"], validate=True)
changed = base64.b64decode(value["changed"], validate=True)

if operation == "delay":
    owner = json.loads(read(account / "account.json")[0])
    write_json("delayed.json", {"process": capture(os.getpid(), delayed_argv), "cgroup": pathlib.Path("/proc/self/cgroup").read_text()})
    os.kill(os.getpid(), signal.SIGSTOP)
    # The acceptance must kill this root descendant before this late setuid.
    os.setgroups([]); os.setgid(owner["uid"]); os.setuid(owner["uid"])
    (pathlib.Path("/home") / username / ("late-login-" + marker)).write_text("escaped old login")
elif operation == "hold":
    if os.environ.get("PAM_USER") != username or os.environ.get("PAM_TYPE") != "auth": sys.exit(0)
    assert os.environ.get("PAM_SERVICE") == "xrdp-sesman"
    fence = json.loads(read(account / "account-lifecycle.json")[0])
    journal = json.loads(read(account / "login-writers.json")[0])
    assert fence["phase"] == "opening" and fence["identity"]["username"] == username
    assert journal["identity"] == fence["identity"] and len(journal["writers"]) == 1
    writer = journal["writers"][0]
    assert journal["schema_version"] == 2 and writer["scope"] is not None
    assert all(writer[key] == fence[key] for key in ("lease_id", "control_epoch", "login_generation", "expires_unix_seconds"))
    creator = capture(os.getppid())
    assert writer["process"] == creator
    group = pathlib.Path("/proc/self/cgroup").read_text()
    assert group.strip() == f'0::/user.slice/user-{fence["identity"]["uid"]}.slice/session-{writer["scope"]["session_id"]}.scope'
    child = os.fork()
    if child == 0: os.execv(delayed_argv[0], delayed_argv)
    deadline = time.monotonic() + 5
    while not (directory / "delayed.json").exists():
        assert time.monotonic() < deadline; time.sleep(0.02)
    delayed = read_json("delayed.json")
    assert delayed["process"]["pid"] == child and delayed["cgroup"] == group
    dispatcher, client = find(dispatcher_argv), find(client_argv)
    assert dispatcher and client and proc(client["pid"])[2] == dispatcher["pid"]
    write_json("held.json", {"dispatcher": dispatcher, "client": client, "creator": creator, "helper": capture(os.getpid(), hook_argv), "delayed": delayed["process"], "identity": fence["identity"]})
    os.kill(os.getpid(), signal.SIGSTOP)
    write_json("resumed.json", {"parent": os.getppid()})
elif operation == "crash":
    if not (directory / "held.json").exists(): print('{"stage":"waiting"}'); sys.exit(0)
    held = read_json("held.json")
    handles = {name: pin(held[name]) for name in ("dispatcher", "client", "creator", "helper", "delayed")}
    assert all(fd is not None for fd in handles.values()) and proc(held["helper"]["pid"])[1] == "T"
    try:
        # The actual fault kills ONLY the QGA Guest dispatcher. Neither its
        # SCP child nor the registered root creator is killed by this fixture.
        signal.pidfd_send_signal(handles["dispatcher"], signal.SIGKILL)
        wait_exit(handles["dispatcher"]); wait_exit(handles["client"])
        assert not exited(handles["creator"]) and not exited(handles["helper"])
        if checkpoint == "orphan_after_auth":
            signal.pidfd_send_signal(handles["creator"], signal.SIGKILL); wait_exit(handles["creator"])
            assert not exited(handles["delayed"]) and not exited(handles["helper"])
        write_json("crashed.json", {"dispatcher_exited": True, "client_exited": True, "creator_alive": checkpoint != "orphan_after_auth"})
        print('{"stage":"crashed"}')
    finally:
        for fd in handles.values(): os.close(fd)
elif operation == "resume":
    assert read_json("crashed.json")["client_exited"]
    held = read_json("held.json")
    for name in ("dispatcher", "client", "creator", "helper", "delayed"):
        fd = pin(held[name])
        if fd is not None: os.close(fd); raise AssertionError("old login domain member remains")
    assert not (directory / "resumed.json").exists()
    assert not (pathlib.Path("/home") / username / ("late-login-" + marker)).exists()
    replace_pam(changed, original, value["mode"])
    print('{"stage":"domain_drained_before_late_setuid"}')
elif operation == "restore":
    # Cleanup is deliberately separate from acceptance observations. Match
    # exact fixture argv even if a failure happened before held.json was saved.
    dispatcher = find(dispatcher_argv)
    if dispatcher: terminate(dispatcher)
    client = find(client_argv)
    if client: terminate(client)
    if (directory / "held.json").exists():
        held = read_json("held.json")
        for name in ("creator", "helper", "delayed"): terminate(held[name])
    helper = find(hook_argv)
    if helper: terminate(helper)
    delayed = find(delayed_argv)
    if delayed: terminate(delayed)
    if os.path.lexists(stage):
        assert read(stage)[0] in (original, changed); stage.unlink()
    current = read(pam)[0]; assert current in (original, changed)
    if current == changed: replace_pam(changed, original, value["mode"])
    allowed = {"manifest.json", "hook.py", "held.json", "crashed.json", "resumed.json", "delayed.json"}
    for path in directory.iterdir():
        assert path.name in allowed; read(path); path.unlink()
    directory.rmdir()
    print('{"stage":"restored"}')
else:
    raise AssertionError("unknown birth fixture operation")

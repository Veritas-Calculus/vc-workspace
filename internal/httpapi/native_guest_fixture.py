"""Explicit isolated Native acceptance ownership; never a shipped Guest API."""
import json
import os
import pathlib
import pwd
import re
import stat
import subprocess
import sys
import time

operation, marker, username, *transport = sys.argv[1:]
if sys.flags.optimize:
    raise SystemExit("Native acceptance refuses disabled ownership assertions")
assert os.geteuid() == 0
assert re.fullmatch(r"svc[0-9a-f]{24}", marker)
assert re.fullmatch(r"vcw[0-9a-f]{12}", username)
assert transport == [] or (operation in ("preconnect", "checkpoint", "connected") and len(transport) == 1 and re.fullmatch(r"[1-9]", transport[0]))
base = pathlib.Path("/run") / ("vc-workspace-" + marker)
record = base / "native-account.json"
account = pathlib.Path("/var/lib/vc-workspace/computer-v2/users") / username
home = pathlib.Path("/home") / username
agent = "/usr/local/sbin/vc-workspace-guest-agent"

def read(path):
    for parent in path.parents:
        meta = parent.lstat()
        assert stat.S_ISDIR(meta.st_mode) and meta.st_uid == 0 and not meta.st_mode & 0o022
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as stream:
        meta = os.fstat(stream.fileno())
        assert stat.S_ISREG(meta.st_mode) and meta.st_uid == 0 and meta.st_nlink == 1 and not meta.st_mode & 0o022 and meta.st_size <= 65536
        return json.load(stream)

def create(path, value):
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "w") as stream:
        json.dump(value, stream); stream.flush(); os.fsync(stream.fileno())
    fd = os.open(base, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try: os.fsync(fd)
    finally: os.close(fd)

def run(*args, check=True):
    return subprocess.run(args, check=check, capture_output=True, text=True, timeout=35)

manifest = read(base / "manifest.json")
assert manifest["marker"] == marker and manifest["native"]
if operation == "reserve":
    assert not os.path.lexists(home) and not os.path.lexists(account)
    assert run("getent", "passwd", username, check=False).returncode == 2
    create(record, {"username": username})
    print("reserved absent acceptance identity")
    sys.exit(0)

value = read(record)
assert value["username"] == username
if operation == "preconnect":
    assert transport == ["1"]
    assert not os.path.lexists(account) and not os.path.lexists(home)
    assert run("getent", "passwd", username, check=False).returncode == 2
    with os.fdopen(os.open("/var/log/xrdp.log", os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as stream:
        meta = os.fstat(stream.fileno())
        daemon = pwd.getpwnam("xrdp")
        assert daemon.pw_uid < 1000
        assert stat.S_ISREG(meta.st_mode) and meta.st_uid in (0, daemon.pw_uid) and meta.st_nlink == 1 and not meta.st_mode & 0o022 and meta.st_size < 64 * 1024 * 1024
        create(base / "transport-1.json", {"device": meta.st_dev, "inode": meta.st_ino, "offset": meta.st_size})
    print("recorded log cursor before default Broker creates the absent OS account")
    sys.exit(0)
owned = read(account / "account.json")
assert owned["username"] == username and owned["uid"] >= 1000
try: user = pwd.getpwnam(username)
except KeyError:
    assert operation == "remove" and not os.path.lexists(home)
    user = None
uid = owned["uid"]
assert user is None or (user.pw_uid == uid and user.pw_dir == str(home))
binding = base / "native-binding.json"
if operation == "bind":
    if os.path.lexists(binding): assert read(binding) == owned
    else: create(binding, owned)
    print("bound exact acceptance UID")
    sys.exit(0)
assert read(binding) == owned

if operation in ("checkpoint", "connected"):
    assert transport
    checkpoint = base / ("transport-" + transport[0] + ".json")
    with os.fdopen(os.open("/var/log/xrdp.log", os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as stream:
        meta = os.fstat(stream.fileno())
        daemon = pwd.getpwnam("xrdp")
        assert daemon.pw_uid < 1000 and daemon.pw_uid != uid
        assert stat.S_ISREG(meta.st_mode) and meta.st_uid in (0, daemon.pw_uid) and meta.st_nlink == 1 and not meta.st_mode & 0o022 and meta.st_size < 64 * 1024 * 1024
        if operation == "checkpoint":
            create(checkpoint, {"device": meta.st_dev, "inode": meta.st_ino, "offset": meta.st_size})
            print("recorded pre-connect log cursor")
        else:
            previous = read(checkpoint)
            assert meta.st_dev == previous["device"] and meta.st_ino == previous["inode"]
            assert 0 <= meta.st_size - previous["offset"] <= 262144
            stream.seek(previous["offset"])
            raw = stream.read(262145).decode("utf-8")
            assert len(raw) <= 262144
            peers = re.findall(r"xrdp_pid=(\d+) connected to Xorg_pid=(\d+) Xorg_uid=" + str(user.pw_uid) + r"\b", raw)
            assert len(peers) == 1, "await exact fresh RDP/Xorg connection"
            rdp, xorg = map(int, peers[0])
            assert pathlib.Path("/proc", str(xorg)).stat().st_uid == user.pw_uid
            assert pathlib.Path("/proc", str(rdp), "comm").read_text().strip() == "xrdp"
            arguments = pathlib.Path("/proc", str(xorg), "cmdline").read_bytes().split(b"\0")
            assert len(arguments) > 2 and re.fullmatch(rb":(?:[1-9][0-9]{0,3})", arguments[1])
            display = arguments[1][1:].decode()
            lock = pathlib.Path("/tmp/.X" + display + "-lock")
            socket = pathlib.Path("/tmp/.X11-unix/X" + display)
            lm, sm = lock.lstat(), socket.lstat()
            assert stat.S_ISREG(lm.st_mode) and lm.st_uid == uid and lm.st_nlink == 1 and int(lock.read_text().strip()) == xorg
            assert stat.S_ISSOCK(sm.st_mode) and sm.st_uid == uid and sm.st_nlink == 1
            proof = {"pid": xorg, "display": display, "lock_inode": lm.st_ino, "socket_inode": sm.st_ino, "device": lm.st_dev}
            proof["ticks"] = pathlib.Path("/proc", str(xorg), "stat").read_text().rsplit(") ", 1)[1].split()[19]
            proof["boot"] = pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip()
            path = base / ("xorg-" + str(xorg) + ".json")
            if os.path.lexists(path): assert read(path) == proof
            else: create(path, proof)
            print(json.dumps({"rdp_pid": rdp, "xorg_pid": xorg, "uid": user.pw_uid}))
elif operation in ("nodes-absent", "freeze-desktop"):
    records = [read(p) for p in base.iterdir() if re.fullmatch(r"xorg-[1-9][0-9]*\.json", p.name)]
    assert records
    if operation == "nodes-absent":
        for proof in records:
            assert not os.path.lexists("/tmp/.X" + proof["display"] + "-lock"), "product left an Xorg lock"
            assert not os.path.lexists("/tmp/.X11-unix/X" + proof["display"]), "product left an Xorg socket"
        print("all captured Xorg nodes gone without fixture cleanup")
    else:
        import signal
        live = [r for r in records if pathlib.Path("/proc", str(r["pid"])).exists()]
        assert len(live) == 1
        proof = live[0]
        assert proof["boot"] == pathlib.Path("/proc/sys/kernel/random/boot_id").read_text().strip()
        xorg_fd = os.pidfd_open(proof["pid"])
        proc = pathlib.Path("/proc", str(proof["pid"]))
        assert proc.stat().st_uid == uid and proc.joinpath("stat").read_text().rsplit(") ", 1)[1].split()[19] == proof["ticks"]
        group = proc.joinpath("cgroup").read_text().strip()
        journal = read(account / "login-writers.json")
        assert journal["identity"] == {"username": username, "uid": uid, "sid": ""}
        creators = [w for w in journal["writers"] if w["scope"] is not None and group == "0::/user.slice/user-" + str(uid) + ".slice/session-" + w["scope"]["session_id"] + ".scope"]
        assert len(creators) == 1
        parent = creators[0]["process"]
        assert parent["boot_id"] == proof["boot"]
        parent_fd = os.pidfd_open(parent["pid"])
        parent_proc = pathlib.Path("/proc", str(parent["pid"]))
        assert parent_proc.stat().st_uid == 0 and parent_proc.joinpath("comm").read_text().strip() == "xrdp-sesexec"
        assert int(parent_proc.joinpath("stat").read_text().rsplit(") ", 1)[1].split()[19]) == parent["start_ticks"]
        try:
            signal.pidfd_send_signal(xorg_fd, signal.SIGSTOP)
            until = time.monotonic() + 3
            while not proc.joinpath("status").read_text().split("State:", 1)[1].lstrip().startswith("T"):
                assert time.monotonic() < until
                time.sleep(0.02)
            # Keep the actual creator alive: killing it can resume/terminate an
            # orphaned stopped process group before the expiry worker runs.
            # A stopped Xorg cannot handle the creator's graceful SIGTERM;
            # the shipped worker must close the scope and remove its nodes.
        finally: os.close(parent_fd); os.close(xorg_fd)
        print("exact Xorg frozen with its registered creator; expiry must force-close and clean nodes")
elif operation == "desktop":
    pids = run("pgrep", "-u", str(user.pw_uid), "-x", "xfce4-session").stdout.split()
    assert len(pids) == 1
    pid = int(pids[0])
    proc = pathlib.Path("/proc") / str(pid)
    assert proc.stat().st_uid == user.pw_uid
    ticks = (proc / "stat").read_text().rsplit(") ", 1)[1].split()[19]
    journal = read(account / "login-writers.json")
    print(json.dumps({"uid": user.pw_uid, "pid": pid, "ticks": ticks,
                      "journal": journal}))
elif operation == "remove":
    observation = json.loads(run(agent, "computer-v2-native-account-inspect", "--guest-user", username).stdout)
    assert observation["identity"] == {"username": username, "uid": uid, "sid": ""}
    assert observation["account"]["disabled"] and observation["processes_absent"] and observation["login_writers_absent"] is True
    assert observation["lifecycle"]["phase"] == "revoked"
    # Clear only failure metadata for this now-empty UID. Otherwise a later
    # isolated test reusing the UID fails its conservative user@ preflight.
    for name in ("user@" + str(uid) + ".service", "user-" + str(uid) + ".slice", "user-runtime-dir@" + str(uid) + ".service"):
        properties = dict(line.split("=", 1) for line in run("systemctl", "show", name, "--property=ActiveState,Job").stdout.splitlines() if "=" in line)
        assert properties["ActiveState"] in ("inactive", "failed") and properties["Job"] in ("", "0")
        if properties["ActiveState"] == "failed": run("systemctl", "reset-failed", name)
    # Forced process-domain closure can leave Xorg filesystem sockets. Only
    # remove exact nodes captured while this fixture's Xorg was still alive.
    xorg_records = [p for p in base.iterdir() if re.fullmatch(r"xorg-[1-9][0-9]*\.json", p.name)]
    for path in xorg_records:
        proof = read(path)
        assert not pathlib.Path("/proc", str(proof["pid"])).exists()
        assert re.fullmatch(r"[1-9][0-9]{0,3}", proof["display"])
        for prefix, suffix, key, is_type in (("/tmp/.X", "-lock", "lock_inode", stat.S_ISREG), ("/tmp/.X11-unix/X", "", "socket_inode", stat.S_ISSOCK)):
            node = pathlib.Path(prefix + proof["display"] + suffix)
            if os.path.lexists(node):
                meta = node.lstat()
                assert is_type(meta.st_mode) and meta.st_uid == uid and meta.st_nlink == 1 and meta.st_ino == proof[key] and meta.st_dev == proof["device"]
                if key == "lock_inode": assert int(node.read_text().strip()) == proof["pid"]
                node.unlink()
        path.unlink()
    # Never delete an active account, follow a Home link, or sweep a prefix.
    if user: assert home.lstat().st_uid == uid and stat.S_ISDIR(home.lstat().st_mode)
    allowed = {"account.json", "account.lock", "login-writers.json", "login-writers.lock", "native-account-lifecycle.json", "runtime"}
    for path in account.iterdir():
        assert path.name in allowed
        if path.name == "runtime":
            meta = path.lstat()
            assert stat.S_ISDIR(meta.st_mode) and meta.st_uid == uid and stat.S_IMODE(meta.st_mode) == 0o700
            for child in path.iterdir():
                child_meta = child.lstat()
                assert child_meta.st_uid == uid and child_meta.st_nlink == 1
                assert (child.name == "helper.lock" and stat.S_ISREG(child_meta.st_mode)) or (child.name == "helper.sock" and stat.S_ISSOCK(child_meta.st_mode))
        else:
            assert stat.S_ISREG(path.lstat().st_mode) and path.lstat().st_uid == 0 and path.lstat().st_nlink == 1
    if user: run("userdel", "-r", username)
    for path in account.iterdir():
        if path.name == "runtime":
            for child in path.iterdir(): child.unlink()
            path.rmdir()
        else: path.unlink()
    account.rmdir()
    assert not os.path.lexists(home) and run("getent", "passwd", username, check=False).returncode == 2
    binding.unlink(); record.unlink()
    for index in range(1, 10):
        path = base / ("transport-" + str(index) + ".json")
        if os.path.lexists(path): read(path); path.unlink()
    print("removed exact closed acceptance account and Home")
else:
    raise AssertionError("unknown Native fixture operation")

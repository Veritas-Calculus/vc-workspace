"""Private opt-in PVE test fixture; never shipped as a Guest command/API.

Installs only absent canonical units. A root-private recovery manifest precedes
external writes, so a lost setup receipt still has an exact cleanup target.
"""
import base64
import json
import os
import pathlib
import re
import stat
import subprocess
import sys

operation, marker, *mode = sys.argv[1:]
assert os.geteuid() == 0 and re.fullmatch(r"svc[0-9a-f]{24}", marker)
assert mode in ([], ["cold-boot"], ["native"])
cold_boot = mode == ["cold-boot"]
native = mode == ["native"]
state = pathlib.Path("/var/lib/vc-workspace")
base = state / ("acceptance-" + marker) if cold_boot else pathlib.Path("/run") / ("vc-workspace-" + marker)
names = ("vc-workspace-agent.service", "vc-workspace-accounts.service", "vc-workspace-accounts.timer")
paths = {name: pathlib.Path("/etc/systemd/system") / name for name in names}
enable_links = {
    pathlib.Path("/etc/systemd/system/multi-user.target.wants/vc-workspace-agent.service"): paths[names[0]],
    pathlib.Path("/etc/systemd/system/timers.target.wants/vc-workspace-accounts.timer"): paths[names[2]],
}
fault_dir = pathlib.Path("/run/systemd/system/vc-workspace-accounts.service.d")
fault = fault_dir / "90-acceptance-readonly.conf"
fault_text = b"[Service]\nReadWritePaths=\nReadWritePaths=/var/lib/vc-workspace/computer-v2\n"
pam = pathlib.Path("/etc/pam.d/xrdp-sesman")
pam_backup = pathlib.Path("/etc/pam.d/xrdp-sesman.vc-workspace-before-birth")
native_pam_backup = pathlib.Path("/etc/pam.d/xrdp-sesman.vc-workspace-before-native")
xrdp_units = ("xrdp.service", "xrdp-sesman.service")

def run(*args, check=True):
    return subprocess.run(args, check=check, capture_output=True, text=True, timeout=55)

def read(path, limit=65536):
    root_directory(path.parent)
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as stream:
        meta = os.fstat(stream.fileno())
        assert stat.S_ISREG(meta.st_mode) and meta.st_uid == 0 and meta.st_gid == 0 and meta.st_nlink == 1 and not meta.st_mode & 0o022 and meta.st_size <= limit
        raw = stream.read(limit+1); assert len(raw) <= limit
        return raw, stat.S_IMODE(meta.st_mode)

def create(path, raw, mode):
    root_directory(path.parent)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    with os.fdopen(fd, "wb") as stream:
        stream.write(raw); stream.flush(); os.fsync(stream.fileno())
    os.chmod(path, mode)
    directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try: os.fsync(directory)
    finally: os.close(directory)

def root_directory(path):
    for parent in (path, *path.parents):
        meta = os.lstat(parent)
        assert stat.S_ISDIR(meta.st_mode) and meta.st_uid == 0 and not meta.st_mode & 0o022

def manifest():
    raw, mode = read(base / "manifest.json")
    assert mode == 0o600
    value = json.loads(raw)
    assert value["marker"] == marker and set(value["units"]) == set(names)
    assert value.get("cold_boot", False) == cold_boot
    assert value.get("native", False) == native
    return value

if operation == "setup":
    request = json.load(sys.stdin)
    assert set(request) == {"units", "login_fence_installer"}
    payload = request["units"]
    installer = request["login_fence_installer"]
    assert isinstance(installer, str) and len(installer) < 8192
    assert set(payload) == set(names) and all(isinstance(v, str) and len(v) < 8192 for v in payload.values())
    for name, path in paths.items():
        assert not os.path.lexists(path)
        assert run("systemctl", "show", name, "--property=LoadState", "--value", check=False).stdout.strip() == "not-found"
        for parent in (pathlib.Path("/etc/systemd/system"), pathlib.Path("/run/systemd/system")):
            assert not os.path.lexists(parent / (name + ".d"))
    assert not os.path.lexists(base)
    assert not os.path.lexists(pam_backup)
    if native: assert not os.path.lexists(native_pam_backup)
    if cold_boot:
        root_directory(state)
        for link in enable_links:
            root_directory(link.parent)
            assert not os.path.lexists(link), "existing boot enablement is not owned"
    original_pam, pam_mode = read(pam)
    assert b"computer-v2-pam-register" not in original_pam
    prefix = run("/usr/local/sbin/vc-workspace-guest-agent", "computer-v2-pam-policy").stdout.encode()
    assert prefix.startswith(b"# VC Workspace login birth fence v2\n") and len(prefix) < 1024
    native_prefix = b""
    xrdp_active = []
    if native:
        native_prefix = run("/usr/local/sbin/vc-workspace-guest-agent", "computer-v2-native-pam-policy").stdout.encode()
        assert native_prefix.startswith(b"# VC Workspace Native login birth fence v1\n") and len(native_prefix) < 1024
        for name in xrdp_units:
            active = run("systemctl", "show", name, "--property=ActiveState", "--value").stdout.strip()
            assert active in ("active", "inactive"), "unstable preexisting xrdp service"
            if active == "active": xrdp_active.append(name)
    backups = {}
    for name in ("agent-state.json", "desktop-ready"):
        try:
            raw, mode = read(state / name)
            backups[name] = {"data": base64.b64encode(raw).decode(), "mode": mode}
        except FileNotFoundError:
            backups[name] = None
    assert not list(state.glob("agent-state.*.tmp")) and not list(state.glob("desktop-ready.*.tmp"))
    base.mkdir(mode=0o700)
    value = {"marker": marker, "units": payload, "backups": backups, "cold_boot": cold_boot, "native": native,
             "xrdp_active": xrdp_active, "native_prefix": base64.b64encode(native_prefix).decode(),
             "pam": {"data": base64.b64encode(original_pam).decode(), "mode": pam_mode, "prefix": base64.b64encode(prefix).decode()}}
    create(base / "manifest.json", json.dumps(value).encode(), 0o600)
    if native: run("systemctl", "stop", *xrdp_units)
    for name, path in paths.items(): create(path, payload[name].encode(), 0o644)
    run("/usr/bin/python3", "-c", installer)
    if native:
        run("/usr/bin/python3", "-c", installer, "--native")
        if xrdp_active: run("systemctl", "start", *xrdp_active)
    run("systemctl", "daemon-reload")
    run("systemd-analyze", "verify", *map(str, paths.values()))
    if cold_boot:
        run("systemctl", "enable", names[0], names[2])
        for link, target in enable_links.items():
            assert link.is_symlink() and os.readlink(link) == str(target)
    run("systemctl", "start", "vc-workspace-agent.service")
    print("installed exact canonical units")
elif operation in ("bind", "bind-next"):
    assert cold_boot
    manifest()
    request = json.load(sys.stdin)
    assert set(request) == {"account"}
    lease = request["account"]
    assert set(lease) == {"schema_version", "identity", "lease_id", "control_epoch", "login_generation", "expires_unix_seconds", "phase"} and lease["phase"] == ""
    identity = lease["identity"]
    assert set(identity) == {"username", "uid", "sid"} and identity["sid"] == "" and identity["uid"] >= 1000
    assert re.fullmatch(r"vca[0-9a-f]{12}", identity["username"])
    assert lease["schema_version"] == 1 and lease["control_epoch"] > 0 and lease["login_generation"] > 0
    account = state / "computer-v2/users" / identity["username"] / "account.json"
    assert json.loads(read(account)[0]) == {"username": identity["username"], "uid": identity["uid"]}
    record = "owned-login.json"
    if operation == "bind-next":
        original = json.loads(read(base / record)[0])
        assert identity == original["identity"]
        assert lease["lease_id"] == original["lease_id"] + "_next"
        assert lease["control_epoch"] == original["control_epoch"] + 1
        assert lease["login_generation"] == original["login_generation"] + 1
        closed = dict(original, phase="revoked", expires_unix_seconds=0)
        assert json.loads(read(account.parent / "account-lifecycle.json")[0]) == closed
        record = "owned-login-next.json"
    create(base / record, json.dumps(lease).encode(), 0o600)
    print("persisted exact cold-boot login identity")
elif operation in ("stop", "restore", "readonly", "repair"):
    if operation in ("stop", "restore") and not base.exists():
        print("fixture absent"); sys.exit(0)
    value = manifest()
    for name, path in paths.items():
        if path.exists(): assert read(path)[0] == value["units"][name].encode(), "unit changed outside fixture"
    if operation in ("readonly", "repair"):
        # This strictly reduces write access to reproduce the original bug.
        if operation == "readonly":
            fault_dir.mkdir(mode=0o755)
            create(fault, fault_text, 0o644)
        else:
            assert read(fault)[0] == fault_text
            fault.unlink(); fault_dir.rmdir()
        run("systemctl", "daemon-reload")
        print(operation)
    elif operation == "stop":
        for name in ("vc-workspace-accounts.timer", "vc-workspace-agent.service", "vc-workspace-accounts.service"):
            run("systemctl", "stop", name)
        print("services stopped")
    else:
        for name in names:
            assert run("systemctl", "show", name, "--property=ActiveState", "--value").stdout.strip() in ("inactive", "failed")
        if cold_boot:
            for link, target in enable_links.items():
                if os.path.lexists(link):
                    root_directory(link.parent)
                    assert link.is_symlink() and os.readlink(link) == str(target), "boot link changed outside fixture"
                    link.unlink()
            for record in ("owned-login-next.json", "owned-login.json"):
                owned = base / record
                if owned.exists():
                    identity = json.loads(read(owned)[0])["identity"]
                    assert run("getent", "-s", "files", "passwd", identity["username"], check=False).returncode == 2
                    assert not os.path.lexists(state / "computer-v2/users" / identity["username"])
                    owned.unlink()
        if fault.exists():
            assert read(fault)[0] == fault_text
            fault.unlink(); fault_dir.rmdir()
        for name, path in paths.items():
            if path.exists(): path.unlink()
        run("systemctl", "daemon-reload")
        for name in names: run("systemctl", "reset-failed", name, check=False)
        original_pam = base64.b64decode(value["pam"]["data"], validate=True)
        prefix = base64.b64decode(value["pam"]["prefix"], validate=True)
        native_prefix = base64.b64decode(value.get("native_prefix", ""), validate=True)
        assert read(pam)[0] in (original_pam, prefix + original_pam, prefix + native_prefix + original_pam), "PAM stack changed outside fixture"
        if native:
            for process in ("Xorg", "xrdp-sesexec"):
                assert run("pgrep", "-x", process, check=False).returncode == 1, "live login prevents PAM restoration"
            run("systemctl", "stop", *xrdp_units)
            if os.path.lexists(native_pam_backup):
                assert read(native_pam_backup)[0] == prefix + original_pam
                native_pam_backup.unlink()
        if os.path.lexists(pam_backup):
            assert read(pam_backup)[0] == original_pam
            pam_backup.unlink()
        pam.unlink(); create(pam, original_pam, value["pam"]["mode"])
        if native and value["xrdp_active"]: run("systemctl", "start", *value["xrdp_active"])
        for name, backup in value["backups"].items():
            path = state / name
            if path.exists(): read(path); path.unlink()
            if backup is not None: create(path, base64.b64decode(backup["data"], validate=True), backup["mode"])
        for pattern in ("agent-state.*.tmp", "desktop-ready.*.tmp"):
            for path in state.glob(pattern):
                assert re.fullmatch(r"(?:agent-state|desktop-ready)\.[0-9]+\.tmp", path.name)
                read(path); path.unlink()
        (base / "manifest.json").unlink(); base.rmdir()
        print("restored units and prior readiness state")
else:
    raise AssertionError("unknown fixture operation")

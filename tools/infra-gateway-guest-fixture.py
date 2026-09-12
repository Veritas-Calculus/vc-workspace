"""Guarded configuration setup/restore for the explicitly idle VM160 lab.

The caller enforces the PVE boundary. UID_MIN may be raised temporarily to avoid
retired lab UID bindings; no account is created here. Only idle xrdp is restarted
after exact, guarded restoration.
"""
import base64
import json
import os
import pathlib
import re
import stat
import subprocess
import sys

assert not sys.flags.optimize and os.geteuid() == 0
operation, marker, *arguments = sys.argv[1:]
assert re.fullmatch(r"svc[0-9a-f]{24}", marker)
assert not arguments or (operation == "uid-floor" and len(arguments) == 1)
root = pathlib.Path("/run/vc-workspace-infra-" + marker)
paths = [pathlib.Path(p) for p in (
    "/etc/xrdp/xrdp.ini", "/etc/xrdp/sesman.ini", "/etc/vc-workspace/background-policy",
    "/usr/local/bin/vc-workspace-apply-background", "/etc/xdg/autostart/vc-workspace-background.desktop",
    "/usr/share/backgrounds/vc-workspace/desktop-background-session.jpg",
    "/etc/login.defs",
)]

def snapshot():
    result = {}
    for path in paths:
        for parent in path.parents:
            meta = parent.lstat()
            assert stat.S_ISDIR(meta.st_mode) and meta.st_uid == 0 and not meta.st_mode & 0o022
        if not path.exists():
            assert not path.is_symlink()
            result[str(path)] = None
            continue
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(fd, "rb") as stream:
            meta = os.fstat(stream.fileno())
            assert stat.S_ISREG(meta.st_mode) and meta.st_uid == 0 and meta.st_nlink == 1 and not meta.st_mode & 0o022 and meta.st_size < 2 * 1024 * 1024
            result[str(path)] = dict(data=base64.b64encode(stream.read()).decode(), mode=stat.S_IMODE(meta.st_mode), uid=meta.st_uid, gid=meta.st_gid)
    return result

def save(name, value):
    with os.fdopen(os.open(root / name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "w") as stream:
        json.dump(value, stream); stream.flush(); os.fsync(stream.fileno())

def read(name):
    meta = root.lstat()
    assert stat.S_ISDIR(meta.st_mode) and meta.st_uid == 0 and stat.S_IMODE(meta.st_mode) == 0o700
    with os.fdopen(os.open(root / name, os.O_RDONLY | os.O_NOFOLLOW), "r") as stream:
        meta = os.fstat(stream.fileno())
        assert stat.S_ISREG(meta.st_mode) and meta.st_uid == 0 and stat.S_IMODE(meta.st_mode) == 0o600 and meta.st_nlink == 1
        return json.load(stream)

if operation == "backup":
    assert not os.path.lexists(root)
    for process in ("Xorg", "xrdp-sesexec"):
        assert subprocess.run(["pgrep", "-x", process], capture_output=True).returncode == 1
    before = snapshot()
    root.mkdir(mode=0o700)
    save("before.json", before)
    print("configuration baseline saved")
elif operation == "verify":
    expected = read("prepared.json") if (root / "prepared.json").exists() else read("before.json")
    assert snapshot() == expected, "configuration baseline has changed"
    print("configuration baseline unchanged")
elif operation == "uid-floor":
    # Deleted lab users retain immutable database identity bindings. Keep the
    # next *new* account above them, never erase bindings or reuse their UID.
    floor = int(arguments[0])
    assert 1001 <= floor <= 60000
    before = read("before.json")
    assert snapshot() == before
    for process in ("Xorg", "xrdp-sesexec"):
        assert subprocess.run(["pgrep", "-x", process], capture_output=True).returncode == 1
    path = pathlib.Path("/etc/login.defs")
    original = before[str(path)]
    raw = base64.b64decode(original["data"], validate=True)
    matches = re.findall(rb"(?m)^UID_MIN[ \t]+([0-9]+)[ \t]*$", raw)
    assert len(matches) == 1 and 1000 <= int(matches[0]) < floor
    prepared = dict(before)
    desired = re.sub(rb"(?m)^UID_MIN[ \t]+[0-9]+[ \t]*$", b"UID_MIN\t" + str(floor).encode(), raw)
    prepared[str(path)] = dict(original, data=base64.b64encode(desired).decode())
    save("prepared.json", prepared)  # recovery intent precedes the only write
    temporary = path.with_name(path.name + ".prepare-" + marker)
    with os.fdopen(os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, original["mode"]), "wb") as stream:
        stream.write(desired); stream.flush(); os.fsync(stream.fileno())
        os.fchmod(stream.fileno(), original["mode"]); os.fchown(stream.fileno(), original["uid"], original["gid"])
    os.replace(temporary, path)
    assert snapshot() == prepared
    print("temporary UID allocation floor avoids retained database identities")
elif operation == "seal":
    before = read("before.json")
    assert set(before) == set(map(str, paths))
    save("applied.json", snapshot())
    print("observed post-Broker configuration saved")
elif operation == "restore":
    before = read("before.json")
    assert set(before) == set(map(str, paths))
    now = snapshot()
    prepared = read("prepared.json") if (root / "prepared.json").exists() else before
    expected = read("applied.json") if (root / "applied.json").exists() else prepared
    assert now == expected or now == before, "configuration changed outside the recorded lab"
    for process in ("Xorg", "xrdp-sesexec"):
        assert subprocess.run(["pgrep", "-x", process], capture_output=True).returncode == 1
    for path in paths:
        original = before[str(path)]
        if original == now[str(path)]: continue
        if original is None:
            path.unlink()
        else:
            temporary = path.with_name(path.name + ".restore-" + marker)
            fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, original["mode"])
            with os.fdopen(fd, "wb") as stream:
                stream.write(base64.b64decode(original["data"], validate=True)); stream.flush(); os.fsync(stream.fileno())
                os.fchmod(stream.fileno(), original["mode"]); os.fchown(stream.fileno(), original["uid"], original["gid"])
            os.replace(temporary, path)
    assert snapshot() == before
    subprocess.run(["systemctl", "restart", "xrdp-sesman.service", "xrdp.service"], check=True, capture_output=True, timeout=30)
    for name in ("applied.json", "prepared.json", "before.json"):
        if (root / name).exists(): (root / name).unlink()
    root.rmdir()
    print("exact configuration baseline restored")
else:
    raise AssertionError("unknown configuration fixture operation")

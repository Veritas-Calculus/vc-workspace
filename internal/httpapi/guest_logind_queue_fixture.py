"""Isolated acceptance: observe a real queued PAM call while logind is paused.

No Guest test flag, synthetic reply, global D-Bus policy change or numeric kill.
A separate pidfd guardian resumes only the original daemon if the observer dies.
"""
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
import time

operation, marker, username, number = sys.argv[1:]
assert os.geteuid() == 0 and re.fullmatch(r"svc[0-9a-f]{24}", marker)
assert re.fullmatch(r"vca[0-9a-f]{12}", username)
uid = int(number); assert 1000 <= uid < 2**32-1 and str(uid) == number
base = Path("/run") / ("vc-workspace-" + marker)
directory = base / "queue"
source_path = directory / "observer.py"
account = Path("/var/lib/vc-workspace/computer-v2/users") / username
unit = "systemd-logind.service"

def run(*args, check=True):
    return subprocess.run(args, check=check, capture_output=True, text=True, timeout=10)

def trusted(path):
    for parent in (path, *path.parents):
        m = parent.lstat()
        assert stat.S_ISDIR(m.st_mode) and m.st_uid == 0 and not m.st_mode & 0o022

def read(path):
    trusted(path.parent)
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as stream:
        m = os.fstat(stream.fileno())
        assert stat.S_ISREG(m.st_mode) and m.st_uid == 0 and m.st_nlink == 1 and stat.S_IMODE(m.st_mode) == 0o600 and m.st_size <= 32768
        raw = stream.read(32769); assert len(raw) <= 32768
        return raw

def create(path, value):
    trusted(path.parent)
    with os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "wb") as stream:
        stream.write(value); stream.flush(); os.fsync(stream.fileno())

def record(name, value): create(directory / name, json.dumps(value).encode())

def identity(pid):
    head, tail = Path(f"/proc/{pid}/stat").read_text().rsplit(") ", 1)
    assert head.startswith(f"{pid} (")
    return {"pid": pid, "start_ticks": int(tail.split()[19]), "boot_id": Path("/proc/sys/kernel/random/boot_id").read_text().strip()}

def exited(fd):
    p = select.poll(); p.register(fd, select.POLLIN | select.POLLHUP)
    return bool(p.poll(0))

def pin(value):
    assert set(value) == {"pid", "start_ticks", "boot_id"} and value["pid"] > 1 and value["start_ticks"] > 0
    if value["boot_id"] != Path("/proc/sys/kernel/random/boot_id").read_text().strip(): return None
    try: fd = os.pidfd_open(value["pid"])
    except ProcessLookupError: return None
    try:
        if exited(fd) or identity(value["pid"]) != value or exited(fd): os.close(fd); return None
        assert any(line.split() == ["Uid:", "0", "0", "0", "0"] for line in Path(f'/proc/{value["pid"]}/status').read_text().splitlines())
        return fd
    except FileNotFoundError:
        assert exited(fd); os.close(fd); return None

def process_args(op): return ["/usr/bin/python3", str(source_path), op, marker, username, number]

def pin_observer(value, op):
    fd = pin(value)
    if fd is None: return None
    try:
        assert Path(f'/proc/{value["pid"]}/cmdline').read_bytes().split(b"\0")[:-1] == [x.encode() for x in process_args(op)]
        assert os.path.samefile(f'/proc/{value["pid"]}/exe', "/usr/bin/python3") and not exited(fd)
        return fd
    except BaseException: os.close(fd); raise

def property(name): return run("systemctl", "show", unit, "--property=" + name, "--value").stdout.strip()

def stopped(pid):
    return Path(f"/proc/{pid}/stat").read_text().rsplit(") ", 1)[1].split()[0] == "T"

def wait(condition, seconds=5):
    deadline = time.monotonic() + seconds
    while not condition():
        assert time.monotonic() < deadline, "bounded queue fixture observation timed out"
        time.sleep(0.02)

def resume(fd):
    if not exited(fd):
        try: signal.pidfd_send_signal(fd, signal.SIGCONT)
        except ProcessLookupError: pass

assert json.loads(read(account / "account.json")) == {"username": username, "uid": uid}
assert run("getent", "-s", "files", "passwd", username).stdout.split(":")[2] == number
if operation == "setup":
    source = sys.stdin.buffer.read(32769); assert 0 < len(source) <= 32768
    trusted(base); assert read(base / "manifest.json")
    assert not os.path.lexists(directory) and not run("loginctl", "list-sessions", "--no-legend").stdout.strip()
    assert property("ActiveState") == "active" and property("SubState") == "running" and property("Restart") == "always"
    daemon = identity(int(property("MainPID")))
    assert not stopped(daemon["pid"])
    executable = str(Path(f'/proc/{daemon["pid"]}/exe').resolve())
    assert executable in ("/usr/lib/systemd/systemd-logind", "/lib/systemd/systemd-logind")
    directory.mkdir(mode=0o700)
    record("manifest.json", {"marker": marker, "uid": uid, "username": username, "daemon": daemon,
           "executable": executable, "invocation": property("InvocationID"), "source_sha256": hashlib.sha256(source).hexdigest()})
    create(source_path, source)
    create(directory / "errors", b"")
    with open(directory / "errors", "ab", buffering=0) as errors:
        subprocess.Popen(process_args("observe"), stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=errors, close_fds=True, start_new_session=True)
    wait(lambda: (directory / "armed.json").exists(), 8)
    print('{"stage":"armed"}'); sys.exit(0)

if operation == "restore" and not directory.exists():
    print('{"stage":"absent"}'); sys.exit(0)
manifest = json.loads(read(directory / "manifest.json"))
assert (manifest["marker"], manifest["uid"], manifest["username"]) == (marker, uid, username)
assert hashlib.sha256(read(source_path)).hexdigest() == manifest["source_sha256"]

def daemon_fd():
    fd = pin(manifest["daemon"])
    if fd is not None:
        assert os.path.samefile(f'/proc/{manifest["daemon"]["pid"]}/exe', manifest["executable"])
    return fd

if operation == "guardian":
    daemon = daemon_fd(); assert daemon is not None
    observer = pin_observer(json.loads(read(directory / "observer.json")), "observe"); assert observer is not None
    try:
        record("guardian.json", identity(os.getpid()))
        p = select.poll(); p.register(observer, select.POLLIN | select.POLLHUP)
        p.poll(40000)
    finally:
        resume(daemon); os.close(daemon); os.close(observer)
    sys.exit(0)

if operation == "observe":
    daemon = daemon_fd(); assert daemon is not None
    record("observer.json", identity(os.getpid()))
    with open(directory / "errors", "ab", buffering=0) as errors:
        subprocess.Popen(process_args("guardian"), stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=errors, close_fds=True, start_new_session=True)
    wait(lambda: (directory / "guardian.json").exists())
    from gi.repository import Gio, GLib
    loop = GLib.MainLoop()
    signal.signal(signal.SIGTERM, lambda *_: loop.quit())
    auxiliary = Gio.bus_get_sync(Gio.BusType.SYSTEM, None)
    address = Gio.dbus_address_get_for_bus_sync(Gio.BusType.SYSTEM, None)
    monitor = Gio.DBusConnection.new_for_address_sync(address, Gio.DBusConnectionFlags.AUTHENTICATION_CLIENT | Gio.DBusConnectionFlags.MESSAGE_BUS_CONNECTION, None, None)
    seen = {}
    def inspect_message(message):
        try:
            if message.get_message_type() == Gio.DBusMessageType.METHOD_CALL and message.get_member() == "CreateSessionWithPIDFD":
                body = message.get_body().unpack()
                if body[0] != uid: return False
                assert not seen and body[2:6] == ("xrdp-sesman", "x11", "user", "XFCE")
                assert stopped(manifest["daemon"]["pid"])
                journal = json.loads(read(account / "login-writers.json"))
                assert journal["schema_version"] == 2 and len(journal["writers"]) == 1 and journal["writers"][0]["scope"] is None
                creator = journal["writers"][0]["process"]
                sender = message.get_sender()
                pid = auxiliary.call_sync("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", "GetConnectionUnixProcessID", GLib.Variant("(s)", (sender,)), GLib.VariantType.new("(u)"), Gio.DBusCallFlags.NONE, 1000, None).unpack()[0]
                assert identity(pid) == creator and os.path.samefile(f"/proc/{pid}/exe", "/usr/lib/x86_64-linux-gnu/xrdp/xrdp-sesexec")
                seen.update(sender=sender, serial=message.get_serial(), creator=creator)
                record("queued.json", seen)
            elif message.get_message_type() in (Gio.DBusMessageType.ERROR, Gio.DBusMessageType.METHOD_RETURN) and seen and message.get_destination() == seen["sender"] and message.get_reply_serial() == seen["serial"]:
                assert message.get_message_type() == Gio.DBusMessageType.ERROR, "dead queued login unexpectedly succeeded"
                assert pin(seen["creator"]) is None
                record("settled.json", {"error": message.get_error_name(), "reply_serial": message.get_reply_serial()})
                GLib.idle_add(loop.quit)
        except BaseException:
            import traceback
            traceback.print_exc()
            GLib.idle_add(loop.quit)
        return False
    def incoming(connection, message, received, _):
        if not received: return message
        kind = message.get_message_type()
        # Filters run on GDBus's shared I/O worker: making a synchronous call
        # there deadlocks even a separate connection's reply handling. Inspect
        # on the main context; retain the message until that callback runs.
        if kind == Gio.DBusMessageType.METHOD_CALL:
            GLib.idle_add(inspect_message, message)
            return None  # A monitor must not reply to somebody else's method.
        if kind in (Gio.DBusMessageType.ERROR, Gio.DBusMessageType.METHOD_RETURN) and message.get_sender() != "org.freedesktop.DBus":
            GLib.idle_add(inspect_message, message)
        return message
    monitor.add_filter(incoming, None)
    rules = ["type='method_call',destination='org.freedesktop.login1',interface='org.freedesktop.login1.Manager',member='CreateSessionWithPIDFD'",
             "type='error',sender='org.freedesktop.login1'", "type='method_return',sender='org.freedesktop.login1'"]
    try:
        monitor.call_sync("org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus.Monitoring", "BecomeMonitor", GLib.Variant("(asu)", (rules, 0)), GLib.VariantType.new("()"), Gio.DBusCallFlags.NONE, 1000, None)
        assert property("InvocationID") == manifest["invocation"]
        signal.pidfd_send_signal(daemon, signal.SIGSTOP)
        wait(lambda: stopped(manifest["daemon"]["pid"]))
        record("armed.json", {})
        GLib.timeout_add_seconds(35, loop.quit)
        loop.run()
    finally:
        resume(daemon); os.close(daemon); monitor.close_sync(None)
    sys.exit(0)

if operation == "probe":
    failures = read(directory / "errors").decode()
    assert not failures, "observer failed: " + failures
    observer = pin_observer(json.loads(read(directory / "observer.json")), "observe"); assert observer is not None
    os.close(observer)
    assert stopped(manifest["daemon"]["pid"]) and not read(directory / "errors")
    print(json.dumps({"stage": "pending" if (directory / "queued.json").exists() else "waiting"}))
elif operation == "release":
    queued = json.loads(read(directory / "queued.json"))
    assert pin(queued["creator"]) is None, "PAM caller has not actually exited"
    daemon = daemon_fd(); assert daemon is not None
    try: resume(daemon)
    finally: os.close(daemon)
    wait(lambda: (directory / "settled.json").exists())
    settled = json.loads(read(directory / "settled.json")); assert settled["error"] and settled["reply_serial"] == queued["serial"]
    assert not read(directory / "errors")
    print(json.dumps({"stage": "released", "error": settled["error"]}))
elif operation == "restart":
    assert (directory / "settled.json").exists() and property("InvocationID") == manifest["invocation"]
    daemon = daemon_fd(); assert daemon is not None
    try: signal.pidfd_send_signal(daemon, signal.SIGKILL)
    finally: os.close(daemon)
    wait(lambda: property("ActiveState") == "active" and property("SubState") == "running" and property("InvocationID") != manifest["invocation"], 10)
    record("restart.json", {"daemon": identity(int(property("MainPID"))), "invocation": property("InvocationID")})
    print('{"stage":"restarted"}')
elif operation == "restore":
    daemon = daemon_fd()
    if daemon is not None:
        try: resume(daemon)
        finally: os.close(daemon)
    for name, op in (("observer.json", "observe"), ("guardian.json", "guardian")):
        if (directory / name).exists():
            fd = pin_observer(json.loads(read(directory / name)), op)
            if fd is not None:
                try:
                    signal.pidfd_send_signal(fd, signal.SIGTERM)
                    p = select.poll(); p.register(fd, select.POLLIN | select.POLLHUP); assert p.poll(5000)
                finally: os.close(fd)
    assert property("ActiveState") == "active" and property("SubState") == "running" and not stopped(int(property("MainPID")))
    for p in directory.iterdir():
        assert p.name in ("manifest.json", "observer.py", "errors", "observer.json", "guardian.json", "armed.json", "queued.json", "settled.json", "restart.json")
        read(p); p.unlink()
    directory.rmdir()
    print('{"stage":"restored"}')
else:
    raise AssertionError("unknown logind queue fixture operation")

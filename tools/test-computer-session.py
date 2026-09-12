"""Opt-in, disposable Linux container integration test, never run on a host.

Two real UID-separated X11/DBus/GTK sessions exercise the release binary.
This is transport evidence, not PVE/xrdp or Windows bootstrap acceptance.
"""
import base64
import fcntl
import hashlib
import json
import os
from pathlib import Path
import secrets
import signal
import socket
import subprocess
import sys
import threading
import time

BINARY = "/out/vc-workspace-guest-agent"
BASE = Path("/var/lib/vc-workspace/computer-v2")
# The existing generic protocol/epoch matrix also serves human Helpers. Agent
# accounts additionally require their real versioned lifecycle, tested below.
USERS = ("vcw0123456789ab", "vcwabcdef012345")
ACTIVE_EPOCH = 1


def command(*args, check=True, **kwargs):
    return subprocess.run(args, check=check, capture_output=True, text=True, timeout=25, **kwargs)


def cli(operation, username=None, *args, check=True, **kwargs):
    options = ("--guest-user", username) if username else ()
    return command(BINARY, "computer-v2-" + operation, *options, *args, check=check, **kwargs)


def eventually(fn, timeout=20):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            result = fn()
            if result:
                return result
        except (OSError, subprocess.SubprocessError, ValueError) as error:
            last = error
        time.sleep(0.1)
    raise AssertionError(f"condition did not converge: {last}")


def desktop(username):
    # Runs as the target user inside its own DBus/Xvfb session.
    import gi
    gi.require_version("Gtk", "3.0")
    from gi.repository import Gtk, GLib
    (Path.home() / "fixture.pid").write_text(str(os.getpid()))
    subprocess.Popen(["openbox"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    helpers = []
    window = Gtk.Window(title="Private desktop " + username)
    window.set_default_size(800, 500)
    box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=20)
    box.pack_start(Gtk.Label(label="Private content " + username), False, False, 20)
    entry = Gtk.Entry()
    entry.get_accessible().set_name("Input " + username)
    entry.set_placeholder_text("Isolated input " + username)
    entry.connect("changed", lambda item: (Path.home() / "typed.txt").write_text(item.get_text()))
    box.pack_start(entry, False, False, 20)
    window.add(box)
    window.connect("destroy", Gtk.main_quit)
    window.show_all()
    window.present()
    entry.grab_focus()
    def ready():
        if not window.is_active():
            window.present()
            entry.grab_focus()
            return True
        helpers.append(subprocess.Popen([BINARY, "computer-v2-helper", "--guest-user", username]))
        (Path.home() / "helper.pid").write_text(str(helpers[-1].pid))
        return False
    GLib.timeout_add(200, ready)
    def restart():
        marker = Path.home() / "restart-helper"
        if marker.exists() and helpers:
            marker.unlink()
            helpers[-1].terminate()
            helpers[-1].wait(timeout=5)
            helpers.append(subprocess.Popen([BINARY, "computer-v2-helper", "--guest-user", username]))
            (Path.home() / "helper.pid").write_text(str(helpers[-1].pid))
        return True
    GLib.timeout_add(100, restart)
    try:
        Gtk.main()
    finally:
        for helper in helpers:
            helper.terminate()
            helper.wait(timeout=5)


def authority(target, epoch=1, state="active", expires_unix_ms=None):
    global ACTIVE_EPOCH
    value = {"schema_version": 2, "target": target if state == "active" else None,
             "authority": {"schema_version": 1, "lease_id": "lease_a-Z_" + "a" * 20,
                           "control_epoch": epoch, "state": state,
                           "expires_unix_ms": (expires_unix_ms or int(time.time() * 1000) + 60_000) if state == "active" else 0}}
    cli("authority", input=json.dumps(value))
    ACTIVE_EPOCH = epoch
    return value


def action(target, operation, payload, epoch=None):
    if epoch is None:
        epoch = ACTIVE_EPOCH
    return {"schema_version": 2, "target": target, "timeout_ms": 5000,
            "request": {"schema_version": 1, "request_id": "action_" + secrets.token_hex(16),
                        "lease_id": "lease_a-Z_" + "a" * 20, "control_epoch": epoch,
                        "expires_unix_ms": int(time.time() * 1000) + 15_000,
                        "operation": operation, **payload}}


def dispatch(value, check=True):
    reqid = value["request"]["request_id"]
    path = BASE / "inbox" / (reqid + ".json.tmp")
    staged = cli("stage", value["target"]["username"], "--request-id", reqid, check=check)
    if staged.returncode:
        return staged
    assert path.stat().st_mode & 0o777 == 0o600
    path.write_text(json.dumps(value))
    published = cli("publish", value["target"]["username"], "--request-id", reqid, check=check)
    if published.returncode:
        return published
    return cli("dispatch", value["target"]["username"], "--request-id", reqid, check=check)


def successful(value):
    response = json.loads(dispatch(value).stdout)
    assert response["ok"], response.get("error")
    assert response["request_id"] == value["request"]["request_id"]
    return response


def rejected(value):
    result = dispatch(value, check=False)
    assert result.returncode or not json.loads(result.stdout)["ok"], "unsafe action was accepted"


def account_suite():
    username = "vca998877665544"
    cli("account-provision", username)
    owned = BASE / "users" / username / "account.json"
    identity = json.loads(owned.read_text())
    assert identity["uid"] >= 1000 and identity["username"] == username
    assert owned.stat().st_uid == 0 and owned.stat().st_mode & 0o777 == 0o600
    cli("account-provision", username)
    assert json.loads(owned.read_text()) == identity, "idempotent provision changed UID"
    shadow = command("getent", "shadow", username).stdout.split(":")
    assert shadow[1].startswith("!") and shadow[7] == "1", "new account can log in before policy"
    command("chpasswd", input=username + ":Vcw1!disposable-only-password\n")
    cli("account-enable", username)
    shadow = command("getent", "shadow", username).stdout.split(":")
    assert not shadow[1].startswith("!") and shadow[7] == ""
    child = subprocess.Popen(["runuser", "-u", username, "--", "sleep", "600"])
    eventually(lambda: command("pgrep", "-u", str(identity["uid"]), check=False).returncode == 0)
    cli("account-disable", username)
    child.wait(timeout=5)
    assert command("pgrep", "-u", str(identity["uid"]), check=False).returncode == 1
    shadow = command("getent", "shadow", username).stdout.split(":")
    assert shadow[1].startswith("!") and shadow[7] == "1"
    foreign = "vca001122334455"
    command("useradd", "--create-home", foreign)
    for operation in ("account-provision", "account-enable", "account-disable"):
        result = cli(operation, foreign, check=False)
        assert result.returncode and "not platform managed" in result.stderr, "unowned user adopted"
    # Even a managed user cannot redirect provisioning writes into root files.
    protected = Path("/root/agent-session-protected")
    protected.write_text("preserved")
    xsession = Path("/home", username, ".xsession")
    xsession.unlink()
    xsession.symlink_to(protected)
    assert cli("account-provision", username, check=False).returncode
    assert protected.read_text() == "preserved"
    xsession.unlink()
    # Root ownership record mismatch must never be silently re-adopted.
    changed = dict(identity, uid=identity["uid"]+1)
    owned.write_text(json.dumps(changed))
    assert cli("account-enable", username, check=False).returncode
    owned.write_text(json.dumps(identity))
    print("Agent account ownership, idempotence, enable/disable and symlink isolation passed", flush=True)


def legacy_revocation_suite():
    payload = json.dumps({"schema_version": 1, "state": "revoked"})
    script = "/workspace/tools/revoke-legacy-linux.py"
    command("python3", script, payload)  # Missing old generations are safe.
    protected = Path("/root/legacy-authority-protected")
    protected.write_text("preserved")
    for product in ("vc-workspace", "vc-vdi"):
        spool = Path("/var/lib", product, "computer")
        spool.mkdir(parents=True, mode=0o700)
        os.chown(spool, 1000, 1000)
        (spool / "authority.json").symlink_to(protected)
    command("python3", script, payload)
    for product in ("vc-workspace", "vc-vdi"):
        spool = Path("/var/lib", product, "computer")
        assert spool.stat().st_uid == 0 and spool.stat().st_mode & 0o022 == 0
        assert json.loads((spool / "authority.json").read_text())["state"] == "revoked"
        assert not (spool / "authority.json").is_symlink()
    assert protected.read_text() == "preserved"
    old = Path("/var/lib/vc-vdi/computer")
    old.rename(old.with_name("computer-preserved"))
    old.symlink_to("/root", target_is_directory=True)
    assert command("python3", script, payload, check=False).returncode, "spool symlink accepted"
    print("Both legacy generations fenced; unsafe paths refused", flush=True)


def staging_suite():
    reqid = "action_" + "e" * 24
    path = BASE / "inbox" / (reqid + ".json.tmp")
    path.write_text("old untrusted QGA inode")
    path.chmod(0o666)
    old = os.open(path, os.O_WRONLY)
    try:
        cli("stage", USERS[0], "--request-id", reqid)
        os.write(old, b"old writer cannot alter replacement")
        assert path.read_bytes() == b"" and path.stat().st_mode & 0o777 == 0o600
    finally:
        os.close(old)
    path.unlink()
    path.symlink_to("/root/agent-session-protected")
    assert cli("stage", USERS[0], "--request-id", reqid, check=False).returncode
    assert Path("/root/agent-session-protected").read_text() == "preserved"
    path.unlink()
    assert cli("authority-stage", check=False).returncode, "legacy shared authorization still available"
    print("Action staging isolates old inodes; shared authority staging retired", flush=True)


def authority_fence_suite(key):
    path = BASE / "authority-account-fenced.json"
    active = authority(key, epoch=10)
    # A process launched earlier may deliver its original stdin much later.
    delayed = subprocess.Popen([BINARY, "computer-v2-authority"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    try:
        revoked = authority(None, epoch=11, state="revoked")
        delayed.communicate(json.dumps(active), timeout=5)
        assert delayed.returncode, "late old active overwrote newer revocation"
        assert json.loads(path.read_text()) == revoked
    finally:
        if delayed.poll() is None:
            delayed.kill(); delayed.wait(timeout=5)
    assert cli("authority", input=json.dumps({**active, "authority":{**active["authority"],"control_epoch":11}}),check=False).returncode, "revoked epoch reactivated"
    active = authority(key, epoch=12)
    assert cli("authority",input=json.dumps(revoked),check=False).returncode, "late old revoke overwrote new lease"
    for changed in [
        {**active,"authority":{**active["authority"],"lease_id":"lease_other"}},
        {**active,"authority":{**active["authority"],"expires_unix_ms":active["authority"]["expires_unix_ms"]-1}},
        {**active,"target":{**key,"uid":key["uid"]+1}},
    ]:
        assert cli("authority",input=json.dumps(changed),check=False).returncode
        assert json.loads(path.read_text()) == active
    # An old executable writing its retired path cannot affect the new Helper.
    (BASE / "authority.json").write_text(json.dumps(revoked))
    assert json.loads(path.read_text()) == active
    successful(action(key,"screenshot",{"screenshot":{"max_width":320}},epoch=12))
    # Stop only our disposable Helper so a real publisher pauses during its
    # authenticated discovery, while holding the exclusive publication lock.
    helper_pid = int(Path("/home", key["username"], "helper.pid").read_text())
    os.kill(helper_pid, signal.SIGSTOP)
    publishing = subprocess.Popen([BINARY,"computer-v2-authority"],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
    try:
        publishing.stdin.write(json.dumps(active)); publishing.stdin.flush(); publishing.stdin.close()
        def reserved():
            with (BASE / "authority.lock").open("rb") as lock:
                try:
                    fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
                    fcntl.flock(lock,fcntl.LOCK_UN)
                    return False
                except BlockingIOError:
                    return True
        eventually(reserved,timeout=2)
        assert cli("authority",input=json.dumps(active),check=False).returncode, "concurrent publisher entered reserved fence"
        publishing.kill(); publishing.wait(timeout=5)
    finally:
        if publishing.poll() is None:
            publishing.kill(); publishing.wait(timeout=5)
        os.kill(helper_pid, signal.SIGCONT)
    assert json.loads(path.read_text()) == active, "interrupted publication damaged snapshot"
    orphan = BASE / "inbox" / (".authority-" + "b"*32 + ".tmp")
    orphan.write_text("abandoned private stage"); orphan.chmod(0o600)
    unrelated = BASE / "inbox" / "unrelated-preserved"
    unrelated.write_text("preserved")
    revoked = authority(None,epoch=13,state="revoked")
    assert not orphan.exists() and unrelated.read_text()=="preserved"
    assert json.loads(path.read_text()) == revoked
    rejected(action(key,"screenshot",{"screenshot":{"max_width":320}},epoch=12))
    # Model an upgrade only inside this guarded disposable container. A legacy
    # snapshot cannot be silently treated as an empty/new control plane.
    legacy = BASE / "authority.json"
    legacy.write_text(json.dumps(revoked)); legacy.chmod(0o644)
    path.unlink()
    upgraded = {**active,"authority":{**active["authority"],"control_epoch":14}}
    assert cli("authority",input=json.dumps(upgraded),check=False).returncode
    assert not path.exists(), "upgrade activated before first revocation"
    authority(None,epoch=14,state="revoked")
    assert cli("authority",input=json.dumps(upgraded),check=False).returncode
    active = authority(key,epoch=15)
    assert json.loads(path.read_text()) == active
    authority(None,epoch=16,state="revoked")
    # Corrupt legacy history fails closed, rather than resetting to epoch 1.
    committed = path.read_bytes()
    path.unlink(); legacy.write_text("{}")
    assert cli("authority",input=json.dumps(revoked),check=False).returncode
    assert not path.exists()
    path.write_bytes(committed); path.chmod(0o644)
    legacy.write_text(json.dumps(revoked))
    print("PASS monotonic epoch, late active/revoke, owner/expiry, old writer isolation, writer conflict and killed publisher recovery",flush=True)
    print("PASS legacy upgrade requires first revocation and preserves corrupt-history rejection",flush=True)


def agent_input_lifecycle_suite(processes):
    """Real shadow changes and live GUI Helpers, including dispatcher death.

    Do not edit a lifecycle journal to pretend a transition happened. The
    dedicated test binary exits after the production durable-intent write.
    """
    test_binary = "/out/vc-workspace-guest-agent-tests"
    for index, user in enumerate(("vca1029384756ab", "vcaba6574839201")):
        epoch = 30 + index * 2
        cli("account-provision", user)
        identity = json.loads(cli("account-inspect", user).stdout)["identity"]
        deadline = int(time.time()) + 120
        intent = {"schema_version": 1, "identity": identity,
                  "lease_id": "lease_a-Z_" + "a" * 20, "control_epoch": epoch,
                  "login_generation": 1, "expires_unix_seconds": deadline}
        def lease(operation, **changes):
            request = {**intent, "operation": operation, **changes}
            if operation == "open":
                request["password"] = "Vcw1!disposable-input-lifecycle"
            return cli("account-lease", user, input=json.dumps(request))
        def start():
            processes.append(subprocess.Popen([
                "runuser", "-u", user, "--", "dbus-run-session", "--", "xvfb-run", "-a",
                "-s", "-screen 0 1280x720x24 -nolisten tcp", "python3", __file__, "--desktop", user,
            ], start_new_session=True))
            return eventually(lambda: json.loads(cli("session", user).stdout)["target"])
        lease("open")
        key = start()
        # No input grant while credentials are still open, even with a Helper.
        proposed = {"schema_version": 2, "target": key, "authority": {
            "schema_version": 1, "lease_id": intent["lease_id"], "control_epoch": epoch,
            "state": "active", "expires_unix_ms": deadline * 1000}}
        assert cli("authority", input=json.dumps(proposed), check=False).returncode
        lease("seal")
        authority(key, epoch=epoch, expires_unix_ms=deadline * 1000)
        committed = json.loads((BASE / "authority-account-fenced.json").read_text())
        assert committed["login_generation"] == 1
        assert cli("authority", input=json.dumps(committed), check=False).returncode, "caller selected login generation"
        journal = BASE / "users" / user / "account-lifecycle.json"
        assert journal.stat().st_mode & 0o777 == 0o644 and journal.stat().st_uid == 0
        assert "password" not in journal.read_text() and "Vcw1!" not in journal.read_text()
        command("runuser", "-u", user, "--", "cat", str(journal))
        assert command("runuser", "-u", user, "--", "test", "-w", str(journal), check=False).returncode
        assert command("runuser", "-u", user, "--", "cat", str(journal.with_name("account.json")), check=False).returncode
        cached = action(key, "screenshot", {"screenshot": {"max_width": 640}})
        assert successful(cached)["screenshot"]["width"] == 640
        successful(action(key, "type_text", {"text": {"value": "bounded-generation", "sensitive": True}}))
        eventually(lambda: (Path("/home") / user / "typed.txt").read_text() == "bounded-generation")
        # Die after opening-generation-2 is durable, before any OS mutation:
        # the old application and Helper are deliberately still alive.
        pending = {**intent, "operation": "open", "login_generation": 2,
                   "password": "Vcw1!disposable-input-lifecycle"}
        expected_phase, expected_generation = "opening", 2
        if index == 1:
            pending = {**intent, "operation": "revoke", "expires_unix_seconds": 0}
            expected_phase, expected_generation = "revoked", 1
        crash = command(test_binary, "--exact", "computer::session::account::lease::tests::account_lease_crash_child",
                        "--ignored", "--nocapture", check=False, input=json.dumps(pending),
                        env={**os.environ, "VC_WORKSPACE_ACCOUNT_TEST_CHECKPOINT": "1"})
        assert crash.returncode == 86, crash.stderr
        state = json.loads(cli("account-inspect", user).stdout)
        assert state["lifecycle"]["phase"] == expected_phase and state["lifecycle"]["login_generation"] == expected_generation
        assert not state["processes_absent"]
        assert json.loads(cli("session", user).stdout)["target"] == key
        # Independent of control-plane prechecks and global authority: neither
        # cached screenshots nor fresh input may survive the pending journal.
        assert json.loads((BASE / "authority-account-fenced.json").read_text()) == committed
        rejected(cached)
        rejected(action(key, "type_text", {"text": {"value": "unsafe", "sensitive": True}}))
        assert (Path("/home") / user / "typed.txt").read_text() == "bounded-generation"
        assert cli("authority", input=json.dumps(proposed), check=False).returncode
        cli("accounts-reconcile")
        state = json.loads(cli("account-inspect", user).stdout)
        assert state["processes_absent"] and state["account"]["disabled"] and state["lifecycle"]["phase"] == "revoked"
        # Real higher-epoch recovery retains the account and its existing Home.
        intent.update(control_epoch=epoch + 1, expires_unix_seconds=int(time.time()) + 120)
        lease("open")
        renewed = start()
        lease("seal")
        authority(renewed, epoch=epoch + 1, expires_unix_ms=intent["expires_unix_seconds"] * 1000)
        assert renewed["uid"] == key["uid"] and renewed["instance_id"] != key["instance_id"]
        successful(action(renewed, "screenshot", {"screenshot": {"max_width": 640}}))
        lease("revoke", expires_unix_seconds=0)
        print("PASS Agent live Helper refuses open/pending/revoked account state and cached observations; real recovery retained UID", flush=True)


def root_suite():
    if not Path("/.dockerenv").exists() or os.geteuid() != 0 or os.environ.get("VC_WORKSPACE_DISPOSABLE_TEST") != "1":
        raise SystemExit("requires a new disposable container with VC_WORKSPACE_DISPOSABLE_TEST=1")
    # A full Guest creates this shared directory through root tmpfiles. In this
    # minimal container, do so before either unprivileged Xvfb can create it.
    x11 = Path("/tmp/.X11-unix")
    assert not x11.exists()
    x11.mkdir(mode=0o1777)
    x11.chmod(0o1777)
    processes = []
    try:
        for username in USERS:
            command("useradd", "--create-home", "--shell", "/bin/bash", username)
            cli("init", username)
        account_suite()
        legacy_revocation_suite()
        staging_suite()
        for username in USERS:
            processes.append(subprocess.Popen([
                "runuser", "-u", username, "--", "dbus-run-session", "--", "xvfb-run", "-a",
                "-s", "-screen 0 1280x720x24 -nolisten tcp", "python3", __file__, "--desktop", username,
            ], start_new_session=True))
        keys = [eventually(lambda user=user: json.loads(cli("session", user).stdout)["target"]) for user in USERS]
        assert keys[0]["uid"] != keys[1]["uid"] and keys[0]["session_id"] != keys[1]["session_id"]
        shots = []
        for epoch, (user, key) in enumerate(zip(USERS, keys), start=1):
            authority(key,epoch=epoch)
            shot = successful(action(key, "screenshot", {"screenshot": {"max_width": 1280}}))["screenshot"]
            raw = base64.b64decode(shot["data"], validate=True)
            assert hashlib.sha256(raw).hexdigest() == shot["sha256"]
            assert (shot["width"], shot["height"]) == (1280, 720)
            assert shot["desktop_bounds"] == {"x": 0, "y": 0, "width": 1280, "height": 720}
            reduced = successful(action(key, "screenshot", {"screenshot": {"max_width": 640}}))["screenshot"]
            assert (reduced["width"], reduced["height"]) == (640, 360)
            assert reduced["desktop_bounds"] == shot["desktop_bounds"], "resizing lost desktop input coordinates"
            shots.append(shot["sha256"])
            tree = eventually(lambda: successful(action(key, "accessibility_snapshot", {"accessibility": {"max_depth": 6, "max_nodes": 100}}))["accessibility"])
            assert user in json.dumps(tree), "semantic tree is not scoped to the target desktop"
            assert tree["source"] == "linux_atspi", tree["source"]
            entry = next(node for node in tree["nodes"] if node.get("name") == "Input " + user)
            assert entry["width"] > 0 and entry["height"] > 0, entry
            successful(action(key, "mouse", {"mouse": {"action": "click", "button": "left",
                       "x": entry["x"] + entry["width"] // 2, "y": entry["y"] + entry["height"] // 2}}))
            value = action(key, "type_text", {"text": {"value": "unique-" + user, "sensitive": False}})
            first = successful(value)
            repeated = json.loads(cli("dispatch", user, "--request-id", value["request"]["request_id"]).stdout)
            assert first == repeated, "QGA replay result changed"
            try:
                eventually(lambda: Path("/home", user, "typed.txt").read_text() == "unique-" + user, timeout=5)
            except AssertionError:
                current = successful(action(key, "accessibility_snapshot", {"accessibility": {"max_depth": 6, "max_nodes": 100}}))["accessibility"]
                print("fixture input failed", {"entry": entry, "tree": current}, flush=True)
                raise
            value["request"]["text"]["value"] = "mutated"
            rejected(value)
            cli("cleanup", user, "--request-id", value["request"]["request_id"])
        assert shots[0] != shots[1], "both UIDs observed the same screen"
        print("PASS separate UID/session screenshot, AT-SPI, mouse, text input and at-most-once replay", flush=True)

        authority(keys[1], epoch=2)
        rejected(action(keys[0], "key", {"key": {"key": "Enter"}}))
        rejected(action(keys[1], "key", {"key": {"key": "Enter"}}, epoch=1))
        fake_key = {**keys[1], "instance_id": "f" * 64}
        rejected(action(fake_key, "key", {"key": {"key": "Enter"}}, epoch=2))
        authority(None, epoch=3, state="revoked")
        rejected(action(keys[1], "screenshot", {"screenshot": {"max_width": 1280}}, epoch=3))
        print("PASS wrong user, old epoch, wrong Helper incarnation and revocation denied", flush=True)

        old_key = keys[1]
        authority(old_key, epoch=4)
        Path("/home", USERS[1], "restart-helper").touch()
        def reconnected():
            current = json.loads(cli("session", USERS[1]).stdout)["target"]
            return current if current["instance_id"] != old_key["instance_id"] else None
        new_key = eventually(reconnected)
        assert new_key["uid"] == old_key["uid"] and new_key["session_id"] == old_key["session_id"]
        rejected(action(old_key, "screenshot", {"screenshot": {"max_width": 1280}}, epoch=4))
        rejected(action(new_key, "screenshot", {"screenshot": {"max_width": 1280}}, epoch=4))
        authority(new_key, epoch=4)
        successful(action(new_key, "screenshot", {"screenshot": {"max_width": 1280}}, epoch=4))
        assert Path("/home", USERS[1], "typed.txt").read_text() == "unique-" + USERS[1]
        print("PASS Helper restart preserves the OS application but requires fresh session authority", flush=True)

        # A hung accessibility provider must not pin the Helper, nor cause
        # it to fall back to a different user's desktop.
        fixture_pid = int(Path("/home", USERS[1], "fixture.pid").read_text())
        os.kill(fixture_pid, signal.SIGSTOP)
        try:
            bounded = action(new_key, "accessibility_snapshot", {"accessibility": {"max_depth": 6, "max_nodes": 100}}, epoch=4)
            bounded["timeout_ms"] = 250
            started = time.monotonic()
            rejected(bounded)
            assert time.monotonic() - started < 3, "worker timeout was not enforced"
        finally:
            os.kill(fixture_pid, signal.SIGCONT)
        successful(action(new_key, "screenshot", {"screenshot": {"max_width": 1280}}, epoch=4))
        duplicate = command("runuser", "-u", USERS[1], "--", "env", "DISPLAY=:99", BINARY,
                            "computer-v2-helper", "--guest-user", USERS[1], check=False)
        assert duplicate.returncode, "ambiguous second Helper was accepted"
        print("PASS hung AT-SPI worker deadline, recovery and duplicate Helper rejection", flush=True)

        # Kernel peer authentication applies even to the endpoint's own user.
        probe = """import socket,struct,json,sys
s=socket.socket(socket.AF_UNIX);s.settimeout(2);s.connect(sys.argv[1]);v=b'{"kind":"session"}'
try:
 s.sendall(struct.pack('!I',len(v))+v); data=s.recv(4)
except (ConnectionResetError,BrokenPipeError): data=b''
assert not data, 'Helper trusted non-root peer'
"""
        command("runuser", "-u", USERS[0], "--", "python3", "-c", probe,
                str(BASE / "users" / USERS[0] / "runtime/helper.sock"))
        assert command("runuser", "-u", USERS[0], "--", "test", "-r", str(BASE / "users" / USERS[1] / "runtime/helper.sock"), check=False).returncode
        assert command("runuser", "-u", USERS[0], "--", "test", "-w", str(BASE / "authority-account-fenced.json"), check=False).returncode
        endpoint = BASE / "users" / USERS[0] / "runtime/helper.sock"
        preserved = endpoint.with_suffix(".preserved")
        endpoint.rename(preserved)
        listener = socket.socket(socket.AF_UNIX)
        try:
            listener.bind(str(endpoint))
            listener.listen(1)
            listener.settimeout(5)
            def impostor():
                peer, _ = listener.accept()
                peer.close()
            thread = threading.Thread(target=impostor)
            thread.start()
            mismatch = cli("session", USERS[0], check=False)
            thread.join(timeout=5)
            assert mismatch.returncode and "Helper UID does not match target account" in mismatch.stderr, "dispatcher did not authenticate the Helper UID"
        finally:
            listener.close()
            endpoint.unlink(missing_ok=True)
            preserved.rename(endpoint)
        linked_action = action(new_key, "screenshot", {"screenshot": {"max_width": 1280}}, epoch=4)
        linked_id = linked_action["request"]["request_id"]
        source = BASE / "test-symlink-target.json"
        source.write_text(json.dumps(linked_action))
        source.chmod(0o600)
        linked = BASE / "inbox" / (linked_id + ".json")
        linked.symlink_to(source)
        refused = cli("dispatch", USERS[1], "--request-id", linked_id, check=False)
        assert refused.returncode and not refused.stdout, "root followed a request symlink"
        print("PASS root/Helper peer authentication, authority permissions and symlink rejection", flush=True)
        expired = action(new_key, "type_text", {"text": {"value": "crash-leftover"}}, epoch=4)
        expired["request"]["expires_unix_ms"] = 1
        residue = BASE / "inbox" / (expired["request"]["request_id"] + ".json")
        residue.write_text(json.dumps(expired))
        residue.chmod(0o600)
        cli("control-init")
        assert not residue.exists(), "expired crash input was retained"
        print("PASS expired immutable request cleanup", flush=True)
        authority_fence_suite(new_key)
        agent_input_lifecycle_suite(processes)
    finally:
        for process in processes:
            try:
                os.killpg(process.pid, signal.SIGTERM)
                process.wait(timeout=5)
            except (ProcessLookupError, subprocess.TimeoutExpired):
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass


if __name__ == "__main__":
    if len(sys.argv) == 3 and sys.argv[1] == "--desktop":
        desktop(sys.argv[2])
    else:
        root_suite()

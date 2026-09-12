"""Disposable installer and actual libpam namespace tests, not xrdp acceptance."""
import ctypes
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import time

AGENT = Path("/usr/local/sbin/vc-workspace-guest-agent")
MODULES = Path("/usr/local/lib/security")
TARGET = Path("/etc/pam.d/xrdp-sesman")
INSTALLER = "/workspace/deploy/guest/linux/install-login-fence.py"


def command(args, *, ok=True):
    result = subprocess.run(args, capture_output=True, text=True, timeout=10)
    assert (result.returncode == 0) == ok, (args, result.stderr)
    return result.stdout


def capabilities():
    return json.loads(command([str(AGENT), "computer-v2-capabilities"]))


def probe(service, username, *, succeeds):
    # Real libpam dispatch, no password or mocked module return code. The fixed
    # Native module ignores other account namespaces but denies Native users
    # outside its supported xrdp service. A real sesexec/logind login is separate.
    pam = ctypes.CDLL("libpam.so.0")
    callback = ctypes.CFUNCTYPE(ctypes.c_int, ctypes.c_int, ctypes.c_void_p,
                               ctypes.c_void_p, ctypes.c_void_p)

    class Conversation(ctypes.Structure):
        _fields_ = [("conv", callback), ("data", ctypes.c_void_p)]

    conv = Conversation(callback(lambda *args: 19), None)
    handle = ctypes.c_void_p()
    pam.pam_start.argtypes = [ctypes.c_char_p, ctypes.c_char_p,
                             ctypes.POINTER(Conversation), ctypes.POINTER(ctypes.c_void_p)]
    for function in (pam.pam_authenticate, pam.pam_open_session, pam.pam_close_session, pam.pam_end):
        function.argtypes = [ctypes.c_void_p, ctypes.c_int]
        function.restype = ctypes.c_int
    assert pam.pam_start(service.encode(), username.encode(), ctypes.byref(conv), ctypes.byref(handle)) == 0
    try:
        assert (pam.pam_authenticate(handle, 0) == 0) == succeeds
        assert (pam.pam_open_session(handle, 0) == 0) == succeeds
    finally:
        assert pam.pam_end(handle, 0) == 0


def suite():
    assert os.geteuid() == 0 and Path("/.dockerenv").is_file()
    assert os.environ.get("VC_WORKSPACE_DISPOSABLE_TEST") == "1"
    assert not AGENT.exists() and not TARGET.exists()
    AGENT.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile("/out/vc-workspace-guest-agent", AGENT)
    AGENT.chmod(0o755)
    MODULES.mkdir(parents=True, exist_ok=True)
    for name in ("pam_vcworkspace.so", "pam_vcworkspace_native.so"):
        shutil.copyfile(Path("/out") / name, MODULES / name)
        (MODULES / name).chmod(0o644)
    original = b"@include common-auth\n@include common-account\n@include common-session\n"
    TARGET.write_bytes(original)
    TARGET.chmod(0o644)
    agent_prefix = command([str(AGENT), "computer-v2-pam-policy"]).encode()
    native_prefix = command([str(AGENT), "computer-v2-native-pam-policy"]).encode()
    assert capabilities()["native_login_birth_fence"] == "unavailable"
    command([sys.executable, "-O", INSTALLER, "--native"], ok=False)
    command([sys.executable, INSTALLER, "--native"], ok=False)
    assert TARGET.read_bytes() == original
    command([sys.executable, INSTALLER])
    before = TARGET.read_bytes()
    assert before == agent_prefix + original
    assert capabilities()["native_login_birth_fence"] == "unavailable"
    # Upgrading just the Agent module must not silently activate vcw interception.
    command([sys.executable, INSTALLER])
    assert TARGET.read_bytes() == before

    process = subprocess.Popen([sys.executable, "-c",
        "import ctypes,time; assert ctypes.CDLL(None).prctl(15,b'xrdp-sesman',0,0,0)==0; time.sleep(20)"])
    try:
        for _ in range(50):
            if subprocess.run(["pgrep", "-x", "xrdp-sesman"], capture_output=True).returncode == 0:
                break
            time.sleep(0.02)
        else:
            raise AssertionError("live session-creator fixture did not start")
        command([sys.executable, INSTALLER, "--native"], ok=False)
        assert TARGET.read_bytes() == before
    finally:
        process.terminate()
        process.wait(timeout=5)

    native_module = MODULES / "pam_vcworkspace_native.so"
    native_module.chmod(0o666)
    command([sys.executable, INSTALLER, "--native"], ok=False)
    assert TARGET.read_bytes() == before
    native_module.chmod(0o644)
    backup = Path(str(TARGET) + ".vc-workspace-before-native")
    backup.write_bytes(before)
    backup.chmod(0o644)
    command([sys.executable, INSTALLER, "--native"], ok=False)
    assert TARGET.read_bytes() == before
    backup.chmod(0o600)
    command([sys.executable, INSTALLER, "--native"])
    assert TARGET.read_bytes() == agent_prefix + native_prefix + original
    assert backup.read_bytes() == before and backup.stat().st_mode & 0o777 == 0o600
    assert Path(str(TARGET) + ".vc-workspace-before-birth").read_bytes() == original
    inode = TARGET.stat().st_ino
    command([sys.executable, INSTALLER, "--native"])
    assert TARGET.stat().st_ino == inode
    assert capabilities()["native_login_birth_fence"] == "pam_logind_native_v1"
    native_module.chmod(0o666)
    assert capabilities()["native_login_birth_fence"] == "unavailable"
    native_module.chmod(0o644)
    print("PASS explicit Native PAM upgrade, live creator/unsafe module denial, exact backups and idempotent capability", flush=True)

    # Exercise the release entry after real installation (not the private shadow
    # child). This proves routing/preconditions, not that xrdp login succeeded.
    username = "vcw123456abcdef"
    command([str(AGENT), "computer-v2-native-account-provision", "--guest-user", username])
    state = json.loads(command([str(AGENT), "computer-v2-native-account-inspect", "--guest-user", username]))
    value = dict(schema_version=1, identity=state["identity"], operation="issue", revision=1,
                 connection_id="conn_native_release", expires_unix_seconds=int(time.time()) + 120,
                 password="Vcw1!disposable-release-pam-test")
    result = subprocess.run([str(AGENT), "computer-v2-native-account-credential", "--guest-user", username],
                            input=json.dumps(value), capture_output=True, text=True, timeout=10)
    assert result.returncode == 0 and json.loads(result.stdout)["phase"] == "issued", result.stderr
    assert value["password"] not in result.stdout + result.stderr
    value.pop("password")
    value.update(operation="revoke", revision=2, expires_unix_seconds=0)
    result = subprocess.run([str(AGENT), "computer-v2-native-account-credential", "--guest-user", username],
                            input=json.dumps(value), capture_output=True, text=True, timeout=10)
    assert result.returncode == 0 and json.loads(result.stdout)["phase"] == "revoked", result.stderr
    print("PASS release Native credential command requires and recognizes the explicitly installed policy", flush=True)

    for suffix, name, ignored, denied in (
        ("native", "pam_vcworkspace_native.so", "vca123456abcdef", "vcw123456abcdef"),
        ("agent", "pam_vcworkspace.so", "vcw123456abcdef", "vca123456abcdef"),
    ):
        service = "vcw-" + suffix + "-namespace-test"
        path = Path("/etc/pam.d") / service
        path.write_text(f"auth requisite {MODULES / name}\nauth required pam_permit.so\n"
                        f"session requisite {MODULES / name}\nsession required pam_permit.so\n")
        path.chmod(0o644)
        for username in (ignored, "root", "ordinary-user"):
            probe(service, username, succeeds=True)
        probe(service, denied, succeeds=False)
    print("PASS actual libpam Agent/Native namespace isolation and wrong-service refusal", flush=True)


if __name__ == "__main__":
    suite()

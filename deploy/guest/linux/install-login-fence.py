"""Install the fixed Guest PAM policy during offline image/Guest maintenance.

Never called by login or an HTTP request. Existing xrdp session creators must
be drained before installation; keep an exact root-private recovery copy.
"""
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile

if sys.flags.optimize:
    raise SystemExit("PAM maintenance refuses Python optimization that disables safety checks")
assert os.geteuid() == 0
assert sys.argv[1:] in ([], ["--native"]), "only the explicit --native offline upgrade is supported"
native = sys.argv[1:] == ["--native"]
target = Path("/etc/pam.d/xrdp-sesman")
backup = Path("/etc/pam.d/xrdp-sesman.vc-workspace-before-native" if native else "/etc/pam.d/xrdp-sesman.vc-workspace-before-birth")

def read(path):
    for parent in path.parents:
        meta = parent.lstat()
        assert stat.S_ISDIR(meta.st_mode) and meta.st_uid == 0 and not meta.st_mode & 0o022
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as stream:
        meta = os.fstat(stream.fileno())
        assert stat.S_ISREG(meta.st_mode) and meta.st_uid == 0 and meta.st_nlink == 1 and not meta.st_mode & 0o022 and meta.st_size <= 65536
        raw = stream.read(65537)
        assert len(raw) <= 65536 and b"\0" not in raw
        return raw, stat.S_IMODE(meta.st_mode)

prefix = subprocess.run(["/usr/local/sbin/vc-workspace-guest-agent", "computer-v2-pam-policy"],
                        capture_output=True, check=True, timeout=5).stdout
assert prefix == b"# VC Workspace login birth fence v2\nauth requisite /usr/local/lib/security/pam_vcworkspace.so\nsession requisite /usr/local/lib/security/pam_vcworkspace.so\n"
agent_prefix = prefix
if native:
    addition = subprocess.run(["/usr/local/sbin/vc-workspace-guest-agent", "computer-v2-native-pam-policy"],
                              capture_output=True, check=True, timeout=5).stdout
    assert addition == b"# VC Workspace Native login birth fence v1\nauth requisite /usr/local/lib/security/pam_vcworkspace_native.so\nsession requisite /usr/local/lib/security/pam_vcworkspace_native.so\n"
    prefix += addition
# Match the capability probe's ownership requirements; installation is never
# allowed to activate a PAM stack whose native module has not been installed.
modules = ["pam_vcworkspace.so"] + (["pam_vcworkspace_native.so"] if native else [])
for name in modules:
    module = Path("/usr/local/lib/security") / name
    for parent in module.parents:
        meta = parent.lstat()
        assert stat.S_ISDIR(meta.st_mode) and meta.st_uid == 0 and not meta.st_mode & 0o022
    meta = module.lstat()
    assert stat.S_ISREG(meta.st_mode) and meta.st_uid == 0 and meta.st_nlink == 1 and not meta.st_mode & 0o022
original, mode = read(target)
if original.startswith(prefix):
    print("PAM login birth fence already installed")
else:
    if native:
        assert original.startswith(agent_prefix), "install the current Agent fence before the Native offline upgrade"
        remainder = original[len(agent_prefix):]
    else:
        remainder = original
    assert b"computer-v2-pam-" not in remainder and b"pam_vcworkspace" not in remainder, "prior PAM integration requires explicit offline drain/upgrade"
    # Native activation is a separate maintenance step, never a side effect of
    # an Agent upgrade or a login. Stop the creator as well as draining children.
    for name in (("xrdp-sesman",) if native else ()) + ("xrdp-sesexec", "Xorg"):
        result = subprocess.run(["/usr/bin/pgrep", "-x", name], capture_output=True, timeout=5)
        assert result.returncode == 1, "drain existing xrdp processes before installing login protection"
    if os.path.lexists(backup):
        saved, saved_mode = read(backup)
        assert saved == original and saved_mode == 0o600, "recovery file differs or is not root-private"
    else:
        with os.fdopen(os.open(backup, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600), "wb") as stream:
            stream.write(original); stream.flush(); os.fsync(stream.fileno())
    fd, temporary = tempfile.mkstemp(prefix=".vcw-xrdp-pam-", dir=target.parent)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(prefix + remainder); stream.flush(); os.fsync(stream.fileno()); os.fchmod(stream.fileno(), mode)
        assert read(target)[0] == original, "PAM stack changed during installation"
        os.replace(temporary, target)
        directory = os.open(target.parent, os.O_RDONLY | os.O_DIRECTORY)
        try: os.fsync(directory)
        finally: os.close(directory)
    finally:
        if os.path.lexists(temporary): os.unlink(temporary)
    print("PAM login birth fence installed; original stack retained")

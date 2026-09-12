"""Install the reviewed runtime only while provisioning a fresh Debian image.

The expected digest is supplied by the build controller, independently of the
uploaded package. This is not an updater for occupied desktops.
"""
import hashlib
import json
import os
import pathlib
import platform
import re
import stat
import subprocess
import sys
import tempfile

BASE_VERSION = "0.10.1-3.1+deb13u2"
PACKAGE_VERSION = BASE_VERSION + "+vcw1"
MAX_PACKAGE_BYTES = 32 * 1024 * 1024


def read_package(path, expected):
    if not isinstance(expected, str) or not re.fullmatch(r"[0-9a-f]{64}", expected):
        raise ValueError("a canonical xrdp package SHA-256 is required")
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or not 0 < info.st_size <= MAX_PACKAGE_BYTES:
            raise ValueError("xrdp package must be a bounded regular file")
        raw = stream.read(MAX_PACKAGE_BYTES + 1)
    if not 0 < len(raw) <= MAX_PACKAGE_BYTES or hashlib.sha256(raw).hexdigest() != expected:
        raise ValueError("xrdp package digest mismatch")
    return raw


def query(*command):
    result = subprocess.run(command, check=True, capture_output=True, text=True, timeout=15)
    if len(result.stdout) > 8192:
        raise ValueError("unexpected package metadata size")
    return result.stdout.strip()


def validate_metadata(package):
    expected = {"Package": "xrdp", "Architecture": "amd64", "Version": PACKAGE_VERSION}
    for field, value in expected.items():
        if query("dpkg-deb", "--field", str(package), field) != value:
            raise ValueError("unexpected xrdp package " + field)


def root_directory(path):
    for parent in (path, *path.parents):
        info = parent.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
            raise ValueError("unsafe runtime receipt directory")


def install(package, expected):
    if os.geteuid() != 0 or sys.flags.optimize:
        raise ValueError("fresh-image installer requires root and normal Python checks")
    release = platform.freedesktop_os_release()
    if release.get("ID") != "debian" or release.get("VERSION_CODENAME") != "trixie":
        raise ValueError("expected Debian 13 trixie")
    if query("dpkg", "--print-architecture") != "amd64":
        raise ValueError("expected Debian amd64")
    raw = read_package(package, expected)
    receipt = pathlib.Path("/usr/share/vc-workspace/rdp-runtime.json")
    root_directory(receipt.parent.parent)
    if receipt.parent.exists():
        root_directory(receipt.parent)
    if os.path.lexists(receipt):
        raise ValueError("runtime is already provisioned; use a fresh image")
    # The bootstrap SSH user owns the uploaded file. Stage verified bytes in a
    # root-private directory so changing /tmp afterward cannot affect apt.
    root_directory(pathlib.Path("/var/lib"))
    with tempfile.TemporaryDirectory(prefix="vc-workspace-xrdp-", dir="/var/lib") as directory:
        stage = pathlib.Path(directory) / "xrdp.deb"
        with os.fdopen(os.open(stage, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "wb") as stream:
            stream.write(raw)
        validate_metadata(stage)
        if query("dpkg-query", "-W", "-f=${Version}", "xrdp") != BASE_VERSION:
            raise ValueError("unexpected installed xrdp version; rebase instead of downgrading")
        subprocess.run(["apt-get", "install", "-y", "--no-install-recommends", str(stage)],
                       check=True, timeout=300, env={**os.environ, "DEBIAN_FRONTEND": "noninteractive"})
        if query("dpkg-query", "-W", "-f=${Version}", "xrdp") != PACKAGE_VERSION:
            raise ValueError("installed xrdp version differs from the reviewed package")
        differences = query("dpkg", "--verify", "xrdp")
        if differences:
            raise ValueError("installed xrdp files differ from package checksums: " + differences)
    daemon = pathlib.Path("/usr/sbin/xrdp").read_bytes()
    record = {"schema_version": 1, "package": "xrdp", "version": PACKAGE_VERSION,
              "architecture": "amd64", "package_sha256": expected,
              "daemon_sha256": hashlib.sha256(daemon).hexdigest(),
              "fixes": ["pointer_image_after_reactivation_v1"]}
    receipt.parent.mkdir(mode=0o755, exist_ok=True)
    root_directory(receipt.parent)
    with os.fdopen(os.open(receipt, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o644), "w") as stream:
        json.dump(record, stream, sort_keys=True)
        stream.write("\n")
        stream.flush()
        os.fsync(stream.fileno())
    print("Verified xrdp runtime installed: " + PACKAGE_VERSION)


if __name__ == "__main__":
    try:
        install("/tmp/vc-workspace-xrdp.deb", os.environ.get("VC_WORKSPACE_XRDP_PACKAGE_SHA256", ""))
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print("xrdp template installation failed: " + str(error), file=sys.stderr)
        sys.exit(1)

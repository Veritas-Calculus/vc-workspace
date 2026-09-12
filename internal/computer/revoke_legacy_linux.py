"""Fence both legacy spools without following user-controlled paths."""
import json
import os
import secrets
import sys

authority = json.loads(sys.argv[1])
if os.geteuid() != 0 or authority.get("state") != "revoked":
    raise SystemExit("root denial tombstone required")
payload = json.dumps(authority).encode()
flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC


def publish(product):
    directory = os.open("/", flags)
    try:
        for component in ("var", "lib", product, "computer"):
            parent = os.fstat(directory)
            if parent.st_uid != 0 or parent.st_mode & 0o022:
                raise PermissionError("untrusted control directory")
            try:
                child = os.open(component, flags, dir_fd=directory)
            except FileNotFoundError:
                return  # No spool exists for this generation.
            os.close(directory)
            directory = child
        owner = os.fstat(directory).st_uid
        if owner != 0 and owner < 1000:
            raise PermissionError("unexpected legacy spool owner")
        # Old templates gave this directory to vdi. Take ownership through
        # the verified descriptor, then publish only a denial. Old helpers
        # cannot replace the tombstone; no requests/responses are trusted.
        os.fchown(directory, 0, 0)
        os.fchmod(directory, 0o755)
        name = ".authority-revoked." + secrets.token_hex(16)
        staged = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW,
                         0o600, dir_fd=directory)
        try:
            with os.fdopen(staged, "wb") as stream:
                stream.write(payload)
                stream.flush()
                os.fchmod(stream.fileno(), 0o644)
                os.fsync(stream.fileno())
            os.replace(name, "authority.json", src_dir_fd=directory, dst_dir_fd=directory)
            os.fsync(directory)
        finally:
            try:
                os.unlink(name, dir_fd=directory)
            except FileNotFoundError:
                pass
    finally:
        os.close(directory)


publish("vc-workspace")
publish("vc-vdi")

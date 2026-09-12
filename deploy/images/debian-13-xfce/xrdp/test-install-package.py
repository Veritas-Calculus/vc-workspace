"""Pure boundary checks; actual dpkg/apt installation uses install-test.Dockerfile."""
import hashlib
import importlib.util
import os
import pathlib
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("xrdp_installer", pathlib.Path(__file__).with_name("install-package.py"))
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)


class PackageTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.path = pathlib.Path(self.directory.name) / "xrdp.deb"
        self.raw = b"package fixture"
        self.path.write_bytes(self.raw)
        self.digest = hashlib.sha256(self.raw).hexdigest()

    def test_exact_bytes(self):
        self.assertEqual(installer.read_package(self.path, self.digest), self.raw)

    def test_invalid_and_mismatched_digests(self):
        for digest in ("", None, self.digest.upper(), "0" * 64, self.digest + "\n", self.digest[:-1]):
            with self.subTest(digest=digest), self.assertRaises(ValueError):
                installer.read_package(self.path, digest)

    def test_changed_package(self):
        self.path.write_bytes(b"changed")
        with self.assertRaises(ValueError):
            installer.read_package(self.path, self.digest)

    def test_nonregular_and_oversized(self):
        for kind in ("symlink", "fifo", "directory", "empty", "oversized"):
            other = self.path.with_name(kind)
            if kind == "symlink": other.symlink_to(self.path)
            elif kind == "fifo": os.mkfifo(other)
            elif kind == "directory": other.mkdir()
            else:
                with other.open("wb") as stream:
                    if kind == "oversized": stream.truncate(installer.MAX_PACKAGE_BYTES + 1)
            with self.subTest(kind=kind), self.assertRaises((OSError, ValueError)):
                installer.read_package(other, self.digest)

    def test_metadata_boundaries(self):
        correct = {"Package": "xrdp", "Architecture": "amd64", "Version": installer.PACKAGE_VERSION}
        with patch.object(installer, "query", side_effect=lambda *args: correct[args[-1]]):
            installer.validate_metadata(self.path)
        for key, value in (("Package", "other"), ("Architecture", "arm64"),
                           ("Version", installer.BASE_VERSION), ("Version", "newer-unreviewed")):
            invalid = {**correct, key: value}
            with self.subTest(key=key, value=value), patch.object(installer, "query", side_effect=lambda *args: invalid[args[-1]]):
                with self.assertRaises(ValueError): installer.validate_metadata(self.path)


if __name__ == "__main__":
    unittest.main()

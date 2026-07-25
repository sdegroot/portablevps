"""Tests for destructive restore path validation."""

import subprocess
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parent.parent
HELPER = REPO_ROOT / "scripts" / "lib" / "restore-paths.sh"


def check_path(path: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [
            "bash",
            "-c",
            'source "$1"; canonical_restore_clear_path "$2"',
            "restore-path-test",
            str(HELPER),
            path,
        ],
        check=False,
        capture_output=True,
        text=True,
    )


class RestorePathTests(unittest.TestCase):
    def test_accepts_canonical_data_descendant(self):
        result = check_path("/data/postgres")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "/data/postgres")

    def test_accepts_canonical_var_lib_descendant(self):
        if Path("/var").is_symlink():
            self.skipTest("macOS exposes /var through a symlink; production NixOS does not")
        result = check_path("/var/lib/portablevps-backups/postgres")
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_rejects_parent_traversal(self):
        result = check_path("/data/app/../../etc")
        self.assertEqual(result.returncode, 78)
        self.assertIn("non-canonical", result.stderr)

    def test_rejects_protected_roots_and_unrelated_paths(self):
        for path in ("", "/", "/data", "/var/lib", "/etc/portablevps"):
            with self.subTest(path=path):
                self.assertEqual(check_path(path).returncode, 78)

    def test_rejects_relative_and_duplicate_separator_paths(self):
        for path in ("data/app", "/data//app", "/var/lib/app/"):
            with self.subTest(path=path):
                self.assertEqual(check_path(path).returncode, 78)


if __name__ == "__main__":
    unittest.main()

"""Unit tests for the password-manager secret resolver."""

import os
import subprocess
import sys
import unittest
from pathlib import Path
from unittest import mock


REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "scripts"))

from portablevps_cloud import secrets  # noqa: E402


class SecretsTests(unittest.TestCase):
    def test_literal_values_pass_through(self):
        self.assertEqual(secrets.resolve_secret("plain-token"), "plain-token")
        self.assertFalse(secrets.is_reference("plain-token"))

    def test_env_reference_reads_environment(self):
        with mock.patch.dict(os.environ, {"MY_TOKEN": "from-env"}, clear=False):
            self.assertTrue(secrets.is_reference("env://MY_TOKEN"))
            self.assertEqual(secrets.resolve_secret("env://MY_TOKEN"), "from-env")

    def test_env_reference_missing_variable_errors(self):
        with mock.patch.dict(os.environ, {}, clear=True):
            with self.assertRaises(secrets.SecretError):
                secrets.resolve_secret("env://ABSENT")

    def test_op_reference_invokes_op_read(self):
        with mock.patch.object(secrets.subprocess, "check_output", return_value="s3cr3t\n") as run:
            self.assertEqual(secrets.resolve_secret("op://Private/Hetzner/token"), "s3cr3t")
        run.assert_called_once_with(["op", "read", "op://Private/Hetzner/token"], text=True)

    def test_op_reference_missing_cli_is_clear_error(self):
        with mock.patch.object(secrets.subprocess, "check_output", side_effect=FileNotFoundError()):
            with self.assertRaises(secrets.SecretError) as raised:
                secrets.resolve_secret("op://Private/Hetzner/token")
        self.assertIn("op", str(raised.exception))

    def test_op_reference_failure_wraps_error(self):
        with mock.patch.object(
            secrets.subprocess,
            "check_output",
            side_effect=subprocess.CalledProcessError(1, ["op"]),
        ):
            with self.assertRaises(secrets.SecretError):
                secrets.resolve_secret("op://Private/Hetzner/token")

    def test_resolve_mapping_only_touches_references(self):
        with mock.patch.object(secrets.subprocess, "check_output", return_value="resolved\n"):
            result = secrets.resolve_mapping({
                "PLAIN": "keep",
                "HCLOUD_TOKEN": "op://Private/Hetzner/token",
            })
        self.assertEqual(result, {"PLAIN": "keep", "HCLOUD_TOKEN": "resolved"})


if __name__ == "__main__":
    unittest.main()

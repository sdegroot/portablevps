"""Unit tests for cloud lifecycle state and provider adapters."""

import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


REPO_ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(REPO_ROOT / "scripts"))

from portablevps_cloud.hetzner import HetznerAdapter  # noqa: E402
from portablevps_cloud.lifecycle import (  # noqa: E402
    LifecycleContext,
    LifecycleError,
    ProviderSpec,
    ServerSpec,
    UnsupportedProviderAdapter,
    require_delete_confirmation,
)
from portablevps_cloud.state import StateStore  # noqa: E402


def context(*, env=None, placement=None):
    return LifecycleContext(
        server=ServerSpec(
            name="test-vps",
            provider="hetzner",
            hostname="test-vps",
            netbird_name="test-vps",
            placement=placement or {},
        ),
        provider=ProviderSpec(
            name="hetzner",
            values={
                "defaultLocation": "nbg1",
                "defaultServerType": "cx22",
                "defaultImage": "ubuntu-24.04",
            },
        ),
        role="candidate",
        repo_root=REPO_ROOT,
        state_dir=REPO_ROOT / ".local/cloud-state",
        env={"HCLOUD_TOKEN": "token"} if env is None else env,
    )


class LifecycleTests(unittest.TestCase):
    def test_state_store_keeps_roles_in_one_server_document(self):
        with tempfile.TemporaryDirectory() as temp:
            store = StateStore(Path(temp))
            store.set_role("test-vps", "active", {"provider_server_id": "1"})
            store.set_role("test-vps", "candidate", {"provider_server_id": "2"})

            self.assertEqual(store.get_role("test-vps", "active")["provider_server_id"], "1")
            self.assertEqual(store.get_role("test-vps", "candidate")["provider_server_id"], "2")

    def test_state_store_save_leaves_only_the_document(self):
        with tempfile.TemporaryDirectory() as temp:
            store = StateStore(Path(temp))
            store.set_role("test-vps", "active", {"provider_server_id": "1"})

            server_dir = Path(temp) / "servers"
            self.assertEqual(
                sorted(path.name for path in server_dir.iterdir()),
                ["test-vps.json"],
            )

    def test_state_store_lock_serializes_role_updates(self):
        with tempfile.TemporaryDirectory() as temp:
            store = StateStore(Path(temp))
            with store.lock("test-vps"):
                store.set_role("test-vps", "active", {"provider_server_id": "1"})

            self.assertEqual(store.get_role("test-vps", "active")["provider_server_id"], "1")

    def test_state_store_refuses_role_overwrite(self):
        with tempfile.TemporaryDirectory() as temp:
            store = StateStore(Path(temp))
            store.set_role("test-vps", "active", {"provider_server_id": "1"})

            with self.assertRaises(LifecycleError) as raised:
                store.set_role("test-vps", "active", {"provider_server_id": "2"})

        self.assertIn("already has active lifecycle state", str(raised.exception))

    def test_delete_confirmation_requires_provider_server_id(self):
        require_delete_confirmation({"provider_server_id": "123"}, "123")

        with self.assertRaises(LifecycleError):
            require_delete_confirmation({"provider_server_id": "123"}, "wrong")

    def test_unsupported_provider_adapter_fails_clearly(self):
        adapter = UnsupportedProviderAdapter("netcup")

        with self.assertRaises(LifecycleError) as raised:
            adapter.preflight(context())

        self.assertIn("not implemented for provider: netcup", str(raised.exception))

    def test_hetzner_preflight_requires_token(self):
        adapter = HetznerAdapter()

        with self.assertRaises(LifecycleError) as raised:
            adapter.preflight(context(env={}))

        self.assertIn("HCLOUD_TOKEN is required", str(raised.exception))

    def test_hetzner_create_server_payload_and_normalized_state(self):
        adapter = HetznerAdapter()
        calls = []

        def fake_request(_context, method, path, *, payload=None):
            calls.append((method, path, payload))
            if method == "GET" and path.startswith("/ssh_keys"):
                return {"ssh_keys": []}
            if method == "POST" and path == "/ssh_keys":
                return {"ssh_key": {"id": 42}}
            if method == "POST" and path == "/servers":
                return {"server": {"id": 123}, "action": {"id": 9}}
            if method == "GET" and path == "/actions/9":
                return {"action": {"status": "success"}}
            if method == "GET" and path == "/servers/123":
                return {
                    "server": {
                        "id": 123,
                        "name": "test-vps-candidate",
                        "status": "running",
                        "rescue_enabled": False,
                        "public_net": {
                            "ipv4": {"ip": "203.0.113.10"},
                            "ipv6": {"ip": "2001:db8::1"},
                        },
                    }
                }
            raise AssertionError((method, path, payload))

        with tempfile.TemporaryDirectory() as temp:
            public_key = Path(temp) / "admin.pub"
            public_key.write_text("ssh-ed25519 AAA test\n", encoding="utf-8")
            with mock.patch.object(adapter, "_request", side_effect=fake_request):
                resource = adapter.create_server(context(), public_key_path=public_key)

        create_call = next(call for call in calls if call[0] == "POST" and call[1] == "/servers")
        self.assertEqual(create_call[2]["name"], "test-vps-candidate")
        self.assertEqual(create_call[2]["server_type"], "cx22")
        self.assertEqual(create_call[2]["location"], "nbg1")
        self.assertEqual(create_call[2]["image"], "ubuntu-24.04")
        self.assertEqual(create_call[2]["ssh_keys"], [42])
        self.assertEqual(create_call[2]["labels"]["portablevps-role"], "candidate")
        self.assertEqual(resource["provider_server_id"], "123")
        self.assertEqual(resource["public_ipv4"], "203.0.113.10")


if __name__ == "__main__":
    unittest.main()

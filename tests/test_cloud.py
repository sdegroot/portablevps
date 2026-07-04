"""Unit tests for the host-side cloud orchestration CLI helpers."""

import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


REPO_ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("cloud", REPO_ROOT / "scripts/cloud.py")
cloud = importlib.util.module_from_spec(spec)
assert spec.loader is not None
sys.modules["cloud"] = cloud
spec.loader.exec_module(cloud)

# Pin the CLI's project root to this repository so tests are independent of the
# working directory (the CLI otherwise defaults REPO_ROOT to the cwd).
cloud.REPO_ROOT = REPO_ROOT


class CloudTests(unittest.TestCase):
    def setUp(self):
        self.tempdir = tempfile.TemporaryDirectory()
        self.addCleanup(self.tempdir.cleanup)
        self.server_registry = Path(self.tempdir.name) / "servers.json"
        self.server_registry.write_text(
            json.dumps({
                "test-vps": {
                    "name": "test-vps",
                    "placement": {"provider": "hetzner"},
                    "hostname": "test-vps",
                    "netbirdName": "test-vps",
                    "backupRepository": "s3://example/test-vps",
                }
            }),
            encoding="utf-8",
        )
        self.server_env = {"SERVER_REGISTRY": str(self.server_registry)}

    def test_provider_defaults_are_loaded_from_registry(self):
        provider = cloud.require_provider("hetzner")

        self.assertEqual(provider.name, "hetzner")
        self.assertEqual(provider.default_disk, "/dev/sda")
        self.assertEqual(provider.default_target_user, "root")
        self.assertEqual(provider.default_kexec_extra_flags, "--kexec-syscall")

    def test_legacy_provider_registry_file_can_still_be_loaded(self):
        with tempfile.TemporaryDirectory() as temp:
            registry = Path(temp) / "providers.json"
            registry.write_text(
                json.dumps({"providers": {"example": {"defaultDisk": "/dev/vda"}}}),
                encoding="utf-8",
            )
            with mock.patch.dict(os.environ, {"PROVIDER_REGISTRY": str(registry)}, clear=False):
                providers = cloud.load_providers()

        self.assertEqual(providers["example"].default_disk, "/dev/vda")

    def test_unknown_provider_fails_with_usage_error(self):
        with self.assertRaises(cloud.CloudError) as raised:
            cloud.require_provider("unknown")

        self.assertEqual(raised.exception.exit_code, 64)
        self.assertIn("unsupported provider", str(raised.exception))

    def test_servers_are_loaded_from_registry(self):
        with mock.patch.dict(os.environ, self.server_env, clear=False):
            server = cloud.require_server("test-vps")

        self.assertEqual(server.name, "test-vps")
        self.assertEqual(server.provider_name, "hetzner")

    def test_unknown_server_fails_with_usage_error(self):
        with mock.patch.dict(os.environ, self.server_env, clear=False):
            with self.assertRaises(cloud.CloudError) as raised:
                cloud.require_server("missing")

        self.assertEqual(raised.exception.exit_code, 64)
        self.assertIn("unsupported server", str(raised.exception))

    def test_legacy_deployment_registry_can_still_be_loaded(self):
        with mock.patch.dict(os.environ, {"DEPLOYMENT_REGISTRY": str(self.server_registry)}, clear=False):
            server = cloud.require_server("test-vps")

        self.assertEqual(server.provider_name, "hetzner")

    def test_profile_name_tracks_restore_mode(self):
        deployment = cloud.Deployment("test-vps", {"provider": "hetzner"})

        self.assertEqual(cloud.profile_name(deployment, "false"), "test-vps")
        self.assertEqual(cloud.profile_name(deployment, "true"), "test-vps-restore")
        self.assertEqual(cloud.profile_name(deployment, "1"), "test-vps-restore")

    def test_provider_target_uses_default_target_user(self):
        provider = cloud.Provider("leaseweb", {"defaultTargetUser": "rescue"})

        self.assertEqual(cloud.provider_target(provider, "203.0.113.10"), "rescue@203.0.113.10")

    def test_kexec_flags_can_be_overridden_by_environment(self):
        provider = cloud.Provider("hetzner", {"defaultKexecExtraFlags": "--kexec-syscall"})
        with mock.patch.dict(os.environ, {"KEXEC_EXTRA_FLAGS": "--custom"}, clear=False):
            self.assertEqual(cloud.kexec_extra_flags(provider), "--custom")

    def test_env_or_treats_empty_value_as_default(self):
        with mock.patch.dict(os.environ, {"DISK": ""}, clear=False):
            self.assertEqual(cloud.env_or("DISK", "auto"), "auto")

    def test_install_uses_provider_disk_when_taskfile_passes_empty_disk(self):
        env = {
            **self.server_env,
            "SERVER": "test-vps",
            "TARGET": "root@203.0.113.20",
            "DISK": "",
        }

        with mock.patch.dict(os.environ, env, clear=False):
            with mock.patch.object(cloud, "install_cloud") as install_cloud:
                cloud.command_install(mock.Mock())

        self.assertEqual(install_cloud.call_args.kwargs["disk"], "/dev/sda")

    def test_disk_path_validation_accepts_device_paths_only(self):
        cloud.require_disk_path("/dev/sda")
        cloud.require_disk_path("/dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_drive")

        with self.assertRaises(cloud.CloudError):
            cloud.require_disk_path("sda")
        with self.assertRaises(cloud.CloudError):
            cloud.require_disk_path("/tmp/disk")

    def test_destroy_confirmation_must_match_host(self):
        cloud.require_destroy_confirmation("198.51.100.10", "198.51.100.10")

        with self.assertRaises(cloud.CloudError) as raised:
            cloud.require_destroy_confirmation("198.51.100.10", "")

        self.assertEqual(raised.exception.exit_code, 64)

    def test_promote_confirmation_must_match_server(self):
        server = cloud.Server("test-vps", {"placement": {"provider": "hetzner"}})
        cloud.require_promote_confirmation(server, "test-vps")

        with self.assertRaises(cloud.CloudError) as raised:
            cloud.require_promote_confirmation(server, "other")

        self.assertEqual(raised.exception.exit_code, 64)

    def test_safe_nixos_name_is_hostname_safe(self):
        self.assertEqual(
            cloud.safe_nixos_name("portablevps Hetzner Restore 203.0.113.10"),
            "portablevps-hetzner-restore-203-0-113-10",
        )

    def test_restore_rehearsal_default_names_are_derived_from_restore_host(self):
        deployment = cloud.Deployment("test-vps", {"provider": "hetzner"})

        self.assertEqual(
            cloud.restore_host_name(deployment, "203.0.113.10"),
            "test-vps-restore-203-0-113-10",
        )
        self.assertEqual(
            cloud.restore_netbird_name(deployment, "203.0.113.10"),
            "test-vps-restore-203-0-113-10",
        )

    def test_resolve_age_key_prefers_explicit_env(self):
        with mock.patch.dict(os.environ, {"SOPS_AGE_KEY_FILE": "/custom/key.txt"}, clear=False):
            self.assertEqual(cloud.resolve_age_key("test-vps"), "/custom/key.txt")

    def test_resolve_age_key_uses_per_server_key_when_present(self):
        project = Path(self.tempdir.name) / "project"
        key = project / ".local/sops/servers/test-vps/age-key.txt"
        key.parent.mkdir(parents=True, exist_ok=True)
        key.write_text("AGE-SECRET-KEY-TEST\n", encoding="utf-8")
        with mock.patch.object(cloud, "REPO_ROOT", project):
            with mock.patch.dict(os.environ, {"SOPS_AGE_KEY_FILE": ""}, clear=False):
                self.assertEqual(
                    cloud.resolve_age_key("test-vps"),
                    ".local/sops/servers/test-vps/age-key.txt",
                )

    def test_resolve_age_key_falls_back_to_shared_key(self):
        project = Path(self.tempdir.name) / "empty-project"
        project.mkdir(parents=True, exist_ok=True)
        with mock.patch.object(cloud, "REPO_ROOT", project):
            with mock.patch.dict(os.environ, {"SOPS_AGE_KEY_FILE": ""}, clear=False):
                self.assertEqual(cloud.resolve_age_key("test-vps"), ".local/sops/age-key.txt")

    def test_ssh_args_resolve_relative_identity_from_repo_root(self):
        args = cloud.ssh_args("root@203.0.113.1", port="2222", identity=".local/key")

        self.assertEqual(args[0], "ssh")
        self.assertIn("2222", args)
        self.assertIn(str(REPO_ROOT / ".local/key"), args)
        self.assertEqual(args[-1], "root@203.0.113.1")

    def test_wait_admin_ssh_retries_until_success(self):
        attempts = {"count": 0}

        def fake_admin_ssh(*_args, **_kwargs):
            attempts["count"] += 1
            if attempts["count"] == 1:
                raise subprocess.CalledProcessError(255, ["ssh"])
            return None

        with mock.patch.object(cloud, "admin_ssh", side_effect=fake_admin_ssh):
            with mock.patch.object(cloud.time, "sleep"):
                cloud.wait_admin_ssh("203.0.113.1", port="22", admin_key=".local/key")

        self.assertEqual(attempts["count"], 2)

    def test_switch_normal_profile_can_be_skipped(self):
        with mock.patch.object(cloud, "run") as run:
            cloud.switch_normal_profile(
                cloud.Deployment("test-vps", {"provider": "hetzner"}),
                "203.0.113.1",
                ".local/key",
                "false",
            )

        run.assert_not_called()

    def test_switch_normal_profile_uses_temp_flake_for_identity_overrides(self):
        original_tempdir = tempfile.TemporaryDirectory
        with tempfile.TemporaryDirectory() as temp:
            temp_path = Path(temp)

            def fake_tempdir():
                return original_tempdir(dir=temp_path)

            with mock.patch.object(cloud.tempfile, "TemporaryDirectory", side_effect=fake_tempdir):
                with mock.patch.object(cloud, "copy_repo_to_temp") as copy_repo:
                    with mock.patch.object(cloud, "run") as run:
                        cloud.switch_normal_profile(
                            cloud.Deployment("test-vps", {"provider": "hetzner"}),
                            "203.0.113.1",
                            ".local/key",
                            "true",
                            override_hostname="restore-a",
                            override_netbird_name="restore-peer-a",
                        )

                    copy_repo.assert_called_once()
                    run.assert_called_once()
                    flake_arg_index = run.call_args.args[0].index("--flake") + 1
                    self.assertIn("#test-vps", run.call_args.args[0][flake_arg_index])

    def test_write_cloud_identity_override(self):
        with tempfile.TemporaryDirectory() as temp:
            tmpdir = Path(temp)
            cloud.write_cloud_identity_override(
                tmpdir,
                override_hostname="restore-a",
                override_netbird_name="restore-peer-a",
            )
            override_text = (tmpdir / "cloud-override.nix").read_text(encoding="utf-8")

        self.assertIn('networking.hostName = "restore-a";', override_text)
        self.assertIn('portablevps.network.name = "restore-peer-a";', override_text)

    def test_write_proxy_smoke_override(self):
        with tempfile.TemporaryDirectory() as temp:
            tmpdir = Path(temp)
            cloud.write_proxy_smoke_override(
                tmpdir,
                domain="proxy-test.portablevps.int",
                visibility="internal",
            )
            override_text = (tmpdir / "cloud-override.nix").read_text(encoding="utf-8")

        self.assertIn("portablevps.proxy = {", override_text)
        self.assertIn("acme.enable = false;", override_text)
        self.assertIn("enable = true;", override_text)
        self.assertIn('domain = "proxy-test.portablevps.int";', override_text)
        self.assertIn('visibility = "internal";', override_text)

    def test_proxy_smoke_test_switches_and_verifies_internal_route(self):
        env = {
            **self.server_env,
            "SERVER": "test-vps",
            "HOST": "100.85.5.203",
        }

        with mock.patch.dict(os.environ, env, clear=False):
            with mock.patch.object(cloud, "wait_admin_ssh") as wait_admin_ssh:
                with mock.patch.object(cloud, "copy_repo_to_temp") as copy_repo:
                    with mock.patch.object(cloud, "switch_profile") as switch_profile:
                        with mock.patch.object(cloud, "curl_proxy", return_value="portablevps proxy test ok") as curl_proxy:
                            cloud.command_proxy_smoke_test(mock.Mock())

        wait_admin_ssh.assert_called_once()
        copy_repo.assert_called_once()
        switch_profile.assert_called_once()
        curl_proxy.assert_called_once_with("proxy-test.portablevps.int", "100.85.5.203")

    def test_preflight_checks_local_config_and_optional_target(self):
        env = {
            **self.server_env,
            "SERVER": "test-vps",
            "HOST": "203.0.113.20",
        }

        with mock.patch.dict(os.environ, env, clear=False):
            with mock.patch.object(Path, "is_file", return_value=True):
                with mock.patch.object(cloud, "run") as run:
                    with mock.patch.object(cloud, "ssh") as ssh:
                        with mock.patch.object(cloud, "list_disks") as list_disks:
                            with mock.patch.object(cloud, "detect_disk", return_value="/dev/sda"):
                                cloud.command_preflight(mock.Mock())

        run.assert_called_once()
        ssh.assert_called_once()
        list_disks.assert_called_once()

    def test_preflight_requires_backup_repository(self):
        registry = Path(self.tempdir.name) / "servers-no-backup.json"
        registry.write_text(
            json.dumps({"test-vps": {"name": "test-vps", "placement": {"provider": "hetzner"}}}),
            encoding="utf-8",
        )
        env = {"SERVER_REGISTRY": str(registry), "SERVER": "test-vps"}

        with mock.patch.dict(os.environ, env, clear=False):
            with self.assertRaises(cloud.CloudError) as raised:
                cloud.command_preflight(mock.Mock())

        self.assertIn("backupRepository", str(raised.exception))

    def test_internal_netbird_records_from_plan_filters_zone(self):
        plan = {
            "domains": [
                {
                    "visibility": "internal",
                    "dns": {
                        "netbird": {
                            "type": "CNAME",
                            "name": "test.int.portablevps.io.",
                            "target": "test-vps.portablevps.int.",
                        }
                    },
                },
                {
                    "visibility": "netbird-edge",
                    "dns": {
                        "public": {
                            "type": "CNAME",
                            "name": "test.portablevps.io.",
                            "target": "eu1.netbird.services.",
                        }
                    },
                },
                {
                    "visibility": "internal",
                    "dns": {
                        "netbird": {
                            "type": "CNAME",
                            "name": "other.example.com.",
                            "target": "elsewhere.example.com.",
                        }
                    },
                },
            ]
        }

        records = cloud.internal_netbird_records_from_plan(plan, "int.portablevps.io")

        self.assertEqual(len(records), 1)
        self.assertEqual(records[0]["name"], "test.int.portablevps.io.")

    def test_netbird_upsert_record_updates_changed_record(self):
        calls = []

        def fake_request(method, path, *, token, payload=None):
            calls.append((method, path, payload))
            if method == "GET":
                return [
                    {
                        "id": "record-a",
                        "name": "test.int.portablevps.io",
                        "type": "CNAME",
                        "content": "old.example.com",
                        "ttl": 300,
                    }
                ]
            return {}

        record = {
            "type": "CNAME",
            "name": "test.int.portablevps.io.",
            "target": "test-vps.portablevps.int.",
        }
        with mock.patch.object(cloud, "netbird_request", side_effect=fake_request):
            status = cloud.netbird_upsert_record("token", "zone-a", record)

        self.assertEqual(status, "updated")
        self.assertEqual(calls[-1][0], "PUT")
        self.assertEqual(calls[-1][1], "/api/dns/zones/zone-a/records/record-a")
        self.assertEqual(
            calls[-1][2],
            {
                "name": "test.int.portablevps.io",
                "type": "CNAME",
                "content": "test-vps.portablevps.int",
                "ttl": 300,
            },
        )

    def test_netbird_upsert_record_rejects_type_conflict(self):
        def fake_request(method, _path, *, token, payload=None):
            if method == "GET":
                return [
                    {
                        "id": "record-a",
                        "name": "test.int.portablevps.io",
                        "type": "A",
                        "content": "100.85.5.203",
                        "ttl": 300,
                    }
                ]
            return {}

        record = {
            "type": "CNAME",
            "name": "test.int.portablevps.io.",
            "target": "test-vps.portablevps.int.",
        }
        with mock.patch.object(cloud, "netbird_request", side_effect=fake_request):
            with self.assertRaises(cloud.CloudError) as raised:
                cloud.netbird_upsert_record("token", "zone-a", record)

        self.assertIn("exists with type A", str(raised.exception))

    def test_restore_phase_requires_confirmation_for_restore_host(self):
        env = {
            **self.server_env,
            "PHASE": "restore",
            "SERVER": "test-vps",
            "SOURCE_HOST": "198.51.100.10",
            "RESTORE_HOST": "203.0.113.20",
            "CONFIRM_DESTROY": "198.51.100.10",
            "MARKER": "marker",
        }

        with mock.patch.dict(os.environ, env, clear=False):
            with self.assertRaises(cloud.CloudError) as raised:
                cloud.command_restore_test(mock.Mock())

        self.assertIn("203.0.113.20", str(raised.exception))

    def test_restore_phase_installs_restore_host_with_unique_identity(self):
        env = {
            **self.server_env,
            "PHASE": "restore",
            "SERVER": "test-vps",
            "SOURCE_HOST": "198.51.100.10",
            "RESTORE_HOST": "203.0.113.20",
            "CONFIRM_DESTROY": "203.0.113.20",
            "MARKER": "marker",
            "FINALIZE_NORMAL": "false",
        }

        with mock.patch.dict(os.environ, env, clear=False):
            with mock.patch.object(cloud, "ssh"):
                with mock.patch.object(cloud, "list_disks"):
                    with mock.patch.object(cloud, "detect_disk", return_value="/dev/sda"):
                        with mock.patch.object(cloud, "install_cloud") as install_cloud:
                            with mock.patch.object(cloud, "wait_admin_ssh"):
                                with mock.patch.object(cloud, "admin_ssh"):
                                    with mock.patch.object(cloud, "wait_postgres"):
                                        cloud.command_restore_test(mock.Mock())

        install_cloud.assert_called_once()
        self.assertEqual(
            install_cloud.call_args.kwargs["override_hostname"],
            "test-vps-restore-203-0-113-20",
        )
        self.assertEqual(
            install_cloud.call_args.kwargs["override_netbird_name"],
            "test-vps-restore-203-0-113-20",
        )

    def test_restore_phase_keeps_same_host_flow_without_identity_override(self):
        env = {
            **self.server_env,
            "PHASE": "restore",
            "SERVER": "test-vps",
            "HOST": "203.0.113.20",
            "CONFIRM_DESTROY": "203.0.113.20",
            "MARKER": "marker",
            "FINALIZE_NORMAL": "false",
        }

        with mock.patch.dict(os.environ, env, clear=False):
            with mock.patch.object(cloud, "ssh"):
                with mock.patch.object(cloud, "list_disks"):
                    with mock.patch.object(cloud, "detect_disk", return_value="/dev/sda"):
                        with mock.patch.object(cloud, "install_cloud") as install_cloud:
                            with mock.patch.object(cloud, "wait_admin_ssh"):
                                with mock.patch.object(cloud, "admin_ssh"):
                                    with mock.patch.object(cloud, "wait_postgres"):
                                        cloud.command_restore_test(mock.Mock())

        self.assertEqual(install_cloud.call_args.kwargs["override_hostname"], "")
        self.assertEqual(install_cloud.call_args.kwargs["override_netbird_name"], "")

    def test_restore_phase_accepts_explicit_identity_overrides(self):
        env = {
            **self.server_env,
            "PHASE": "restore",
            "SERVER": "test-vps",
            "SOURCE_HOST": "198.51.100.10",
            "RESTORE_HOST": "203.0.113.20",
            "RESTORE_HOSTNAME": "restore-a",
            "RESTORE_NETBIRD_NAME": "restore-peer-a",
            "CONFIRM_DESTROY": "203.0.113.20",
            "MARKER": "marker",
            "FINALIZE_NORMAL": "false",
        }

        with mock.patch.dict(os.environ, env, clear=False):
            with mock.patch.object(cloud, "ssh"):
                with mock.patch.object(cloud, "list_disks"):
                    with mock.patch.object(cloud, "detect_disk", return_value="/dev/sda"):
                        with mock.patch.object(cloud, "install_cloud") as install_cloud:
                            with mock.patch.object(cloud, "wait_admin_ssh"):
                                with mock.patch.object(cloud, "admin_ssh"):
                                    with mock.patch.object(cloud, "wait_postgres"):
                                        cloud.command_restore_test(mock.Mock())

        self.assertEqual(install_cloud.call_args.kwargs["override_hostname"], "restore-a")
        self.assertEqual(install_cloud.call_args.kwargs["override_netbird_name"], "restore-peer-a")

    def test_restore_candidate_defaults_to_restore_phase(self):
        env = {
            **self.server_env,
            "SERVER": "test-vps",
            "SOURCE_HOST": "198.51.100.10",
            "RESTORE_HOST": "203.0.113.20",
            "CONFIRM_DESTROY": "203.0.113.20",
            "MARKER": "marker",
            "FINALIZE_NORMAL": "false",
        }

        with mock.patch.dict(os.environ, env, clear=False):
            with mock.patch.object(cloud, "command_restore_test") as command_restore_test:
                cloud.command_restore_candidate(mock.Mock())
                self.assertEqual(os.environ["PHASE"], "restore")

        command_restore_test.assert_called_once()

    def test_promote_candidate_requires_source_offline(self):
        env = {
            **self.server_env,
            "SERVER": "test-vps",
            "CANDIDATE_HOST": "203.0.113.20",
            "CONFIRM_PROMOTE": "test-vps",
        }

        with mock.patch.dict(os.environ, env, clear=False):
            with self.assertRaises(cloud.CloudError) as raised:
                cloud.command_promote_candidate(mock.Mock())

        self.assertIn("SOURCE_OFFLINE=1", str(raised.exception))

    def test_promote_candidate_switches_to_stable_identity(self):
        env = {
            **self.server_env,
            "SERVER": "test-vps",
            "CANDIDATE_HOST": "203.0.113.20",
            "SOURCE_OFFLINE": "1",
            "CONFIRM_PROMOTE": "test-vps",
            "MARKER": "marker",
            "CLOUD_STATE_DIR": str(Path(self.tempdir.name) / "state"),
        }

        with mock.patch.dict(os.environ, env, clear=False):
            with mock.patch.object(cloud, "wait_admin_ssh") as wait_admin_ssh:
                with mock.patch.object(cloud, "switch_normal_profile") as switch_normal_profile:
                    with mock.patch.object(cloud, "admin_ssh") as admin_ssh:
                        with mock.patch.object(cloud, "wait_postgres") as wait_postgres:
                            cloud.command_promote_candidate(mock.Mock())

        self.assertEqual(wait_admin_ssh.call_count, 2)
        switch_normal_profile.assert_called_once()
        self.assertEqual(switch_normal_profile.call_args.kwargs["ssh_port"], "22")
        admin_ssh.assert_any_call(
            "203.0.113.20",
            "sudo verify-test-data.sh marker",
            port="22",
            admin_key=".local/ssh/cloud-admin_ed25519",
        )
        wait_postgres.assert_called_once()

    def promote_with_state(self, state_dir):
        env = {
            **self.server_env,
            "SERVER": "test-vps",
            "CANDIDATE_HOST": "203.0.113.20",
            "SOURCE_OFFLINE": "1",
            "CONFIRM_PROMOTE": "test-vps",
            "MARKER": "marker",
            "CLOUD_STATE_DIR": str(state_dir),
        }

        with mock.patch.dict(os.environ, env, clear=False):
            with mock.patch.object(cloud, "wait_admin_ssh"):
                with mock.patch.object(cloud, "switch_normal_profile"):
                    with mock.patch.object(cloud, "admin_ssh"):
                        with mock.patch.object(cloud, "wait_postgres"):
                            cloud.command_promote_candidate(mock.Mock())

    def test_promote_candidate_swaps_lifecycle_roles(self):
        state_dir = Path(self.tempdir.name) / "state-swap"
        store = cloud.StateStore(state_dir)
        store.set_role("test-vps", "active", {"provider_server_id": "1", "public_ipv4": "198.51.100.10"})
        store.set_role("test-vps", "candidate", {"provider_server_id": "2", "public_ipv4": "203.0.113.20"})

        self.promote_with_state(state_dir)

        self.assertEqual(store.get_role("test-vps", "active")["provider_server_id"], "2")
        self.assertEqual(store.get_role("test-vps", "candidate")["provider_server_id"], "1")

    def test_promote_candidate_without_previous_active_clears_candidate_role(self):
        state_dir = Path(self.tempdir.name) / "state-fresh"
        store = cloud.StateStore(state_dir)
        store.set_role("test-vps", "candidate", {"provider_server_id": "2", "public_ipv4": "203.0.113.20"})

        self.promote_with_state(state_dir)

        self.assertEqual(store.get_role("test-vps", "active")["provider_server_id"], "2")
        self.assertIsNone(store.get_role("test-vps", "candidate"))

    def test_promote_candidate_leaves_state_alone_on_host_mismatch(self):
        state_dir = Path(self.tempdir.name) / "state-mismatch"
        store = cloud.StateStore(state_dir)
        store.set_role("test-vps", "active", {"provider_server_id": "1", "public_ipv4": "198.51.100.10"})
        store.set_role("test-vps", "candidate", {"provider_server_id": "2", "public_ipv4": "203.0.113.99"})

        self.promote_with_state(state_dir)

        self.assertEqual(store.get_role("test-vps", "active")["provider_server_id"], "1")
        self.assertEqual(store.get_role("test-vps", "candidate")["provider_server_id"], "2")

    def test_detect_disk_returns_single_disk(self):
        with mock.patch.object(cloud, "ssh_capture", return_value="/dev/sda\n"):
            disk = cloud.detect_disk("root@203.0.113.10", port="22", identity="")

        self.assertEqual(disk, "/dev/sda")

    def test_detect_disk_fails_on_multiple_disks(self):
        with mock.patch.object(cloud, "ssh_capture", return_value="/dev/sda\n/dev/sdb\n"):
            with self.assertRaises(cloud.CloudError) as raised:
                cloud.detect_disk("root@203.0.113.10", port="22", identity="")

        self.assertIn("multiple disks", str(raised.exception))

    def test_verify_install_disk_requires_explicit_disk_on_multi_disk_targets(self):
        with mock.patch.object(cloud, "ssh_capture", return_value="/dev/sda\n/dev/sdb\n"):
            with self.assertRaises(cloud.CloudError) as raised:
                cloud.verify_install_disk(
                    "root@203.0.113.10", "/dev/sda", port="22", identity="", explicit=False
                )
            cloud.verify_install_disk(
                "root@203.0.113.10", "/dev/sda", port="22", identity="", explicit=True
            )

        self.assertIn("multiple disks", str(raised.exception))

    def test_verify_install_disk_rejects_disk_missing_on_target(self):
        with mock.patch.object(cloud, "ssh_capture", return_value="/dev/vda\n"):
            with self.assertRaises(cloud.CloudError) as raised:
                cloud.verify_install_disk(
                    "root@203.0.113.10", "/dev/sda", port="22", identity="", explicit=True
                )

        self.assertIn("not a disk on", str(raised.exception))

    def test_lifecycle_create_records_role_state(self):
        public_key = Path(self.tempdir.name) / "admin.pub"
        public_key.write_text("ssh-ed25519 AAA test\n", encoding="utf-8")
        state_dir = Path(self.tempdir.name) / "state"
        env = {
            **self.server_env,
            "SERVER": "test-vps",
            "ROLE": "candidate",
            "CLOUD_ADMIN_PUBKEY": str(public_key),
            "CLOUD_STATE_DIR": str(state_dir),
        }

        adapter = mock.Mock()
        adapter.preflight.return_value = None
        adapter.create_server.return_value = {
            "provider": "hetzner",
            "provider_server_id": "123",
            "public_ipv4": "203.0.113.10",
        }

        with mock.patch.dict(os.environ, env, clear=False):
            with mock.patch.object(cloud, "provider_adapter", return_value=adapter):
                cloud.command_lifecycle_create(mock.Mock())

        state = json.loads((state_dir / "servers/test-vps.json").read_text(encoding="utf-8"))
        self.assertEqual(state["resources"]["candidate"]["provider_server_id"], "123")
        adapter.create_server.assert_called_once()

    def test_lifecycle_create_refuses_existing_role_before_provider_call(self):
        public_key = Path(self.tempdir.name) / "admin.pub"
        public_key.write_text("ssh-ed25519 AAA test\n", encoding="utf-8")
        state_dir = Path(self.tempdir.name) / "state"
        store = cloud.StateStore(state_dir)
        store.set_role("test-vps", "active", {"provider_server_id": "123"})
        env = {
            **self.server_env,
            "SERVER": "test-vps",
            "ROLE": "active",
            "CLOUD_ADMIN_PUBKEY": str(public_key),
            "CLOUD_STATE_DIR": str(state_dir),
        }
        adapter = mock.Mock()

        with mock.patch.dict(os.environ, env, clear=False):
            with mock.patch.object(cloud, "provider_adapter", return_value=adapter):
                with self.assertRaises(cloud.CloudError) as raised:
                    cloud.command_lifecycle_create(mock.Mock())

        self.assertIn("already has active lifecycle state", str(raised.exception))
        adapter.create_server.assert_not_called()

    def test_lifecycle_delete_requires_provider_id_confirmation(self):
        state_dir = Path(self.tempdir.name) / "state"
        store = cloud.StateStore(state_dir)
        store.set_role("test-vps", "candidate", {"provider_server_id": "123"})
        env = {
            **self.server_env,
            "SERVER": "test-vps",
            "ROLE": "candidate",
            "CLOUD_STATE_DIR": str(state_dir),
            "CONFIRM_DELETE": "wrong",
        }

        with mock.patch.dict(os.environ, env, clear=False):
            with self.assertRaises(cloud.LifecycleError) as raised:
                cloud.command_lifecycle_delete(mock.Mock())

        self.assertIn("CONFIRM_DELETE=123", str(raised.exception))

    def test_lifecycle_delete_removes_role_state_after_provider_delete(self):
        state_dir = Path(self.tempdir.name) / "state"
        store = cloud.StateStore(state_dir)
        store.set_role("test-vps", "candidate", {"provider_server_id": "123"})
        env = {
            **self.server_env,
            "SERVER": "test-vps",
            "ROLE": "candidate",
            "CLOUD_STATE_DIR": str(state_dir),
            "CONFIRM_DELETE": "123",
        }
        adapter = mock.Mock()

        with mock.patch.dict(os.environ, env, clear=False):
            with mock.patch.object(cloud, "provider_adapter", return_value=adapter):
                cloud.command_lifecycle_delete(mock.Mock())

        self.assertIsNone(store.get_role("test-vps", "candidate"))
        adapter.delete_server.assert_called_once()

    def test_install_created_uses_lifecycle_state_host(self):
        state_dir = Path(self.tempdir.name) / "state"
        store = cloud.StateStore(state_dir)
        store.set_role("test-vps", "candidate", {
            "provider_server_id": "123",
            "public_ipv4": "203.0.113.10",
        })
        env = {
            **self.server_env,
            "SERVER": "test-vps",
            "ROLE": "candidate",
            "CLOUD_STATE_DIR": str(state_dir),
        }

        with mock.patch.dict(os.environ, env, clear=False):
            with mock.patch.object(cloud, "install_cloud") as install_cloud:
                cloud.command_install_created(mock.Mock())

        self.assertEqual(install_cloud.call_args.kwargs["target"], "root@203.0.113.10")

    def test_restore_candidate_uses_candidate_state_when_restore_host_is_unset(self):
        state_dir = Path(self.tempdir.name) / "state"
        store = cloud.StateStore(state_dir)
        store.set_role("test-vps", "candidate", {
            "provider_server_id": "123",
            "public_ipv4": "203.0.113.10",
        })
        env = {
            **self.server_env,
            "SERVER": "test-vps",
            "CLOUD_STATE_DIR": str(state_dir),
        }

        with mock.patch.dict(os.environ, env, clear=False):
            with mock.patch.object(cloud, "command_restore_test") as command_restore_test:
                cloud.command_restore_candidate(mock.Mock())
                self.assertEqual(os.environ["RESTORE_HOST"], "203.0.113.10")

        command_restore_test.assert_called_once()


if __name__ == "__main__":
    unittest.main()

#!/usr/bin/env python3
"""Host-side cloud install and restore orchestration."""

from __future__ import annotations

import argparse
import json
import os
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path


SCRIPT_DIR = Path(__file__).resolve().parent
# Where the portablevps tool's own code and bundled data (providers, shell
# helpers) live.
CODE_ROOT = SCRIPT_DIR.parent
# The consumer repository the CLI operates on: its servers flake, keys,
# secrets, and .local state. Defaults to the current working directory so the
# CLI runs from inside a consumer repo; override with PORTABLEVPS_PROJECT.
REPO_ROOT = Path(os.environ.get("PORTABLEVPS_PROJECT") or Path.cwd()).resolve()


def flake_root() -> Path:
    """Filesystem root copied for nixos-anywhere / nixos-rebuild.

    In a monorepo the consumer flake references portablevps by a relative path
    input, so the copied tree must include both. Consumers set
    PORTABLEVPS_FLAKE_ROOT to that shared root and PORTABLEVPS_FLAKE_SUBDIR to
    their own subdirectory; standalone consumers leave both unset.
    """
    return Path(os.environ.get("PORTABLEVPS_FLAKE_ROOT") or REPO_ROOT).resolve()


def flake_subdir() -> str:
    return os.environ.get("PORTABLEVPS_FLAKE_SUBDIR", "").strip("/")


def flake_base(copied_root: Path) -> Path:
    """The consumer flake directory inside a copied flake_root tree."""
    subdir = flake_subdir()
    return copied_root / subdir if subdir else copied_root
if str(SCRIPT_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPT_DIR))

from portablevps_cloud.lifecycle import (  # noqa: E402
    LifecycleContext,
    LifecycleError,
    ProviderSpec,
    ServerSpec,
    require_delete_confirmation,
    require_role,
)
from portablevps_cloud.providers import provider_adapter  # noqa: E402
from portablevps_cloud.secrets import (  # noqa: E402
    SecretError,
    resolve_mapping,
    resolve_secret,
)
from portablevps_cloud.state import StateStore  # noqa: E402


class CloudError(RuntimeError):
    def __init__(self, message: str, exit_code: int = 1) -> None:
        super().__init__(message)
        self.exit_code = exit_code


@dataclass(frozen=True)
class Provider:
    name: str
    values: dict[str, str]

    @property
    def default_disk(self) -> str:
        return self.values.get("defaultDisk", "")

    @property
    def default_target_user(self) -> str:
        return self.values.get("defaultTargetUser", "root")

    @property
    def default_kexec_extra_flags(self) -> str:
        return self.values.get("defaultKexecExtraFlags", "")

    @property
    def lifecycle_adapter(self) -> str:
        return self.values.get("lifecycleAdapter", self.name)


@dataclass(frozen=True)
class Deployment:
    name: str
    values: dict[str, str]

    @property
    def provider_name(self) -> str:
        placement = self.values.get("placement", {})
        if isinstance(placement, dict) and placement.get("provider"):
            return str(placement.get("provider", ""))
        return str(self.values.get("provider", ""))

    @property
    def backup_repository(self) -> str:
        return str(self.values.get("backupRepository", ""))

    @property
    def hostname(self) -> str:
        return str(self.values.get("hostname", self.name))

    @property
    def netbird_name(self) -> str:
        return str(self.values.get("netbirdName", self.hostname))

    @property
    def netbird_groups(self) -> list[str]:
        netbird = self.values.get("netbird", {})
        if not isinstance(netbird, dict):
            return []
        groups = netbird.get("groups", [])
        return [str(g) for g in groups] if isinstance(groups, list) else []


Server = Deployment


def env(name: str, default: str = "") -> str:
    return os.environ.get(name, default)


def env_or(name: str, default: str) -> str:
    return os.environ.get(name, "") or default


def repo_path(path: str) -> Path:
    candidate = Path(path)
    if candidate.is_absolute():
        return candidate
    return REPO_ROOT / candidate


def run(args: list[str], *, cwd: Path | None = None, env_vars: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        args,
        cwd=cwd,
        env={**os.environ, **(env_vars or {})},
        text=True,
        check=True,
    )


def capture(args: list[str]) -> str:
    return subprocess.check_output(args, text=True).strip()


def strip_trailing_dot(value: str) -> str:
    return value[:-1] if value.endswith(".") else value


def comma_list(value: str) -> list[str]:
    return [item.strip() for item in value.split(",") if item.strip()]


def truthy(value: str) -> bool:
    return value.lower() in {"1", "true", "yes"}


def read_env_file(path: Path) -> dict[str, str]:
    if not path.is_file():
        return {}
    values = {}
    for raw_line in path.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        values[key.strip()] = value.strip().strip('"').strip("'")
    return values


def per_server_age_key(server_name: str) -> str:
    return f".local/sops/servers/{server_name}/age-key.txt"


def resolve_age_key(server_name: str) -> str:
    """Age identity shipped to a host so it can decrypt its secrets.

    Prefers a per-server key (.local/sops/servers/<name>/age-key.txt); falls
    back to the shared bootstrap key for repositories that have not migrated.
    An explicit SOPS_AGE_KEY_FILE always wins.
    """
    explicit = env("SOPS_AGE_KEY_FILE", "")
    if explicit:
        return explicit
    per_server = per_server_age_key(server_name)
    if repo_path(per_server).is_file():
        return per_server
    return ".local/sops/age-key.txt"


def secret_env(name: str, default: str = "") -> str:
    """Read an environment variable, resolving password-manager references
    (e.g. op://vault/item/field) to their secret value."""
    return resolve_secret(env(name, default))


def provider_env(provider: Provider) -> dict[str, str]:
    local_values = read_env_file(REPO_ROOT / ".local/providers" / f"{provider.name}.env")
    # Provider credentials (e.g. HCLOUD_TOKEN) may be stored as password-manager
    # references in the provider env file or the environment; resolve them.
    return resolve_mapping({
        **local_values,
        **os.environ,
    })


def providers_registry_path() -> Path:
    """Provider metadata directory: an explicit override, else the consumer's
    own providers/ dir, else the tool's bundled providers."""
    override = env("PROVIDER_REGISTRY", "")
    if override:
        return repo_path(override)
    project_providers = REPO_ROOT / "providers"
    if project_providers.is_dir():
        return project_providers
    return CODE_ROOT / "providers"


def load_providers() -> dict[str, Provider]:
    registry_path = providers_registry_path()
    if registry_path.is_dir():
        providers: dict[str, Provider] = {}
        for provider_dir in sorted(path for path in registry_path.iterdir() if path.is_dir()):
            provider_file = provider_dir / "provider.json"
            if provider_file.is_file():
                providers[provider_dir.name] = Provider(
                    name=provider_dir.name,
                    values=json.loads(provider_file.read_text(encoding="utf-8")),
                )
        for provider_file in sorted(registry_path.glob("*.json")):
            providers[provider_file.stem] = Provider(
                name=provider_file.stem,
                values=json.loads(provider_file.read_text(encoding="utf-8")),
            )
        return providers

    with registry_path.open(encoding="utf-8") as handle:
        registry = json.load(handle)
    if "providers" in registry:
        return {
            name: Provider(name=name, values=values)
            for name, values in registry.get("providers", {}).items()
        }

    return {
        registry_path.stem: Provider(name=registry_path.stem, values=registry)
    }


def load_servers() -> dict[str, Server]:
    registry_path = env("SERVER_REGISTRY", env("DEPLOYMENT_REGISTRY", ""))
    if registry_path:
        raw = json.loads(repo_path(registry_path).read_text(encoding="utf-8"))
    else:
        raw = json.loads(
            subprocess.check_output([
                "nix",
                "--extra-experimental-features",
                "nix-command flakes",
                "eval",
                "--json",
                ".#serverInfo",
            ], cwd=REPO_ROOT, text=True).strip()
        )
    return {
        name: Server(name=name, values=values)
        for name, values in raw.items()
    }


def load_deployments() -> dict[str, Deployment]:
    return load_servers()


def load_netbird_config() -> dict:
    """Fleet-level NetBird intent from the consumer flake's `.#netbird` output
    (policies + disableDefaultPolicy). Empty when the consumer declares none."""
    override = env("NETBIRD_CONFIG", "")
    if override:
        return json.loads(repo_path(override).read_text(encoding="utf-8"))
    try:
        raw = subprocess.check_output([
            "nix", "--extra-experimental-features", "nix-command flakes",
            "eval", "--json", ".#netbird",
        ], cwd=REPO_ROOT, text=True, stderr=subprocess.DEVNULL).strip()
    except subprocess.CalledProcessError:
        return {}
    return json.loads(raw) if raw else {}


def require_provider(name: str) -> Provider:
    providers = load_providers()
    provider = providers.get(name)
    if provider is None:
        expected = ", ".join(sorted(providers))
        raise CloudError(f"error: unsupported provider: {name}; expected one of: {expected}", 64)
    return provider


def require_server(name: str) -> Server:
    servers = load_servers()
    server = servers.get(name)
    if server is None:
        expected = ", ".join(sorted(servers))
        raise CloudError(f"error: unsupported server: {name}; expected one of: {expected}", 64)
    if not server.provider_name:
        raise CloudError(f"error: server {name} does not declare a placement provider", 64)
    return server


def require_deployment(name: str) -> Deployment:
    return require_server(name)


def selected_server() -> Server:
    name = env("SERVER", env("DEPLOYMENT", ""))
    if not name:
        raise CloudError("error: SERVER is required", 64)
    return require_server(name)


def selected_deployment() -> Deployment:
    return selected_server()


def lifecycle_role() -> str:
    return require_role(env_or("ROLE", "active"))


def state_store() -> StateStore:
    return StateStore(repo_path(env_or("CLOUD_STATE_DIR", ".local/cloud-state")))


def lifecycle_context(server: Server, provider: Provider, *, role: str) -> LifecycleContext:
    placement = server.values.get("placement", {})
    if not isinstance(placement, dict):
        placement = {}
    return LifecycleContext(
        server=ServerSpec(
            name=server.name,
            provider=server.provider_name,
            hostname=server.hostname,
            netbird_name=server.netbird_name,
            placement=placement,
        ),
        provider=ProviderSpec(name=provider.name, values=provider.values),
        role=role,
        repo_root=REPO_ROOT,
        state_dir=state_store().root,
        env=provider_env(provider),
    )


def lifecycle_adapter(server: Server, provider: Provider):
    return provider_adapter(provider.lifecycle_adapter or server.provider_name)


def resource_host(resource: dict) -> str:
    return str(resource.get("public_ipv4") or resource.get("public_ipv6") or "")


def require_lifecycle_resource(server: Server, role: str) -> dict:
    resource = state_store().get_role(server.name, role)
    if resource is None:
        raise CloudError(f"error: no lifecycle state for SERVER={server.name} ROLE={role}", 64)
    return resource


def profile_name(deployment: Deployment, restore_mode: str) -> str:
    if restore_mode in {"1", "true", "yes"}:
        return f"{deployment.name}-restore"
    return deployment.name


def provider_target(provider: Provider, host: str) -> str:
    return f"{provider.default_target_user}@{host}"


def kexec_extra_flags(provider: Provider) -> str:
    value = env("KEXEC_EXTRA_FLAGS", "")
    if value:
        return value
    return provider.default_kexec_extra_flags


def require_disk_path(disk: str) -> None:
    if re.fullmatch(r"/dev/[A-Za-z0-9._/+:-]+", disk) is None:
        raise CloudError(f"error: unsupported disk path: {disk}", 64)


def require_destroy_confirmation(host: str, confirm_destroy: str) -> None:
    if confirm_destroy != host:
        raise CloudError(f"error: refusing to wipe/install {host} without CONFIRM_DESTROY={host}", 64)


def require_promote_confirmation(server: Server, confirm_promote: str) -> None:
    if confirm_promote != server.name:
        raise CloudError(f"error: refusing to promote without CONFIRM_PROMOTE={server.name}", 64)


def known_hosts_file() -> Path:
    """Operator-local known_hosts used for host-key verification of already
    installed hosts (TOFU). Kept under .local (git-ignored) alongside the admin
    key. Created on first use so ssh can pin a key with accept-new."""
    path = repo_path(".local/known_hosts")
    path.parent.mkdir(parents=True, exist_ok=True)
    return path


def ssh_args(target: str, *, port: str, identity: str = "", verify: bool = False) -> list[str]:
    # First-contact operations (rescue/kexec install, bootstrapping the admin
    # key) legitimately cannot know the host key yet, so they stay permissive.
    # Ongoing operations against an already-installed host set verify=True:
    # accept-new pins the key on first sight into a persistent known_hosts and
    # alarms if it ever changes, so a MITM cannot silently intercept later
    # sessions (including the one that ships the host age private key).
    if verify:
        host_key_opts = [
            "-o",
            "StrictHostKeyChecking=accept-new",
            "-o",
            f"UserKnownHostsFile={known_hosts_file()}",
        ]
    else:
        host_key_opts = [
            "-o",
            "StrictHostKeyChecking=no",
            "-o",
            "UserKnownHostsFile=/dev/null",
        ]
    args = [
        "ssh",
        "-p",
        port,
        "-o",
        "BatchMode=yes",
        "-o",
        "ConnectTimeout=10",
        *host_key_opts,
    ]
    if identity:
        args.extend(["-i", str(repo_path(identity)), "-o", "IdentitiesOnly=yes"])
    args.append(target)
    return args


def ssh(target: str, command: str, *, port: str, identity: str = "", verify: bool = False) -> subprocess.CompletedProcess[str]:
    return run([*ssh_args(target, port=port, identity=identity, verify=verify), command])


def ssh_capture(target: str, command: str, *, port: str, identity: str = "", verify: bool = False) -> str:
    return capture([*ssh_args(target, port=port, identity=identity, verify=verify), command])


def admin_ssh(host: str, command: str, *, port: str, admin_key: str) -> subprocess.CompletedProcess[str]:
    return ssh(f"admin@{host}", command, port=port, identity=admin_key, verify=True)


def admin_capture(host: str, command: str, *, port: str, admin_key: str) -> str:
    return ssh_capture(f"admin@{host}", command, port=port, identity=admin_key, verify=True)


def wait_admin_ssh(host: str, *, port: str, admin_key: str) -> None:
    print(f"wait: waiting for installed NixOS admin SSH on {host}", flush=True)
    for attempt in range(1, 121):
        try:
            admin_ssh(host, "true", port=port, admin_key=admin_key)
            print("wait: admin SSH is ready", flush=True)
            return
        except subprocess.CalledProcessError:
            if attempt == 120:
                raise CloudError(f"error: admin SSH did not become ready on {host}", 70)
            time.sleep(5)


def wait_postgres(host: str, *, port: str, admin_key: str) -> None:
    print(f"wait: waiting for PostgreSQL on {host}", flush=True)
    for attempt in range(1, 61):
        try:
            admin_ssh(host, "sudo systemctl is-active --quiet postgres.service", port=port, admin_key=admin_key)
            print("wait: PostgreSQL is active", flush=True)
            return
        except subprocess.CalledProcessError:
            if attempt == 60:
                print(f"error: PostgreSQL service did not become active on {host}", file=sys.stderr)
                try:
                    admin_ssh(host, "sudo systemctl status postgres.service --no-pager -l", port=port, admin_key=admin_key)
                except subprocess.CalledProcessError:
                    pass
                raise CloudError("error: PostgreSQL service did not become active", 71)
            time.sleep(5)


def ensure_admin_keypair(admin_key: str, admin_pubkey: str) -> None:
    if repo_path(admin_key).is_file() and repo_path(admin_pubkey).is_file():
        return
    run(
        [str(SCRIPT_DIR / "cloud-keygen.sh")],
        cwd=REPO_ROOT,
        env_vars={
            "CLOUD_ADMIN_KEY": str(repo_path(admin_key)),
            "CLOUD_ADMIN_PUBKEY": str(repo_path(admin_pubkey)),
        },
    )


def check_local_preflight(server: Server, provider: Provider, *, admin_key: str, admin_pubkey: str, age_key: str) -> None:
    print(f"server: {server.name}", flush=True)
    print(f"provider: {provider.name}", flush=True)
    print(f"hostname: {server.hostname}", flush=True)
    print(f"netbird name: {server.netbird_name}", flush=True)
    if not server.backup_repository:
        raise CloudError(f"error: server {server.name} does not declare backupRepository", 64)
    print(f"backup repository: {server.backup_repository}", flush=True)
    if not provider.default_disk:
        raise CloudError(f"error: provider {provider.name} does not declare defaultDisk", 64)
    print(f"default disk: {provider.default_disk}", flush=True)
    if not repo_path(admin_key).is_file() or not repo_path(admin_pubkey).is_file():
        raise CloudError("error: cloud admin keypair is missing; run: mise exec -- task cloud:keygen", 66)
    print(f"admin keypair: {repo_path(admin_key)} / {repo_path(admin_pubkey)}", flush=True)
    if not repo_path(age_key).is_file():
        raise CloudError(f"error: missing sops age key: {repo_path(age_key)}", 66)
    print(f"sops age key: {repo_path(age_key)}", flush=True)
    run([
        "nix",
        "--extra-experimental-features",
        "nix-command flakes",
        "eval",
        "--raw",
        f".#nixosConfigurations.{server.name}.config.networking.hostName",
    ], cwd=REPO_ROOT)
    print(f"flake: .#{server.name} evaluates", flush=True)


def preflight_target(
    provider: Provider,
    *,
    host: str,
    target: str,
    disk: str,
    ssh_port: str,
    root_identity: str,
) -> str:
    if not host and not target:
        print("target: skipped; set HOST or TARGET to check rescue SSH and disk discovery", flush=True)
        return disk
    target = target or provider_target(provider, host)
    print(f"target: checking SSH to {target}", flush=True)
    ssh(target, "true", port=ssh_port, identity=root_identity)
    print(f"target: available disks on {target}", flush=True)
    list_disks(target, port=ssh_port, identity=root_identity)
    if disk == "auto":
        disk = detect_disk(target, port=ssh_port, identity=root_identity)
    if not disk:
        raise CloudError("error: could not detect install disk; set DISK=/dev/...", 66)
    require_disk_path(disk)
    print(f"target disk: {disk}", flush=True)
    return disk


def list_disks(target: str, *, port: str, identity: str) -> None:
    ssh(target, "lsblk -dnpo NAME,SIZE,TYPE,MODEL", port=port, identity=identity)


def list_target_disks(target: str, *, port: str, identity: str) -> list[str]:
    output = ssh_capture(
        target,
        "lsblk -dnpo NAME,TYPE | awk '$2 == \"disk\" { print $1 }'",
        port=port,
        identity=identity,
    )
    return [line.strip() for line in output.splitlines() if line.strip()]


def detect_disk(target: str, *, port: str, identity: str) -> str:
    disks = list_target_disks(target, port=port, identity=identity)
    if len(disks) > 1:
        raise CloudError(
            f"error: multiple disks found on {target}: {', '.join(disks)}; set DISK=/dev/... explicitly",
            66,
        )
    return disks[0] if disks else ""


def verify_install_disk(target: str, disk: str, *, port: str, identity: str, explicit: bool) -> None:
    disks = list_target_disks(target, port=port, identity=identity)
    if not disks:
        raise CloudError(f"error: no disks found on {target}", 66)
    if disk not in disks:
        raise CloudError(
            f"error: {disk} is not a disk on {target}; available: {', '.join(disks)}; set DISK=/dev/...",
            66,
        )
    if len(disks) > 1 and not explicit:
        raise CloudError(
            f"error: {target} has multiple disks ({', '.join(disks)}); refusing the provider default, set DISK=/dev/... explicitly",
            66,
        )


def copy_repo_to_temp(tmpdir: Path) -> Path:
    """Copy the flake root (consumer plus, in a monorepo, the tool) into a
    tempdir so it can be mutated for install. Returns the consumer flake
    directory inside the copy."""
    def ignore(_directory: str, names: list[str]) -> set[str]:
        return {name for name in names if name in {".git", ".local", "result"}}

    shutil.copytree(flake_root(), tmpdir, dirs_exist_ok=True, ignore=ignore)
    return flake_base(tmpdir)


def vendor_path_inputs(base: Path, copied_root: Path) -> None:
    """Make the consumer flake self-contained for remote evaluation.

    In a monorepo the consumer references the tool as `path:../portablevps`.
    nixos-anywhere --build-on-remote evaluates the flake on the target in pure
    mode, where that sibling directory does not exist, so it fails with
    "access to absolute path ... is forbidden". Copy each `path:../<name>`
    input into the flake tree and rewrite the input to an in-tree relative
    path so the whole flake travels to the target as one self-contained unit.
    A standalone consumer (tool pinned from a registry) has no such input and
    is left unchanged.
    """
    flake_file = base / "flake.nix"
    if not flake_file.is_file():
        return
    text = flake_file.read_text(encoding="utf-8")
    names = sorted(set(re.findall(r"path:\.\./([A-Za-z0-9._-]+)", text)))

    def ignore(_directory: str, entries: list[str]) -> set[str]:
        return {e for e in entries if e in {".git", ".local", "result"}}

    changed = False
    for name in names:
        src = copied_root / name
        if not src.is_dir():
            continue
        shutil.copytree(src, base / ".vendor" / name, dirs_exist_ok=True, ignore=ignore)
        text = text.replace(f"path:../{name}", f"path:./.vendor/{name}")
        changed = True
    if not changed:
        return
    flake_file.write_text(text, encoding="utf-8")
    (base / "flake.lock").unlink(missing_ok=True)
    run(["nix", "--extra-experimental-features", "nix-command flakes", "flake", "lock", str(base)])


def install_cloud(
    deployment: Deployment,
    provider: Provider,
    *,
    target: str,
    disk: str,
    ssh_port: str,
    root_identity: str,
    restore_mode: str,
    admin_key: str,
    admin_pubkey: str,
    age_key: str,
    kexec_flags: str,
) -> None:
    if not target:
        raise CloudError("error: TARGET is required", 64)
    if not repo_path(admin_key).is_file() or not repo_path(admin_pubkey).is_file():
        raise CloudError("error: cloud admin keypair is missing; run: mise exec -- task cloud:keygen", 66)
    if not repo_path(age_key).is_file():
        raise CloudError(
            f"error: missing sops age key: {repo_path(age_key)}\nrun the documented secrets bootstrap before installing cloud hosts",
            66,
        )
    if disk.strip() in ("", "auto"):
        disk = detect_disk(target, port=ssh_port, identity=root_identity)
        if not disk:
            raise CloudError(f"error: could not detect an install disk on {target}; set DISK=/dev/...", 66)
        print(f"detected install disk: {disk}", flush=True)
    require_disk_path(disk)
    verify_install_disk(
        target,
        disk,
        port=ssh_port,
        identity=root_identity,
        explicit=bool(env("DISK", "")),
    )

    profile = profile_name(deployment, restore_mode)
    with tempfile.TemporaryDirectory() as temp:
        tmpdir = Path(temp)
        base = copy_repo_to_temp(tmpdir)
        # nixos-anywhere evaluates on the target; make the flake self-contained.
        vendor_path_inputs(base, tmpdir)
        # A cloud host takes its own static machine name from its server config;
        # only the install-time disk device is injected here.
        write_override_file(base, [f'  portablevps.cloud.diskDevice = "{disk}";'])
        target_age_dir = tmpdir / "extra-files/etc/sops/age"
        target_age_dir.mkdir(parents=True, exist_ok=True)
        shutil.copy2(repo_path(age_key), target_age_dir / "keys.txt")
        os.chmod(target_age_dir / "keys.txt", 0o400)

        print(f"installing .#{profile} on {target}", flush=True)
        print(f"target disk: {disk}", flush=True)
        print(f"admin login after reboot: ssh -i {repo_path(admin_key)} admin@HOST", flush=True)

        args = [
            "nix",
            "--extra-experimental-features",
            "nix-command flakes",
            "run",
            "github:nix-community/nixos-anywhere",
            "--",
            "--flake",
            f"{base}#{profile}",
            "--ssh-port",
            ssh_port,
            "--ssh-option",
            "StrictHostKeyChecking=no",
            "--ssh-option",
            "UserKnownHostsFile=/dev/null",
            "--extra-files",
            str(tmpdir / "extra-files"),
        ]
        if root_identity:
            args.extend(["-i", str(repo_path(root_identity))])
        if kexec_flags:
            args.extend(["--kexec-extra-flags", kexec_flags])
        # Build the system closure on the target rather than the operator
        # machine. Required when the operator's architecture differs from the
        # server's (e.g. an aarch64 macOS laptop installing an x86_64 VPS).
        # Set BUILD_ON_REMOTE=false to build locally instead.
        if env("BUILD_ON_REMOTE", "true") != "false":
            args.append("--build-on-remote")
        args.append(target)
        run(args)


def command_install(_args: argparse.Namespace) -> None:
    deployment = selected_deployment()
    provider = require_provider(deployment.provider_name)
    install_cloud(
        deployment,
        provider,
        target=env("TARGET", ""),
        disk=env("DISK", ""),
        ssh_port=env("SSH_PORT", "22"),
        root_identity=env("ROOT_IDENTITY", ""),
        restore_mode=env("RESTORE_MODE", "false"),
        admin_key=env("CLOUD_ADMIN_KEY", ".local/ssh/cloud-admin_ed25519"),
        admin_pubkey=env("CLOUD_ADMIN_PUBKEY", "keys/cloud-admin.pub"),
        age_key=resolve_age_key(deployment.name),
        kexec_flags=kexec_extra_flags(provider),
    )


def sshpass_prefix() -> list[str]:
    """sshpass invocation prefix; falls back to running it from nixpkgs so
    operators do not have to install sshpass separately."""
    if shutil.which("sshpass") is not None:
        return ["sshpass", "-e"]
    return ["nix", "--extra-experimental-features", "nix-command flakes", "run", "nixpkgs#sshpass", "--", "-e"]


def bootstrap_admin_key(
    host: str,
    *,
    login_user: str,
    ssh_port: str,
    password: str,
    initial_key: str,
    admin_pubkey: str,
) -> None:
    """Install the cloud admin public key onto an existing host using a one-off
    credential (a password, or a provider-issued initial private key), so the
    reinstall can proceed over key auth. The key is deduplicated and appended
    to the login user's authorized_keys."""
    pub_path = repo_path(admin_pubkey)
    if not pub_path.is_file():
        raise CloudError(f"error: admin public key not found: {pub_path}; run: task cloud:keygen", 66)
    pubkey = pub_path.read_text(encoding="utf-8").strip()
    target = f"{login_user}@{host}"
    remote_script = (
        "set -eu; mkdir -p ~/.ssh; chmod 700 ~/.ssh; "
        "touch ~/.ssh/authorized_keys; chmod 600 ~/.ssh/authorized_keys; "
        'key="$(cat)"; '
        'grep -qxF "$key" ~/.ssh/authorized_keys || printf "%s\\n" "$key" >> ~/.ssh/authorized_keys'
    )
    ssh_opts = [
        "-p", ssh_port,
        "-o", "StrictHostKeyChecking=no",
        "-o", "UserKnownHostsFile=/dev/null",
        "-o", "ConnectTimeout=20",
    ]
    env_vars = dict(os.environ)
    if password:
        cmd = sshpass_prefix() + ["ssh"] + ssh_opts + [target, remote_script]
        env_vars["SSHPASS"] = password
    elif initial_key:
        cmd = ["ssh", "-i", str(repo_path(initial_key)), "-o", "IdentitiesOnly=yes"] + ssh_opts + [target, remote_script]
    else:
        cmd = ["ssh"] + ssh_opts + [target, remote_script]
    print(f"adopt: installing admin key on {target}", flush=True)
    subprocess.run(cmd, input=pubkey, text=True, check=True, env=env_vars)


def command_adopt(_args: argparse.Namespace) -> None:
    server = selected_server()
    provider = require_provider(server.provider_name)
    host = env("HOST", "")
    if not host:
        raise CloudError("error: HOST is required", 64)
    require_destroy_confirmation(host, env("CONFIRM_DESTROY", ""))
    login_user = env_or("LOGIN_USER", "root")
    admin_key = env("CLOUD_ADMIN_KEY", ".local/ssh/cloud-admin_ed25519")
    admin_pubkey = env("CLOUD_ADMIN_PUBKEY", "keys/cloud-admin.pub")
    ssh_port = env("SSH_PORT", "22")

    ensure_admin_keypair(admin_key, admin_pubkey)
    bootstrap_admin_key(
        host,
        login_user=login_user,
        ssh_port=ssh_port,
        password=secret_env("PASSWORD", ""),
        initial_key=env("INITIAL_KEY", ""),
        admin_pubkey=admin_pubkey,
    )
    install_cloud(
        server,
        provider,
        target=f"{login_user}@{host}",
        disk=env("DISK", ""),
        ssh_port=ssh_port,
        root_identity=admin_key,
        restore_mode=env("RESTORE_MODE", "false"),
        admin_key=admin_key,
        admin_pubkey=admin_pubkey,
        age_key=resolve_age_key(server.name),
        kexec_flags=kexec_extra_flags(provider),
    )


def command_preflight(_args: argparse.Namespace) -> None:
    server = selected_server()
    provider = require_provider(server.provider_name)
    admin_key = env("CLOUD_ADMIN_KEY", ".local/ssh/cloud-admin_ed25519")
    admin_pubkey = env("CLOUD_ADMIN_PUBKEY", "keys/cloud-admin.pub")
    age_key = resolve_age_key(server.name)
    check_local_preflight(
        server,
        provider,
        admin_key=admin_key,
        admin_pubkey=admin_pubkey,
        age_key=age_key,
    )
    preflight_target(
        provider,
        host=env("HOST", ""),
        target=env("TARGET", ""),
        disk=env_or("DISK", "auto"),
        ssh_port=env("SSH_PORT", "22"),
        root_identity=env("ROOT_IDENTITY", ""),
    )
    print("PASS: cloud preflight checks completed", flush=True)


def command_lifecycle_preflight(_args: argparse.Namespace) -> None:
    server = selected_server()
    provider = require_provider(server.provider_name)
    role = lifecycle_role()
    adapter = lifecycle_adapter(server, provider)
    context = lifecycle_context(server, provider, role=role)
    adapter.preflight(context)
    resource = state_store().get_role(server.name, role)
    if resource:
        print(f"state: {server.name}/{role} -> {json.dumps(resource, sort_keys=True)}", flush=True)
    else:
        print(f"state: no local lifecycle state for {server.name}/{role}", flush=True)
    print("PASS: lifecycle preflight checks completed", flush=True)


def command_lifecycle_create(_args: argparse.Namespace) -> None:
    server = selected_server()
    provider = require_provider(server.provider_name)
    role = lifecycle_role()
    adapter = lifecycle_adapter(server, provider)
    context = lifecycle_context(server, provider, role=role)
    admin_pubkey = repo_path(env("CLOUD_ADMIN_PUBKEY", "keys/cloud-admin.pub"))
    if not admin_pubkey.is_file():
        raise CloudError("error: cloud admin public key is missing; run: mise exec -- task cloud:keygen", 66)
    store = state_store()
    with store.lock(server.name):
        existing = store.get_role(server.name, role)
        if existing is not None:
            provider_id = existing.get("provider_server_id", "")
            detail = f" for provider server {provider_id}" if provider_id else ""
            raise CloudError(f"error: {server.name} already has {role} lifecycle state{detail}", 64)
        adapter.preflight(context)
        resource = adapter.create_server(context, public_key_path=admin_pubkey)
        store.set_role(server.name, role, resource)
    print(f"created: {server.name}/{role} provider_id={resource['provider_server_id']} ipv4={resource.get('public_ipv4', '')}", flush=True)


def command_lifecycle_status(_args: argparse.Namespace) -> None:
    server = selected_server()
    provider = require_provider(server.provider_name)
    role = lifecycle_role()
    adapter = lifecycle_adapter(server, provider)
    context = lifecycle_context(server, provider, role=role)
    resource = require_lifecycle_resource(server, role)
    live = adapter.status(context, resource)
    state_store().set_role(server.name, role, live, overwrite=True)
    print(json.dumps({"local": resource, "provider": live}, indent=2, sort_keys=True), flush=True)


def command_lifecycle_rescue(_args: argparse.Namespace) -> None:
    server = selected_server()
    provider = require_provider(server.provider_name)
    role = lifecycle_role()
    adapter = lifecycle_adapter(server, provider)
    context = lifecycle_context(server, provider, role=role)
    resource = require_lifecycle_resource(server, role)
    admin_pubkey = repo_path(env("CLOUD_ADMIN_PUBKEY", "keys/cloud-admin.pub"))
    if not admin_pubkey.is_file():
        raise CloudError("error: cloud admin public key is missing; run: mise exec -- task cloud:keygen", 66)
    resource = adapter.enable_rescue(context, resource, public_key_path=admin_pubkey)
    resource = adapter.reboot(context, resource)
    state_store().set_role(server.name, role, resource, overwrite=True)
    host = resource_host(resource)
    print(f"rescue: {server.name}/{role} host={host} target={provider_target(provider, host)}", flush=True)


def command_lifecycle_delete(_args: argparse.Namespace) -> None:
    server = selected_server()
    provider = require_provider(server.provider_name)
    role = lifecycle_role()
    adapter = lifecycle_adapter(server, provider)
    context = lifecycle_context(server, provider, role=role)
    resource = require_lifecycle_resource(server, role)
    require_delete_confirmation(resource, env("CONFIRM_DELETE", ""))
    adapter.delete_server(context, resource)
    state_store().delete_role(server.name, role)
    print(f"deleted: {server.name}/{role} provider_id={resource['provider_server_id']}", flush=True)


def command_install_created(_args: argparse.Namespace) -> None:
    server = selected_server()
    provider = require_provider(server.provider_name)
    role = lifecycle_role()
    resource = require_lifecycle_resource(server, role)
    host = resource_host(resource)
    if not host:
        raise CloudError(f"error: lifecycle state for {server.name}/{role} has no public IP", 66)
    install_cloud(
        server,
        provider,
        target=provider_target(provider, host),
        disk=env("DISK", ""),
        ssh_port=env("SSH_PORT", "22"),
        root_identity=env("ROOT_IDENTITY", ""),
        restore_mode=env("RESTORE_MODE", "false"),
        admin_key=env("CLOUD_ADMIN_KEY", ".local/ssh/cloud-admin_ed25519"),
        admin_pubkey=env("CLOUD_ADMIN_PUBKEY", "keys/cloud-admin.pub"),
        age_key=resolve_age_key(server.name),
        kexec_flags=kexec_extra_flags(provider),
    )


def command_smoke_test(_args: argparse.Namespace) -> None:
    deployment = selected_deployment()
    provider = require_provider(deployment.provider_name)
    host = env("HOST", "")
    if not host:
        raise CloudError("error: HOST is required", 64)
    target = env("TARGET", "") or provider_target(provider, host)
    disk = env_or("DISK", "auto")
    ssh_port = env("SSH_PORT", "22")
    root_identity = env("ROOT_IDENTITY", "")
    admin_key = env("CLOUD_ADMIN_KEY", ".local/ssh/cloud-admin_ed25519")
    admin_pubkey = env("CLOUD_ADMIN_PUBKEY", "keys/cloud-admin.pub")

    require_destroy_confirmation(host, env("CONFIRM_DESTROY", ""))
    ensure_admin_keypair(admin_key, admin_pubkey)

    print(f"preflight: checking root SSH to {target}", flush=True)
    ssh(target, "true", port=ssh_port, identity=root_identity)
    print(f"preflight: available disks on {target}", flush=True)
    list_disks(target, port=ssh_port, identity=root_identity)
    if disk == "auto":
        disk = detect_disk(target, port=ssh_port, identity=root_identity)
    if not disk:
        raise CloudError("error: could not detect install disk; set DISK=/dev/...", 66)

    print(f"install: provider={provider.name} target={target} disk={disk}", flush=True)
    install_cloud(
        deployment,
        provider,
        target=target,
        disk=disk,
        ssh_port=ssh_port,
        root_identity=root_identity,
        restore_mode="false",
        admin_key=admin_key,
        admin_pubkey=admin_pubkey,
        age_key=resolve_age_key(deployment.name),
        kexec_flags=kexec_extra_flags(provider),
    )

    wait_admin_ssh(host, port=ssh_port, admin_key=admin_key)
    wait_postgres(host, port=ssh_port, admin_key=admin_key)
    print("verify: inserting and checking marker", flush=True)
    marker = admin_capture(host, "sudo insert-test-data.sh | tail -n 1", port=ssh_port, admin_key=admin_key)
    admin_ssh(host, f"sudo verify-test-data.sh {sh_quote(marker)}", port=ssh_port, admin_key=admin_key)
    print(f"PASS: {provider.name} server {host} installed and PostgreSQL marker verified: {marker}", flush=True)


def safe_file_name(host: str) -> str:
    return re.sub(r"[^A-Za-z0-9_.-]", "_", host)


def safe_nixos_name(value: str) -> str:
    name = re.sub(r"[^a-z0-9-]+", "-", value.lower())
    name = re.sub(r"-+", "-", name).strip("-")
    return name[:63] or "restore-host"


def sh_quote(value: str) -> str:
    return shlex.quote(value)


def marker_file(deployment: Deployment, source_host: str) -> Path:
    return REPO_ROOT / ".local/cloud-restore" / f"{deployment.name}-{safe_file_name(source_host)}.marker"


def write_override_file(base: Path, body_lines: list[str]) -> None:
    """Write cloud-override.nix at the consumer flake root inside a copied
    tree. mkFlake injects this file into every cloud host when present."""
    lines = ["{ ... }:", "", "{"] + body_lines + ["}"]
    (base / "cloud-override.nix").write_text("\n".join(lines) + "\n", encoding="utf-8")


def write_proxy_smoke_override(base: Path, *, domain: str, visibility: str) -> None:
    write_override_file(base, [
        "  portablevps.proxy = {",
        "    enable = true;",
        "    acme.enable = false;",
        "    testBackend = {",
        "      enable = true;",
        f'      domain = "{domain}";',
        f'      visibility = "{visibility}";',
        "    };",
        "  };",
    ])


def nix_ssh_opts(admin_key: str, ssh_port: str) -> str:
    # Used for nixos-rebuild --target-host/--build-host admin@host, i.e. an
    # already-installed host: verify the host key (TOFU) like the other admin
    # operations rather than blindly trusting it.
    return (
        f"-i {repo_path(admin_key)} "
        "-o IdentitiesOnly=yes "
        f"-p {ssh_port} "
        "-o StrictHostKeyChecking=accept-new "
        f"-o UserKnownHostsFile={known_hosts_file()}"
    )


def switch_normal_profile(
    deployment: Deployment,
    host: str,
    admin_key: str,
    finalize_normal: str,
    ssh_port: str = "22",
) -> None:
    if finalize_normal not in {"1", "true", "yes"}:
        print("finalize: skipping normal profile switch", flush=True)
        return

    # The host keeps its own static machine name across restore, so this is a
    # plain in-place switch from the restore profile to the normal one — no
    # temporary-identity override, no flake copy.
    print(f"finalize: switching restored host to .#{deployment.name}", flush=True)
    switch_profile(deployment, host, admin_key, ssh_port, flake_path=REPO_ROOT)


def switch_restore_profile(
    deployment: Deployment,
    host: str,
    admin_key: str,
    ssh_port: str = "22",
) -> None:
    print(f"prepare-target: switching {host} to .#{deployment.name}-restore", flush=True)
    switch_profile(
        Deployment(f"{deployment.name}-restore", deployment.values),
        host,
        admin_key,
        ssh_port,
        flake_path=REPO_ROOT,
    )


def switch_profile(
    deployment: Deployment,
    host: str,
    admin_key: str,
    ssh_port: str,
    *,
    flake_path: Path,
) -> None:
    run(
        [
            "nix",
            "--extra-experimental-features",
            "nix-command flakes",
            "run",
            "nixpkgs#nixos-rebuild",
            "--",
            "switch",
            "--flake",
            f"{flake_path}#{deployment.name}",
            "--target-host",
            f"admin@{host}",
            "--build-host",
            f"admin@{host}",
            # Modern nixos-rebuild flag names (were --use-remote-sudo / --fast).
            "--elevate=sudo",
            "--no-reexec",
        ],
        cwd=REPO_ROOT,
        env_vars={"NIX_SSHOPTS": nix_ssh_opts(admin_key, ssh_port)},
    )


def install_host_age_key(host: str, *, admin_key: str, ssh_port: str, age_key_path: str) -> None:
    """Ship a server's host age key to /etc/sops/age/keys.txt over admin+sudo,
    so an in-place switch to that server's config can decrypt its secrets."""
    key_path = repo_path(age_key_path)
    if not key_path.is_file():
        raise CloudError(f"error: missing host age key: {key_path}", 66)
    content = key_path.read_text(encoding="utf-8")
    remote = (
        "set -eu; sudo mkdir -p /etc/sops/age; "
        "sudo tee /etc/sops/age/keys.txt >/dev/null; "
        "sudo chmod 0400 /etc/sops/age/keys.txt; "
        "sudo chown root:root /etc/sops/age/keys.txt"
    )
    print(f"repurpose: installing {key_path.name} host age key on {host}", flush=True)
    subprocess.run(
        [*ssh_args(f"admin@{host}", port=ssh_port, identity=admin_key, verify=True), remote],
        input=content,
        text=True,
        check=True,
    )


def command_repurpose(_args: argparse.Namespace) -> None:
    """Switch an already-portablevps-managed host to a DIFFERENT logical server,
    in place, over the admin mesh SSH — no kexec/reinstall. Swaps the host age
    key to the target server's, optionally clears data directories the new app
    needs to initialise clean, then does a nixos-rebuild switch. NetBird and
    machine state persist, so the peer keeps its address and is just renamed."""
    server = selected_server()
    host = env("HOST", "")
    if not host:
        raise CloudError("error: HOST is required", 64)
    require_destroy_confirmation(host, env("CONFIRM_DESTROY", ""))
    admin_key = env("CLOUD_ADMIN_KEY", ".local/ssh/cloud-admin_ed25519")
    ssh_port = env("SSH_PORT", "22")
    reset_paths = comma_list(env("RESET_PATHS", ""))
    age_key = resolve_age_key(server.name)

    if not repo_path(age_key).is_file():
        raise CloudError(f"error: missing host age key for {server.name}: {repo_path(age_key)}", 66)
    for path in reset_paths:
        # A misplaced reset would wipe the system; keep it to app data roots.
        if not (path.startswith("/data/") or path.startswith("/var/lib/")):
            raise CloudError(f"error: refusing to reset a path outside /data or /var/lib: {path}", 64)

    # Confirm admin reachability before touching anything.
    admin_ssh(host, "true", port=ssh_port, admin_key=admin_key)

    # Stop the app stack so its data directories are free before we swap identity.
    print(f"repurpose: stopping apps.target on {host}", flush=True)
    admin_ssh(host, "sudo systemctl stop apps.target || true", port=ssh_port, admin_key=admin_key)

    # Swap the host age key so sops-nix can decrypt the target server's secrets
    # on the next activation.
    install_host_age_key(host, admin_key=admin_key, ssh_port=ssh_port, age_key_path=age_key)

    # Clear data directories the new app must own from scratch (e.g. a postgres
    # cluster whose database/user differ from the outgoing app's).
    for path in reset_paths:
        print(f"repurpose: clearing {path} on {host}", flush=True)
        admin_ssh(
            host,
            f"sudo find {shlex.quote(path)} -mindepth 1 -maxdepth 1 -exec rm -rf {{}} +",
            port=ssh_port,
            admin_key=admin_key,
        )

    # In-place switch to the target server config (built on the remote so a
    # different-arch operator can drive it).
    print(f"repurpose: switching {host} to .#{server.name}", flush=True)
    switch_profile(server, host, admin_key, ssh_port, flake_path=REPO_ROOT)

    print(f"repurpose: {host} now runs .#{server.name}", flush=True)
    print("next steps:", flush=True)
    print(f"  - rename the NetBird peer to '{server.netbird_name}' in the console (in-place keeps the old name)", flush=True)
    print(f"  - mise exec -- task cloud:netbird-dns-sync SERVER={server.name}  (repoint internal DNS)", flush=True)


def command_deploy(_args: argparse.Namespace) -> None:
    """Deploy the current committed flake to an already-running, healthy host:
    an in-place `nixos-rebuild switch` over the admin mesh SSH, built on the
    remote. For routine config updates to a live server — no reinstall, and no
    identity or data changes (unlike repurpose). Requires HOST, the running
    host's reachable admin address (e.g. its mesh FQDN)."""
    server = selected_server()
    host = env("HOST", "")
    if not host:
        raise CloudError("error: HOST is required (the running host's admin address, e.g. its mesh FQDN)", 64)
    admin_key = env("CLOUD_ADMIN_KEY", ".local/ssh/cloud-admin_ed25519")
    ssh_port = env("SSH_PORT", "22")

    # Confirm admin reachability before building and switching.
    print(f"deploy: checking admin SSH to {host}", flush=True)
    admin_ssh(host, "true", port=ssh_port, admin_key=admin_key)

    # In-place switch to the same server config, built on the remote so a
    # different-arch operator (e.g. an aarch64 laptop) can drive an x86_64 host.
    print(f"deploy: switching {host} to .#{server.name} (built on the remote)", flush=True)
    try:
        switch_profile(server, host, admin_key, ssh_port, flake_path=REPO_ROOT)
    except BaseException:
        _report_deploy_outcome(host, "failure", admin_key=admin_key, ssh_port=ssh_port)
        raise
    _report_deploy_outcome(host, "success", admin_key=admin_key, ssh_port=ssh_port)
    print(f"deploy: {host} now runs .#{server.name}", flush=True)


def _report_deploy_outcome(host: str, outcome: str, *, admin_key: str, ssh_port: str) -> None:
    """Best-effort: publish this deploy's outcome to the telemetry gateway via the
    host's own reporter unit (which knows its endpoint). Same signal the unattended
    pull-upgrade emits, so DeployFailing covers any upgrade path. The unit only
    exists where the telemetry shipper is enabled; never let reporting mask the
    deploy result."""
    unit = f"portablevps-deploy-report-{outcome}.service"
    try:
        admin_ssh(host, f"sudo systemctl start {unit} || true", port=ssh_port, admin_key=admin_key)
    except Exception:
        pass


def _report_restore_drill(host: str, *, admin_key: str, ssh_port: str) -> None:
    """Best-effort: stamp a restore drill after data verification succeeds."""
    try:
        admin_ssh(
            host,
            "sudo systemctl start portablevps-backup-restore-drill-report.service || true",
            port=ssh_port,
            admin_key=admin_key,
        )
    except Exception:
        pass


def curl_proxy(domain: str, address: str) -> str:
    return subprocess.check_output(
        [
            "curl",
            "--fail",
            "--silent",
            "--show-error",
            "--insecure",
            "--connect-timeout",
            "10",
            "--resolve",
            f"{domain}:443:{address}",
            f"https://{domain}/",
        ],
        text=True,
    ).strip()


def netbird_api_base() -> str:
    # env_or so an explicitly-empty NETBIRD_API_URL (e.g. a task passing "")
    # still falls back to the NetBird Cloud API rather than an invalid URL.
    return env_or("NETBIRD_API_URL", "https://api.netbird.io").rstrip("/")


def netbird_request(method: str, path: str, *, token: str, payload: dict | None = None) -> object:
    body = None
    headers = {
        "Accept": "application/json",
        "Authorization": f"Token {token}",
    }
    if payload is not None:
        body = json.dumps(payload).encode("utf-8")
        headers["Content-Type"] = "application/json"

    request = urllib.request.Request(
        f"{netbird_api_base()}{path}",
        data=body,
        headers=headers,
        method=method,
    )
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            data = response.read()
    except urllib.error.HTTPError as error:
        detail = error.read().decode("utf-8", errors="replace")
        raise CloudError(f"error: NetBird API {method} {path} failed: HTTP {error.code}: {detail}", 70) from error
    except urllib.error.URLError as error:
        raise CloudError(f"error: NetBird API {method} {path} failed: {error.reason}", 70) from error

    if not data:
        return {}
    return json.loads(data.decode("utf-8"))


def netbird_list_zones(token: str) -> list[dict]:
    zones = netbird_request("GET", "/api/dns/zones", token=token)
    if not isinstance(zones, list):
        raise CloudError("error: NetBird API returned an unexpected DNS zones payload", 70)
    return zones


def netbird_find_zone(token: str, domain: str) -> dict | None:
    expected = strip_trailing_dot(domain)
    for zone in netbird_list_zones(token):
        if strip_trailing_dot(str(zone.get("domain", ""))) == expected:
            return zone
    return None


def netbird_create_zone(token: str, *, name: str, domain: str, group_ids: list[str]) -> dict:
    if not group_ids:
        raise CloudError(
            "error: NetBird DNS zone is missing and NETBIRD_DNS_GROUP_IDS is unset; "
            "provide comma-separated group IDs to create the zone",
            64,
        )
    payload = {
        "name": name,
        "domain": strip_trailing_dot(domain),
        "enabled": True,
        "enable_search_domain": False,
        "distribution_groups": group_ids,
    }
    zone = netbird_request("POST", "/api/dns/zones", token=token, payload=payload)
    if not isinstance(zone, dict) or not zone.get("id"):
        raise CloudError("error: NetBird API returned an unexpected DNS zone creation payload", 70)
    return zone


def netbird_list_records(token: str, zone_id: str) -> list[dict]:
    records = netbird_request("GET", f"/api/dns/zones/{zone_id}/records", token=token)
    if not isinstance(records, list):
        raise CloudError("error: NetBird API returned an unexpected DNS records payload", 70)
    return records


def netbird_record_payload(record: dict) -> dict:
    return {
        "name": strip_trailing_dot(str(record["name"])),
        "type": str(record["type"]),
        "content": strip_trailing_dot(str(record["target"])),
        "ttl": int(record.get("ttl", 300)),
    }


def netbird_upsert_record(token: str, zone_id: str, record: dict) -> str:
    desired = netbird_record_payload(record)
    for existing in netbird_list_records(token, zone_id):
        if strip_trailing_dot(str(existing.get("name", ""))) != desired["name"]:
            continue
        if existing.get("type") != desired["type"]:
            raise CloudError(
                f"error: NetBird DNS record {desired['name']} exists with type {existing.get('type')}, "
                f"expected {desired['type']}",
                70,
            )
        existing_content = strip_trailing_dot(str(existing.get("content", "")))
        existing_ttl = int(existing.get("ttl", 0))
        if existing_content == desired["content"] and existing_ttl == desired["ttl"]:
            return "unchanged"
        record_id = existing.get("id")
        if not record_id:
            raise CloudError(f"error: NetBird DNS record {desired['name']} has no id", 70)
        netbird_request("PUT", f"/api/dns/zones/{zone_id}/records/{record_id}", token=token, payload=desired)
        return "updated"

    netbird_request("POST", f"/api/dns/zones/{zone_id}/records", token=token, payload=desired)
    return "created"


def proxy_domain_plan(host: str, *, port: str, admin_key: str) -> dict:
    raw = admin_capture(host, "portablevps-proxy-domain-plan", port=port, admin_key=admin_key)
    try:
        plan = json.loads(raw)
    except json.JSONDecodeError as error:
        raise CloudError("error: server returned invalid proxy domain plan JSON", 70) from error
    if not isinstance(plan, dict):
        raise CloudError("error: server returned an unexpected proxy domain plan", 70)
    return plan


def internal_netbird_records_from_plan(plan: dict, zone_domain: str) -> list[dict]:
    records = []
    normalized_zone = strip_trailing_dot(zone_domain)
    for entry in plan.get("domains", []):
        if not isinstance(entry, dict) or entry.get("visibility") != "internal":
            continue
        dns = entry.get("dns", {})
        if not isinstance(dns, dict):
            continue
        record = dns.get("netbird")
        if not isinstance(record, dict):
            continue
        name = strip_trailing_dot(str(record.get("name", "")))
        if name != normalized_zone and not name.endswith(f".{normalized_zone}"):
            continue
        records.append(record)
    return records


def command_proxy_smoke_test(_args: argparse.Namespace) -> None:
    deployment = selected_deployment()
    host = env("HOST", "")
    if not host:
        raise CloudError("error: HOST is required", 64)

    ssh_port = env("SSH_PORT", "22")
    admin_key = env("CLOUD_ADMIN_KEY", ".local/ssh/cloud-admin_ed25519")
    domain = env("PROXY_DOMAIN", "") or "proxy-test.portablevps.int"
    visibility = env("PROXY_VISIBILITY", "") or "internal"
    public_host = env("PUBLIC_HOST", "")
    public_domain = env("PUBLIC_DOMAIN", "") or domain
    if visibility not in {"internal", "netbird-edge", "direct-public"}:
        raise CloudError("error: PROXY_VISIBILITY must be internal, netbird-edge, or direct-public", 64)

    wait_admin_ssh(host, port=ssh_port, admin_key=admin_key)
    with tempfile.TemporaryDirectory() as temp:
        tmpdir = Path(temp)
        base = copy_repo_to_temp(tmpdir)
        # switch_profile evaluates the copied flake in pure mode; make it
        # self-contained so a monorepo consumer's path:../<tool> input resolves.
        vendor_path_inputs(base, tmpdir)
        write_proxy_smoke_override(base, domain=domain, visibility=visibility)
        print(f"switch: enabling temporary proxy smoke-test route on {host}", flush=True)
        switch_profile(deployment, host, admin_key, ssh_port, flake_path=base)

    print(f"verify: internal https://{domain}/ via {host}", flush=True)
    body = curl_proxy(domain, host)
    if body != "portablevps proxy test ok":
        raise CloudError(f"error: unexpected internal proxy response: {body}", 70)
    print("PASS: internal NetBird proxy route returned expected response", flush=True)

    if public_host:
        print(f"verify: public https://{public_domain}/ via {public_host}", flush=True)
        public_body = curl_proxy(public_domain, public_host)
        if public_body != "portablevps proxy test ok":
            raise CloudError(f"error: unexpected public proxy response: {public_body}", 70)
        print("PASS: public proxy route returned expected response", flush=True)
    else:
        print("skip: PUBLIC_HOST is unset; create a NetBird Reverse Proxy target to verify public access", flush=True)


def command_netbird_dns_sync(_args: argparse.Namespace) -> None:
    host = env("HOST", "")
    if not host:
        raise CloudError("error: HOST is required", 64)
    token = secret_env("NETBIRD_API_TOKEN", "")
    if not token:
        raise CloudError("error: NETBIRD_API_TOKEN is required", 64)
    zone_domain = env("NETBIRD_DNS_ZONE", "int.portablevps.io")
    zone_name = env("NETBIRD_DNS_ZONE_NAME", zone_domain)
    group_ids = comma_list(env("NETBIRD_DNS_GROUP_IDS", ""))
    ssh_port = env("SSH_PORT", "22")
    admin_key = env("CLOUD_ADMIN_KEY", ".local/ssh/cloud-admin_ed25519")

    wait_admin_ssh(host, port=ssh_port, admin_key=admin_key)
    plan = proxy_domain_plan(host, port=ssh_port, admin_key=admin_key)
    records = internal_netbird_records_from_plan(plan, zone_domain)
    if not records:
        print(f"skip: no internal NetBird DNS records in plan for zone {zone_domain}", flush=True)
        return

    zone = netbird_find_zone(token, zone_domain)
    if zone is None:
        print(f"create: NetBird DNS zone {zone_domain}", flush=True)
        zone = netbird_create_zone(token, name=zone_name, domain=zone_domain, group_ids=group_ids)

    zone_id = str(zone.get("id", ""))
    if not zone_id:
        raise CloudError(f"error: NetBird DNS zone {zone_domain} has no id", 70)

    for record in records:
        status = netbird_upsert_record(token, zone_id, record)
        payload = netbird_record_payload(record)
        print(f"{status}: {payload['name']} {payload['type']} {payload['content']} ttl={payload['ttl']}", flush=True)


def proxy_domains(plan: dict) -> list[str]:
    domains: list[str] = []
    for entry in plan.get("domains", []):
        if not isinstance(entry, dict):
            continue
        domain = str(entry.get("domain", "")).strip()
        if domain and domain not in domains:
            domains.append(domain)
    return domains


def prewarm_proxy_certificates(host: str, *, port: str, admin_key: str) -> None:
    plan = proxy_domain_plan(host, port=port, admin_key=admin_key)
    domains = proxy_domains(plan)
    if not domains:
        print("prewarm-tls: no proxy domains declared; skipping", flush=True)
        return
    for domain in domains:
        print(f"prewarm-tls: trigger certificate load/issuance for {domain} on {host}", flush=True)
        subprocess.run(
            [
                "curl",
                "--head",
                "--silent",
                "--show-error",
                "--insecure",
                "--connect-timeout",
                "10",
                "--max-time",
                "20",
                "--connect-to",
                f"{domain}:443:{host}:443",
                f"https://{domain}/",
            ],
            check=True,
        )
        for attempt in range(1, 31):
            result = subprocess.run(
                [
                    "curl",
                    "--head",
                    "--silent",
                    "--show-error",
                    "--connect-timeout",
                    "10",
                    "--max-time",
                    "20",
                    "--connect-to",
                    f"{domain}:443:{host}:443",
                    f"https://{domain}/",
                ],
                text=True,
            )
            if result.returncode == 0:
                print(f"prewarm-tls: valid certificate for {domain} on {host}", flush=True)
                break
            if attempt == 30:
                raise CloudError(
                    f"error: {host} did not present a valid TLS certificate for {domain}; "
                    "aborting before stopping the source host",
                    70,
                )
            time.sleep(10)


def sync_internal_netbird_dns(host: str, *, port: str, admin_key: str) -> None:
    token = secret_env("NETBIRD_API_TOKEN", "")
    if not token:
        print("netbird-dns: NETBIRD_API_TOKEN is unset; skipping DNS repoint", flush=True)
        return
    zone_domain = env("NETBIRD_DNS_ZONE", "int.portablevps.io")
    zone_name = env("NETBIRD_DNS_ZONE_NAME", zone_domain)
    group_ids = comma_list(env("NETBIRD_DNS_GROUP_IDS", ""))
    plan = proxy_domain_plan(host, port=port, admin_key=admin_key)
    records = internal_netbird_records_from_plan(plan, zone_domain)
    if not records:
        print(f"netbird-dns: no internal records in plan for zone {zone_domain}; skipping", flush=True)
        return
    zone = netbird_find_zone(token, zone_domain)
    if zone is None:
        print(f"netbird-dns: create zone {zone_domain}", flush=True)
        zone = netbird_create_zone(token, name=zone_name, domain=zone_domain, group_ids=group_ids)
    zone_id = str(zone.get("id", ""))
    if not zone_id:
        raise CloudError(f"error: NetBird DNS zone {zone_domain} has no id", 70)
    for record in records:
        status = netbird_upsert_record(token, zone_id, record)
        payload = netbird_record_payload(record)
        print(f"netbird-dns: {status}: {payload['name']} {payload['type']} {payload['content']} ttl={payload['ttl']}", flush=True)


def command_migrate_service(_args: argparse.Namespace) -> None:
    target = require_server(env("TARGET_SERVER", env("SERVER", "")))
    source_name = env("SOURCE_SERVER", "")
    source = require_server(source_name) if source_name else None
    source_host = env("SOURCE_HOST", "")
    target_host = env("TARGET_HOST", env("RESTORE_HOST", ""))
    if not source_host:
        raise CloudError("error: SOURCE_HOST is required", 64)
    if not target_host:
        raise CloudError("error: TARGET_HOST is required", 64)
    if source and source.backup_repository and target.backup_repository and source.backup_repository != target.backup_repository:
        raise CloudError(
            f"error: source and target backup repositories differ: {source.backup_repository} != {target.backup_repository}",
            64,
        )
    if not target.backup_repository:
        raise CloudError(f"error: target server {target.name} does not declare backupRepository", 64)

    ssh_port = env("SSH_PORT", "22")
    admin_key = env("CLOUD_ADMIN_KEY", ".local/ssh/cloud-admin_ed25519")
    marker = env("MARKER", "")
    marker_path = marker_file(target, source_host)
    marker_path.parent.mkdir(parents=True, exist_ok=True)

    wait_admin_ssh(target_host, port=ssh_port, admin_key=admin_key)
    switch_restore_profile(target, target_host, admin_key, ssh_port)
    wait_admin_ssh(target_host, port=ssh_port, admin_key=admin_key)
    admin_ssh(
        target_host,
        "test \"$(cat /etc/portablevps/restore-mode)\" = true; "
        "! systemctl is-active --quiet postgres.service; "
        "! systemctl is-active --quiet apps.target",
        port=ssh_port,
        admin_key=admin_key,
    )
    if truthy(env("PREWARM_TLS", "1")):
        prewarm_proxy_certificates(target_host, port=ssh_port, admin_key=admin_key)

    wait_admin_ssh(source_host, port=ssh_port, admin_key=admin_key)
    wait_postgres(source_host, port=ssh_port, admin_key=admin_key)
    if not marker:
        marker = admin_capture(source_host, "sudo insert-test-data.sh | tail -n 1", port=ssh_port, admin_key=admin_key)
    marker_path.write_text(f"{marker}\n", encoding="utf-8")
    admin_ssh(source_host, f"sudo verify-test-data.sh {sh_quote(marker)}", port=ssh_port, admin_key=admin_key)
    print(f"backup: final source marker verified: {marker}", flush=True)
    admin_ssh(source_host, "sudo systemctl start portablevps-backup.service", port=ssh_port, admin_key=admin_key)
    print("backup: final source backup completed", flush=True)

    print(f"cutover: stopping app services on source {source_host}", flush=True)
    admin_ssh(
        source_host,
        "sudo systemctl stop apps.target || true; "
        "sudo systemctl stop authentik-server.service authentik-worker.service authentik-provision.service postgres.service 2>/dev/null || true; "
        "for i in $(seq 1 60); do "
        "active=0; "
        "for unit in apps.target postgres.service authentik-server.service authentik-worker.service; do "
        "systemctl is-active --quiet \"$unit\" && active=1; "
        "done; "
        "test \"$active\" = 0 && exit 0; "
        "sleep 2; "
        "done; "
        "exit 1",
        port=ssh_port,
        admin_key=admin_key,
    )

    print(f"restore: restoring latest backup onto {target_host}", flush=True)
    admin_ssh(target_host, "sudo restore.sh", port=ssh_port, admin_key=admin_key)
    switch_normal_profile(target, target_host, admin_key, "true", ssh_port=ssh_port)
    wait_admin_ssh(target_host, port=ssh_port, admin_key=admin_key)
    admin_ssh(target_host, "sudo systemctl start apps.target", port=ssh_port, admin_key=admin_key)
    wait_postgres(target_host, port=ssh_port, admin_key=admin_key)
    admin_ssh(target_host, f"sudo verify-test-data.sh {sh_quote(marker)}", port=ssh_port, admin_key=admin_key)
    sync_internal_netbird_dns(target_host, port=ssh_port, admin_key=admin_key)
    _report_restore_drill(target_host, admin_key=admin_key, ssh_port=ssh_port)
    print(
        f"PASS: migrated backup repository {target.backup_repository} from {source_host} to {target_host}; marker {marker}",
        flush=True,
    )


def netbird_list_groups(token: str) -> list[dict]:
    groups = netbird_request("GET", "/api/groups", token=token)
    if not isinstance(groups, list):
        raise CloudError("error: NetBird API returned an unexpected groups payload", 70)
    return groups


def netbird_ensure_groups(token: str, names: list[str]) -> dict[str, str]:
    """Return {name: id} for each requested group, creating any that are missing."""
    existing = {str(g.get("name", "")): str(g.get("id", "")) for g in netbird_list_groups(token)}
    result: dict[str, str] = {}
    for name in names:
        if name in existing and existing[name]:
            result[name] = existing[name]
            continue
        print(f"create: NetBird group {name}", flush=True)
        created = netbird_request("POST", "/api/groups", token=token, payload={"name": name, "peers": []})
        if not isinstance(created, dict) or not created.get("id"):
            raise CloudError(f"error: failed to create NetBird group {name}", 70)
        result[name] = str(created["id"])
    return result


def netbird_find_peer(token: str, name: str) -> dict | None:
    peers = netbird_request("GET", "/api/peers", token=token)
    if not isinstance(peers, list):
        raise CloudError("error: NetBird API returned an unexpected peers payload", 70)
    for peer in peers:
        if str(peer.get("hostname", "")) == name or str(peer.get("name", "")) == name:
            return peer
    return None


def netbird_group_peer_ids(group: dict) -> list[str]:
    return [str(p.get("id", p)) if isinstance(p, dict) else str(p) for p in group.get("peers", [])]


def netbird_ensure_peer_in_group(token: str, group_id: str, peer_id: str) -> bool:
    """Add a peer to a group (idempotent, additive). Returns True if changed."""
    group = netbird_request("GET", f"/api/groups/{group_id}", token=token)
    if not isinstance(group, dict):
        raise CloudError(f"error: NetBird group {group_id} not found", 70)
    peer_ids = netbird_group_peer_ids(group)
    if peer_id in peer_ids:
        return False
    netbird_request(
        "PUT",
        f"/api/groups/{group_id}",
        token=token,
        payload={"name": group.get("name", ""), "peers": peer_ids + [peer_id]},
    )
    return True


def netbird_managed_setup_key_name(server_name: str) -> str:
    return f"portablevps-{server_name}"


# Bound the managed setup key rather than issuing an unlimited, year-long one.
# The key auto-groups any peer that joins with it, so an unlimited key is a
# broad standing credential. These bounds are safe because a key that has
# expired or exhausted its uses reports valid=false, so the next
# `cloud:netbird-sync` transparently regenerates and re-stores it — and a
# DR/repurpose flow always runs sync before a host joins.
NETBIRD_SETUP_KEY_USAGE_LIMIT = 10
NETBIRD_SETUP_KEY_EXPIRES_IN_SECONDS = 90 * 24 * 60 * 60  # 90 days


def netbird_setup_key_auto_groups(key: dict) -> list[str]:
    return [str(g) for g in key.get("auto_groups", [])]


def netbird_ensure_setup_key(token: str, name: str, group_ids: list[str]) -> tuple[str, bool]:
    """Ensure a reusable, non-revoked, still-valid setup key with the given
    auto-groups exists. Returns (plaintext_key_or_empty, created); the plaintext
    is only available when the key is newly created. If a live key already
    exists but its auto-groups have drifted from group_ids, reconcile them in
    place so later joins land in the currently declared groups."""
    desired = sorted(set(str(g) for g in group_ids))
    keys = netbird_request("GET", "/api/setup-keys", token=token)
    if isinstance(keys, list):
        for key in keys:
            if str(key.get("name", "")) == name and not key.get("revoked", False) and key.get("valid", True):
                key_id = str(key.get("id", ""))
                if key_id and sorted(set(netbird_setup_key_auto_groups(key))) != desired:
                    netbird_request(
                        "PUT",
                        f"/api/setup-keys/{key_id}",
                        token=token,
                        payload={"name": name, "auto_groups": list(group_ids), "revoked": False},
                    )
                return "", False
    created = netbird_request(
        "POST",
        "/api/setup-keys",
        token=token,
        payload={
            "name": name,
            "type": "reusable",
            "expires_in": NETBIRD_SETUP_KEY_EXPIRES_IN_SECONDS,
            "auto_groups": list(group_ids),
            "usage_limit": NETBIRD_SETUP_KEY_USAGE_LIMIT,
            "ephemeral": False,
        },
    )
    if not isinstance(created, dict) or not created.get("key"):
        raise CloudError(f"error: failed to create NetBird setup key {name}", 70)
    return str(created["key"]), True


# Default sops index for the NetBird setup key, matching netbird.nix's
# setupKeySecret default of "netbird/setup-key".
NETBIRD_SETUP_KEY_SECRET_INDEX = '["netbird"]["setup-key"]'


def store_server_secret_via_sops(server_name: str, index: str, value: str) -> None:
    """Write a secret into the server's sops file without ever exposing it in a
    process listing (sops --value-stdin) or shell history. Requires the operator
    age identity (SOPS_AGE_KEY_FILE, defaulting to the shared operator key)."""
    secrets_file = repo_path(f"secrets/{server_name}.yaml")
    if not secrets_file.is_file():
        raise CloudError(f"error: cannot store secret: secrets file not found: {secrets_file}", 66)
    sops_env = dict(os.environ)
    if not sops_env.get("SOPS_AGE_KEY_FILE"):
        sops_env["SOPS_AGE_KEY_FILE"] = str(repo_path(".local/sops/age-key.txt"))
    try:
        subprocess.run(
            ["sops", "set", "--value-stdin", str(secrets_file), index],
            input=json.dumps(value),
            text=True,
            check=True,
            env=sops_env,
        )
    except FileNotFoundError as exc:
        raise CloudError("error: sops not found; cannot store the secret", 69) from exc
    except subprocess.CalledProcessError as exc:
        raise CloudError(f"error: sops failed to store the secret (exit {exc.returncode})", 70) from exc


def command_netbird_sync(_args: argparse.Namespace) -> None:
    server = selected_server()
    token = secret_env("NETBIRD_API_TOKEN", "")
    if not token:
        raise CloudError("error: NETBIRD_API_TOKEN is required (a NetBird management API token; op:// references are supported)", 64)
    groups = server.netbird_groups
    if not groups:
        print(f"skip: {server.name} declares no netbird.groups", flush=True)
        return

    group_ids = netbird_ensure_groups(token, groups)
    print(f"groups: {', '.join(f'{n}={i}' for n, i in group_ids.items())}", flush=True)

    key_name = netbird_managed_setup_key_name(server.name)
    key_value, created = netbird_ensure_setup_key(token, key_name, list(group_ids.values()))
    if created:
        # Store the plaintext key straight into sops via stdin — never echoed to
        # the terminal or passed on a command line — so it cannot leak into
        # scrollback, shell history, or a process listing.
        store_server_secret_via_sops(server.name, NETBIRD_SETUP_KEY_SECRET_INDEX, key_value)
        print(f"created reusable setup key {key_name} (auto-groups {', '.join(groups)}) and stored it in secrets/{server.name}.yaml", flush=True)
    else:
        print(f"setup key {key_name} already exists (auto-groups reconciled)", flush=True)

    peer = netbird_find_peer(token, server.netbird_name)
    if peer is None:
        print(f"peer: {server.netbird_name} is not registered yet; it will be auto-grouped when it joins with the setup key", flush=True)
        return
    peer_id = str(peer.get("id", ""))
    changed = [name for name, gid in group_ids.items() if netbird_ensure_peer_in_group(token, gid, peer_id)]
    if changed:
        print(f"peer {server.netbird_name}: added to groups {', '.join(changed)}", flush=True)
    else:
        print(f"peer {server.netbird_name}: already in all declared groups", flush=True)


NETBIRD_MANAGED_POLICY_PREFIX = "portablevps:"


def netbird_list_policies(token: str) -> list[dict]:
    policies = netbird_request("GET", "/api/policies", token=token)
    if not isinstance(policies, list):
        raise CloudError("error: NetBird API returned an unexpected policies payload", 70)
    return policies


def netbird_build_policy_payload(spec: dict, group_ids: dict[str, str]) -> dict:
    name = NETBIRD_MANAGED_POLICY_PREFIX + str(spec["name"])
    sources = [group_ids[g] for g in spec.get("sources", [])]
    destinations = [group_ids[g] for g in spec.get("destinations", [])]
    rule = {
        "name": name,
        "description": spec.get("description", ""),
        "enabled": True,
        "action": spec.get("action", "accept"),
        "bidirectional": bool(spec.get("bidirectional", False)),
        "protocol": spec.get("protocol", "all"),
        "ports": [str(p) for p in spec.get("ports", [])],
        "sources": sources,
        "destinations": destinations,
    }
    return {
        "name": name,
        "description": spec.get("description", ""),
        "enabled": True,
        "rules": [rule],
    }


def netbird_policy_group_ids(items: list) -> list[str]:
    return [str(i.get("id", i)) if isinstance(i, dict) else str(i) for i in items]


def netbird_find_default_policy(token: str) -> dict | None:
    for policy in netbird_list_policies(token):
        if str(policy.get("name", "")) == "Default":
            return policy
    return None


def netbird_set_policy_enabled(token: str, policy: dict, enabled: bool) -> None:
    rules = []
    for rule in policy.get("rules", []):
        rules.append({
            "name": rule.get("name", ""),
            "description": rule.get("description", ""),
            "enabled": rule.get("enabled", True),
            "action": rule.get("action", "accept"),
            "bidirectional": rule.get("bidirectional", False),
            "protocol": rule.get("protocol", "all"),
            "ports": rule.get("ports", []),
            "sources": netbird_policy_group_ids(rule.get("sources", [])),
            "destinations": netbird_policy_group_ids(rule.get("destinations", [])),
        })
    payload = {
        "name": policy.get("name", ""),
        "description": policy.get("description", ""),
        "enabled": enabled,
        "rules": rules,
    }
    netbird_request("PUT", f"/api/policies/{policy['id']}", token=token, payload=payload)


def command_netbird_policy_sync(_args: argparse.Namespace) -> None:
    token = secret_env("NETBIRD_API_TOKEN", "")
    if not token:
        raise CloudError("error: NETBIRD_API_TOKEN is required (op:// references are supported)", 64)
    config = load_netbird_config()
    policies = config.get("policies", [])
    disable_default = bool(config.get("disableDefaultPolicy", False))
    if not policies and not disable_default:
        print("skip: no NetBird policies declared", flush=True)
        return
    if disable_default and not truthy(env("CONFIRM_DEFAULT_DENY", "")):
        raise CloudError(
            "error: refusing to disable NetBird's default allow-all without CONFIRM_DEFAULT_DENY=yes. "
            "This makes the mesh default-deny; confirm operator SSH access is covered by a policy first "
            "(you administer servers over the mesh).",
            64,
        )

    group_names = sorted({
        g for p in policies for g in (list(p.get("sources", [])) + list(p.get("destinations", [])))
    })
    group_ids = netbird_ensure_groups(token, group_names) if group_names else {}

    existing = {
        str(p.get("name", "")): p
        for p in netbird_list_policies(token)
        if str(p.get("name", "")).startswith(NETBIRD_MANAGED_POLICY_PREFIX)
    }
    declared: set[str] = set()
    for spec in policies:
        payload = netbird_build_policy_payload(spec, group_ids)
        declared.add(payload["name"])
        if payload["name"] in existing:
            netbird_request("PUT", f"/api/policies/{existing[payload['name']]['id']}", token=token, payload=payload)
            print(f"update: policy {payload['name']}", flush=True)
        else:
            netbird_request("POST", "/api/policies", token=token, payload=payload)
            print(f"create: policy {payload['name']}", flush=True)

    for name, policy in existing.items():
        if name not in declared:
            netbird_request("DELETE", f"/api/policies/{policy['id']}", token=token)
            print(f"delete: policy {name}", flush=True)

    if disable_default:
        default = netbird_find_default_policy(token)
        if default is None:
            print("warning: no NetBird 'Default' policy found to disable", flush=True)
        elif default.get("enabled", True):
            netbird_set_policy_enabled(token, default, False)
            print("disabled: NetBird default allow-all — the mesh is now default-deny", flush=True)
        else:
            print("default allow-all already disabled", flush=True)


def command_restore_test(_args: argparse.Namespace) -> None:
    phase = env("PHASE", "")
    deployment = selected_deployment()
    provider = require_provider(deployment.provider_name)
    legacy_host = env("HOST", "")
    source_host = env("SOURCE_HOST", legacy_host)
    restore_host = env("RESTORE_HOST", legacy_host)
    if not phase:
        raise CloudError("error: PHASE is required", 64)
    if phase == "prepare" and not source_host:
        raise CloudError("error: SOURCE_HOST is required for PHASE=prepare", 64)
    if phase == "restore" and (not source_host or not restore_host):
        raise CloudError("error: SOURCE_HOST and RESTORE_HOST are required for PHASE=restore", 64)

    ssh_port = env("SSH_PORT", "22")
    root_identity = env("ROOT_IDENTITY", "")
    admin_key = env("CLOUD_ADMIN_KEY", ".local/ssh/cloud-admin_ed25519")
    marker = env("MARKER", "")
    marker_path = marker_file(deployment, source_host)

    if phase == "prepare":
        marker_path.parent.mkdir(parents=True, exist_ok=True)
        wait_admin_ssh(source_host, port=ssh_port, admin_key=admin_key)
        wait_postgres(source_host, port=ssh_port, admin_key=admin_key)
        marker = admin_capture(source_host, "sudo insert-test-data.sh | tail -n 1", port=ssh_port, admin_key=admin_key)
        marker_path.write_text(f"{marker}\n", encoding="utf-8")
        print(f"prepare: inserted marker {marker}", flush=True)
        admin_ssh(source_host, "sudo init-backup-repo.sh; sudo backup.sh", port=ssh_port, admin_key=admin_key)
        print(f"prepare: marker saved to {marker_path.relative_to(REPO_ROOT)}", flush=True)
        print(
            "next: boot the restore host into rescue mode, then run "
            f"PHASE=restore SOURCE_HOST={source_host} RESTORE_HOST=RESTORE_IP CONFIRM_DESTROY=RESTORE_IP",
            flush=True,
        )
        return

    if phase != "restore":
        raise CloudError("error: PHASE must be prepare or restore", 64)

    require_destroy_confirmation(restore_host, env("CONFIRM_DESTROY", ""))
    if not marker:
        if not marker_path.is_file():
            raise CloudError(f"error: MARKER is unset and marker file is missing: {marker_path.relative_to(REPO_ROOT)}", 64)
        marker = marker_path.read_text(encoding="utf-8").strip()

    target = env("TARGET", "") or provider_target(provider, restore_host)
    disk = env_or("DISK", "auto")
    print(f"preflight: checking rescue SSH to {target}", flush=True)
    ssh(target, "true", port=ssh_port, identity=root_identity)
    print(f"preflight: available disks on {target}", flush=True)
    list_disks(target, port=ssh_port, identity=root_identity)
    if disk == "auto":
        disk = detect_disk(target, port=ssh_port, identity=root_identity)
    if not disk:
        raise CloudError("error: could not detect install disk; set DISK=/dev/...", 66)

    # The restore host is rebuilt as this server's own static machine identity
    # (the host-loss / replacement case). With machine-based naming there is no
    # temporary identity to assign and later rename.
    install_cloud(
        deployment,
        provider,
        target=target,
        disk=disk,
        ssh_port=ssh_port,
        root_identity=root_identity,
        restore_mode="true",
        admin_key=admin_key,
        admin_pubkey=env("CLOUD_ADMIN_PUBKEY", "keys/cloud-admin.pub"),
        age_key=resolve_age_key(deployment.name),
        kexec_flags=kexec_extra_flags(provider),
    )
    wait_admin_ssh(restore_host, port=ssh_port, admin_key=admin_key)
    admin_ssh(restore_host, "sudo restore.sh", port=ssh_port, admin_key=admin_key)
    switch_normal_profile(
        deployment,
        restore_host,
        admin_key,
        env("FINALIZE_NORMAL", "true"),
        ssh_port=ssh_port,
    )
    wait_admin_ssh(restore_host, port=ssh_port, admin_key=admin_key)
    admin_ssh(restore_host, "sudo systemctl start apps.target", port=ssh_port, admin_key=admin_key)
    wait_postgres(restore_host, port=ssh_port, admin_key=admin_key)
    admin_ssh(restore_host, f"sudo verify-test-data.sh {sh_quote(marker)}", port=ssh_port, admin_key=admin_key)
    _report_restore_drill(restore_host, admin_key=admin_key, ssh_port=ssh_port)
    print(
        f"PASS: restored {provider.name} source {source_host} onto {restore_host} and verified marker: {marker}",
        flush=True,
    )


def command_restore_candidate(args: argparse.Namespace) -> None:
    if not env("RESTORE_HOST", ""):
        server = selected_server()
        resource = state_store().get_role(server.name, "candidate")
        if resource:
            host = resource_host(resource)
            if host:
                os.environ["RESTORE_HOST"] = host
    if not env("PHASE", ""):
        os.environ["PHASE"] = "restore"
    command_restore_test(args)


def promote_lifecycle_state(server: Server, candidate_host: str) -> None:
    """Swap local lifecycle roles after a successful promotion.

    The promoted host becomes the active record. A previous active record is
    re-recorded as candidate so its provider server can still be deleted
    through cloud:delete instead of leaking.
    """
    store = state_store()
    with store.lock(server.name):
        candidate = store.get_role(server.name, "candidate")
        if candidate is None or resource_host(candidate) != candidate_host:
            print(
                f"state: no candidate lifecycle state matching {candidate_host}; local roles unchanged",
                flush=True,
            )
            return
        previous_active = store.get_role(server.name, "active")
        store.set_role(server.name, "active", candidate, overwrite=True)
        if previous_active is None:
            store.delete_role(server.name, "candidate")
            print(f"state: {server.name}/active now points at {candidate_host}", flush=True)
            return
        store.set_role(server.name, "candidate", previous_active, overwrite=True)
        previous_id = previous_active.get("provider_server_id", "")
        print(
            f"state: {server.name}/active now points at {candidate_host}; "
            f"previous active provider server {previous_id} is recorded as candidate. "
            f"Retire it with: task cloud:delete SERVER={server.name} ROLE=candidate CONFIRM_DELETE={previous_id}",
            flush=True,
        )


def command_promote_candidate(_args: argparse.Namespace) -> None:
    server = selected_server()
    candidate_host = env("CANDIDATE_HOST", env("RESTORE_HOST", ""))
    if not candidate_host:
        raise CloudError("error: CANDIDATE_HOST or RESTORE_HOST is required", 64)
    if not truthy(env("SOURCE_OFFLINE", "")):
        raise CloudError(
            "error: SOURCE_OFFLINE=1 is required before promoting a candidate to stable identity",
            64,
        )
    require_promote_confirmation(server, env("CONFIRM_PROMOTE", ""))

    ssh_port = env("SSH_PORT", "22")
    admin_key = env("CLOUD_ADMIN_KEY", ".local/ssh/cloud-admin_ed25519")
    source_host = env("SOURCE_HOST", "")
    marker = env("MARKER", "")
    if not marker and source_host:
        marker_path = marker_file(server, source_host)
        if marker_path.is_file():
            marker = marker_path.read_text(encoding="utf-8").strip()

    print(f"promote: finalizing {candidate_host} on the normal .#{server.name} profile", flush=True)
    wait_admin_ssh(candidate_host, port=ssh_port, admin_key=admin_key)
    switch_normal_profile(
        server,
        candidate_host,
        admin_key,
        "true",
        ssh_port=ssh_port,
    )
    wait_admin_ssh(candidate_host, port=ssh_port, admin_key=admin_key)
    admin_ssh(candidate_host, "sudo systemctl start apps.target", port=ssh_port, admin_key=admin_key)
    wait_postgres(candidate_host, port=ssh_port, admin_key=admin_key)
    if marker:
        admin_ssh(candidate_host, f"sudo verify-test-data.sh {sh_quote(marker)}", port=ssh_port, admin_key=admin_key)
        print(f"verify: marker {marker}", flush=True)
    else:
        print("verify: skipped marker check; set MARKER or SOURCE_HOST to verify restored data", flush=True)
    promote_lifecycle_state(server, candidate_host)
    print(
        "PASS: candidate promoted. Update external NetBird/public routing to the promoted peer if it is not automatic.",
        flush=True,
    )


def command_secrets_init_server(_args: argparse.Namespace) -> None:
    server = selected_server()
    key_rel = per_server_age_key(server.name)
    key_path = repo_path(key_rel)
    if key_path.exists() and not truthy(env("FORCE", "")):
        raise CloudError(f"error: {key_rel} already exists; set FORCE=1 to replace it", 73)
    key_path.parent.mkdir(parents=True, exist_ok=True)
    if key_path.exists():
        key_path.unlink()
    run(["age-keygen", "-o", str(key_path)])
    os.chmod(key_path, 0o600)
    recipient = capture(["age-keygen", "-y", str(key_path)])
    print(f"created per-server age key: {key_rel}", flush=True)
    print(f"recipient: {recipient}", flush=True)
    print("next steps:", flush=True)
    print(f"  1. add the recipient above to .sops.yaml for secrets/{server.name}.yaml", flush=True)
    print(f"     (keep your operator recipient too so you can still decrypt)", flush=True)
    print(f"  2. set portablevps.secrets.file = ./secrets/{server.name}.yaml in servers/{server.name}.nix", flush=True)
    print(f"  3. create or rekey the secrets: sops secrets/{server.name}.yaml", flush=True)
    print(f"     then: sops updatekeys secrets/{server.name}.yaml", flush=True)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    subcommands = parser.add_subparsers(dest="command", required=True)

    install = subcommands.add_parser("install", help="Install a logical server VPS with nixos-anywhere")
    install.set_defaults(func=command_install)

    adopt = subcommands.add_parser("adopt", help="Bootstrap the admin key onto an existing host and reinstall it")
    adopt.set_defaults(func=command_adopt)

    repurpose = subcommands.add_parser("repurpose", help="Switch an existing portablevps host to a different server config in place (no reinstall)")
    repurpose.set_defaults(func=command_repurpose)

    deploy = subcommands.add_parser("deploy", help="Deploy the latest committed config to a running server, in place (no reinstall)")
    deploy.set_defaults(func=command_deploy)

    lifecycle_preflight = subcommands.add_parser("lifecycle-preflight", help="Validate provider lifecycle credentials and local state")
    lifecycle_preflight.set_defaults(func=command_lifecycle_preflight)

    lifecycle_create = subcommands.add_parser("lifecycle-create", help="Create a provider VPS and record local state")
    lifecycle_create.set_defaults(func=command_lifecycle_create)

    lifecycle_status = subcommands.add_parser("lifecycle-status", help="Show local and provider lifecycle state")
    lifecycle_status.set_defaults(func=command_lifecycle_status)

    lifecycle_rescue = subcommands.add_parser("lifecycle-rescue", help="Enable provider rescue mode and reboot")
    lifecycle_rescue.set_defaults(func=command_lifecycle_rescue)

    lifecycle_delete = subcommands.add_parser("lifecycle-delete", help="Delete a provider VPS from local lifecycle state")
    lifecycle_delete.set_defaults(func=command_lifecycle_delete)

    install_created = subcommands.add_parser("install-created", help="Install NixOS onto a lifecycle-created VPS")
    install_created.set_defaults(func=command_install_created)

    preflight = subcommands.add_parser("preflight", help="Validate local config, secrets, and optional target SSH/disk")
    preflight.set_defaults(func=command_preflight)

    smoke = subcommands.add_parser("smoke-test", help="Install a VPS and verify PostgreSQL starts")
    smoke.set_defaults(func=command_smoke_test)

    restore = subcommands.add_parser("restore-test", help="Run the two-phase cloud restore validation")
    restore.set_defaults(func=command_restore_test)

    restore_candidate = subcommands.add_parser("restore-candidate", help="Install and restore onto a separate candidate host")
    restore_candidate.set_defaults(func=command_restore_candidate)

    promote_candidate = subcommands.add_parser("promote-candidate", help="Promote a validated restore candidate to stable identity")
    promote_candidate.set_defaults(func=command_promote_candidate)

    migrate_service = subcommands.add_parser("migrate-service", help="Move a service between existing hosts via backup and restore")
    migrate_service.set_defaults(func=command_migrate_service)

    proxy = subcommands.add_parser("proxy-smoke-test", help="Temporarily enable and verify the proxy smoke-test backend")
    proxy.set_defaults(func=command_proxy_smoke_test)

    netbird_dns = subcommands.add_parser("netbird-dns-sync", help="Sync internal proxy DNS records into NetBird DNS")
    netbird_dns.set_defaults(func=command_netbird_dns_sync)

    netbird_sync = subcommands.add_parser("netbird-sync", help="Reconcile a server's NetBird groups and reusable setup key via the API")
    netbird_sync.set_defaults(func=command_netbird_sync)

    netbird_policy_sync = subcommands.add_parser("netbird-policy-sync", help="Reconcile fleet NetBird access policies (allow-list) via the API")
    netbird_policy_sync.set_defaults(func=command_netbird_policy_sync)

    secrets_init = subcommands.add_parser("secrets-init-server", help="Generate a per-server age key and print its recipient")
    secrets_init.set_defaults(func=command_secrets_init_server)

    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    try:
        args.func(args)
    except CloudError as error:
        print(str(error), file=sys.stderr)
        return error.exit_code
    except LifecycleError as error:
        print(str(error), file=sys.stderr)
        return error.exit_code
    except SecretError as error:
        print(str(error), file=sys.stderr)
        return error.exit_code
    except subprocess.CalledProcessError as error:
        return error.returncode
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

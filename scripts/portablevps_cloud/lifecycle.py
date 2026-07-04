"""Provider-neutral lifecycle adapter interfaces and orchestration helpers."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Protocol


class LifecycleError(RuntimeError):
    """Raised when provider lifecycle management cannot proceed safely."""

    def __init__(self, message: str, exit_code: int = 1) -> None:
        super().__init__(message)
        self.exit_code = exit_code


@dataclass(frozen=True)
class ServerSpec:
    name: str
    provider: str
    hostname: str
    netbird_name: str
    placement: dict


@dataclass(frozen=True)
class ProviderSpec:
    name: str
    values: dict


@dataclass(frozen=True)
class LifecycleContext:
    server: ServerSpec
    provider: ProviderSpec
    role: str
    repo_root: Path
    state_dir: Path
    env: dict[str, str]


class ProviderAdapter(Protocol):
    name: str

    def preflight(self, context: LifecycleContext) -> None:
        """Validate credentials and provider-specific placement metadata."""

    def create_server(self, context: LifecycleContext, *, public_key_path: Path) -> dict:
        """Create one provider server and return normalized resource state."""

    def status(self, context: LifecycleContext, resource: dict) -> dict:
        """Return normalized live provider state for a tracked resource."""

    def enable_rescue(self, context: LifecycleContext, resource: dict, *, public_key_path: Path) -> dict:
        """Enable rescue mode and return updated normalized resource state."""

    def reboot(self, context: LifecycleContext, resource: dict) -> dict:
        """Reboot the tracked resource and return updated normalized resource state."""

    def delete_server(self, context: LifecycleContext, resource: dict) -> None:
        """Delete the tracked provider server."""


class UnsupportedProviderAdapter:
    """Explicit adapter for providers whose lifecycle API is not implemented yet."""

    def __init__(self, name: str) -> None:
        self.name = name

    def _unsupported(self) -> None:
        raise LifecycleError(f"error: lifecycle management is not implemented for provider: {self.name}", 64)

    def preflight(self, context: LifecycleContext) -> None:
        self._unsupported()

    def create_server(self, context: LifecycleContext, *, public_key_path: Path) -> dict:
        self._unsupported()

    def status(self, context: LifecycleContext, resource: dict) -> dict:
        self._unsupported()

    def enable_rescue(self, context: LifecycleContext, resource: dict, *, public_key_path: Path) -> dict:
        self._unsupported()

    def reboot(self, context: LifecycleContext, resource: dict) -> dict:
        self._unsupported()

    def delete_server(self, context: LifecycleContext, resource: dict) -> None:
        self._unsupported()


def require_role(role: str) -> str:
    if role not in {"active", "candidate"}:
        raise LifecycleError("error: ROLE must be active or candidate", 64)
    return role


def require_delete_confirmation(resource: dict, confirm_delete: str) -> None:
    provider_id = str(resource.get("provider_server_id", ""))
    if confirm_delete != provider_id:
        raise LifecycleError(f"error: refusing to delete without CONFIRM_DELETE={provider_id}", 64)


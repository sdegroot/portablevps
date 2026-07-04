"""Local untracked lifecycle state for concrete provider resources."""

from __future__ import annotations

import fcntl
import json
import os
from contextlib import contextmanager
from datetime import datetime, timezone
from pathlib import Path
from typing import Iterator

from .lifecycle import LifecycleError, require_role


def utc_now() -> str:
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


class StateStore:
    def __init__(self, root: Path) -> None:
        self.root = root

    def server_path(self, server_name: str) -> Path:
        return self.root / "servers" / f"{server_name}.json"

    def load(self, server_name: str) -> dict:
        path = self.server_path(server_name)
        if not path.is_file():
            return {
                "server": server_name,
                "resources": {},
            }
        return json.loads(path.read_text(encoding="utf-8"))

    def save(self, server_name: str, document: dict) -> None:
        path = self.server_path(server_name)
        path.parent.mkdir(parents=True, exist_ok=True)
        tmp_path = path.with_name(path.name + ".tmp")
        tmp_path.write_text(json.dumps(document, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        os.replace(tmp_path, path)

    @contextmanager
    def lock(self, server_name: str) -> Iterator[None]:
        """Serialize read-modify-write sequences for one server document."""
        path = self.server_path(server_name).with_name(f"{server_name}.lock")
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open("w", encoding="utf-8") as handle:
            fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
            try:
                yield
            finally:
                fcntl.flock(handle.fileno(), fcntl.LOCK_UN)

    def get_role(self, server_name: str, role: str) -> dict | None:
        require_role(role)
        return self.load(server_name).get("resources", {}).get(role)

    def set_role(self, server_name: str, role: str, resource: dict, *, overwrite: bool = False) -> None:
        require_role(role)
        document = self.load(server_name)
        resources = document.setdefault("resources", {})
        if role in resources and not overwrite:
            provider_id = resources[role].get("provider_server_id", "")
            raise LifecycleError(
                f"error: {server_name} already has {role} lifecycle state"
                + (f" for provider server {provider_id}" if provider_id else ""),
                64,
            )
        resources[role] = {
            **resource,
            "role": role,
            "updated_at": utc_now(),
        }
        self.save(server_name, document)

    def delete_role(self, server_name: str, role: str) -> None:
        require_role(role)
        document = self.load(server_name)
        resources = document.setdefault("resources", {})
        resources.pop(role, None)
        self.save(server_name, document)


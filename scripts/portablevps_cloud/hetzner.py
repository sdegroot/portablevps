"""Hetzner Cloud lifecycle adapter."""

from __future__ import annotations

import json
import time
import urllib.error
import urllib.request
from pathlib import Path

from .lifecycle import LifecycleContext, LifecycleError, ProviderAdapter


class HetznerAdapter(ProviderAdapter):
    name = "hetzner"
    api_base = "https://api.hetzner.cloud/v1"

    def _token(self, context: LifecycleContext) -> str:
        token = context.env.get("HCLOUD_TOKEN", "")
        if not token:
            raise LifecycleError(
                "error: HCLOUD_TOKEN is required; create .local/providers/hetzner.env or export it",
                64,
            )
        return token

    def _request(
        self,
        context: LifecycleContext,
        method: str,
        path: str,
        *,
        payload: dict | None = None,
    ) -> dict:
        body = None
        headers = {
            "Authorization": f"Bearer {self._token(context)}",
            "Accept": "application/json",
        }
        if payload is not None:
            body = json.dumps(payload).encode("utf-8")
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(
            f"{self.api_base}{path}",
            data=body,
            headers=headers,
            method=method,
        )
        try:
            with urllib.request.urlopen(request, timeout=45) as response:
                data = response.read()
        except urllib.error.HTTPError as error:
            detail = error.read().decode("utf-8", errors="replace")
            raise LifecycleError(
                f"error: Hetzner API {method} {path} failed: HTTP {error.code}: {detail}",
                70,
            ) from error
        except urllib.error.URLError as error:
            raise LifecycleError(f"error: Hetzner API {method} {path} failed: {error.reason}", 70) from error
        if not data:
            return {}
        return json.loads(data.decode("utf-8"))

    def _wait_action(self, context: LifecycleContext, action: dict | None) -> None:
        if not action:
            return
        action_id = action.get("id")
        if not action_id:
            return
        for _attempt in range(120):
            payload = self._request(context, "GET", f"/actions/{action_id}")
            status = payload.get("action", {}).get("status")
            if status == "success":
                return
            if status == "error":
                error = payload.get("action", {}).get("error", {})
                raise LifecycleError(f"error: Hetzner action {action_id} failed: {error}", 70)
            time.sleep(2)
        raise LifecycleError(f"error: timed out waiting for Hetzner action {action_id}", 70)

    def _placement_value(self, context: LifecycleContext, key: str, provider_key: str, default: str = "") -> str:
        placement = context.server.placement
        if key in placement:
            return str(placement[key])
        return str(context.provider.values.get(provider_key, default))

    def preflight(self, context: LifecycleContext) -> None:
        self._token(context)
        location = self._placement_value(context, "location", "defaultLocation")
        server_type = self._placement_value(context, "serverType", "defaultServerType")
        image = self._placement_value(context, "image", "defaultImage", "ubuntu-24.04")
        if not location:
            raise LifecycleError("error: Hetzner lifecycle requires defaultLocation or placement.location", 64)
        if not server_type:
            raise LifecycleError("error: Hetzner lifecycle requires defaultServerType or placement.serverType", 64)
        if not image:
            raise LifecycleError("error: Hetzner lifecycle requires defaultImage or placement.image", 64)
        self._request(context, "GET", "/locations")
        print(f"hetzner: credentials ok; location={location} server_type={server_type} image={image}", flush=True)

    def _labels(self, context: LifecycleContext) -> dict[str, str]:
        labels = {
            "managed-by": "portablevps-nix-infra",
            "portablevps-server": context.server.name,
            "portablevps-role": context.role,
        }
        extra_labels = context.server.placement.get("labels", {})
        if isinstance(extra_labels, dict):
            labels.update({str(key): str(value) for key, value in extra_labels.items()})
        return labels

    def ensure_ssh_key(self, context: LifecycleContext, *, public_key_path: Path) -> int:
        public_key = public_key_path.read_text(encoding="utf-8").strip()
        key_name = f"portablevps-{context.server.name}-{context.role}"
        payload = self._request(context, "GET", "/ssh_keys")
        for ssh_key in payload.get("ssh_keys", []):
            if ssh_key.get("name") == key_name and ssh_key.get("public_key") == public_key:
                return int(ssh_key["id"])
        payload = self._request(context, "POST", "/ssh_keys", payload={
            "name": key_name,
            "public_key": public_key,
            "labels": self._labels(context),
        })
        return int(payload["ssh_key"]["id"])

    def create_server(self, context: LifecycleContext, *, public_key_path: Path) -> dict:
        ssh_key_id = self.ensure_ssh_key(context, public_key_path=public_key_path)
        location = self._placement_value(context, "location", "defaultLocation")
        server_type = self._placement_value(context, "serverType", "defaultServerType")
        image = self._placement_value(context, "image", "defaultImage", "ubuntu-24.04")
        name = f"{context.server.name}-{context.role}"
        payload = self._request(context, "POST", "/servers", payload={
            "name": name,
            "server_type": server_type,
            "image": image,
            "location": location,
            "ssh_keys": [ssh_key_id],
            "labels": self._labels(context),
        })
        self._wait_action(context, payload.get("action"))
        server = self._request(context, "GET", f"/servers/{payload['server']['id']}")["server"]
        return self._normalize(server, context.role)

    def status(self, context: LifecycleContext, resource: dict) -> dict:
        server = self._server(context, resource)
        return self._normalize(server, str(resource.get("role", context.role)))

    def enable_rescue(self, context: LifecycleContext, resource: dict, *, public_key_path: Path) -> dict:
        ssh_key_id = self.ensure_ssh_key(context, public_key_path=public_key_path)
        payload = self._request(
            context,
            "POST",
            f"/servers/{resource['provider_server_id']}/actions/enable_rescue",
            payload={
                "type": "linux64",
                "ssh_keys": [ssh_key_id],
            },
        )
        self._wait_action(context, payload.get("action"))
        return self.status(context, resource)

    def reboot(self, context: LifecycleContext, resource: dict) -> dict:
        payload = self._request(
            context,
            "POST",
            f"/servers/{resource['provider_server_id']}/actions/reboot",
        )
        self._wait_action(context, payload.get("action"))
        return self.status(context, resource)

    def delete_server(self, context: LifecycleContext, resource: dict) -> None:
        payload = self._request(context, "DELETE", f"/servers/{resource['provider_server_id']}")
        self._wait_action(context, payload.get("action"))

    def _server(self, context: LifecycleContext, resource: dict) -> dict:
        return self._request(context, "GET", f"/servers/{resource['provider_server_id']}")["server"]

    def _normalize(self, server: dict, role: str) -> dict:
        public_net = server.get("public_net", {})
        ipv4 = public_net.get("ipv4") or {}
        ipv6 = public_net.get("ipv6") or {}
        return {
            "provider": self.name,
            "provider_server_id": str(server["id"]),
            "provider_server_name": str(server.get("name", "")),
            "role": role,
            "status": str(server.get("status", "")),
            "public_ipv4": str(ipv4.get("ip", "")),
            "public_ipv6": str(ipv6.get("ip", "")),
            "rescue_enabled": bool(server.get("rescue_enabled", False)),
        }

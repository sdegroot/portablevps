"""Provider adapter registry."""

from __future__ import annotations

from .hetzner import HetznerAdapter
from .lifecycle import ProviderAdapter, UnsupportedProviderAdapter


def provider_adapter(name: str) -> ProviderAdapter:
    if name == "hetzner":
        return HetznerAdapter()
    return UnsupportedProviderAdapter(name)


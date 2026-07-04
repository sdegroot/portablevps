"""Resolve secret references from a password manager or the environment.

A reference is a URI-like string that says where to read a secret:

    op://<vault>/<item>/<field>   1Password (requires the `op` CLI)
    env://<VARNAME>               an environment variable

Anything that does not start with a registered scheme is returned unchanged, so
existing plaintext configuration keeps working. Add support for another manager
(Bitwarden, pass, Vault, ...) by registering a scheme handler in RESOLVERS.
"""

from __future__ import annotations

import os
import subprocess


class SecretError(RuntimeError):
    def __init__(self, message: str, exit_code: int = 64) -> None:
        super().__init__(message)
        self.exit_code = exit_code


def _resolve_op(ref: str) -> str:
    try:
        return subprocess.check_output(["op", "read", ref], text=True).strip()
    except FileNotFoundError as error:
        raise SecretError(
            "error: 1Password CLI `op` not found; install it and sign in "
            f"(needed to resolve {ref})",
            69,
        ) from error
    except subprocess.CalledProcessError as error:
        raise SecretError(f"error: failed to read 1Password reference {ref}", 70) from error


def _resolve_env(ref: str) -> str:
    name = ref[len("env://"):]
    if name not in os.environ:
        raise SecretError(f"error: environment variable {name} is not set (from {ref})")
    return os.environ[name]


# Scheme prefix -> handler. Register new managers here; see the module docstring.
RESOLVERS = {
    "op://": _resolve_op,
    "env://": _resolve_env,
}


def is_reference(value: str) -> bool:
    return isinstance(value, str) and any(value.startswith(prefix) for prefix in RESOLVERS)


def resolve_secret(value: str) -> str:
    """Resolve a reference to its secret value; return literals unchanged."""
    for prefix, handler in RESOLVERS.items():
        if value.startswith(prefix):
            return handler(value)
    return value


def resolve_mapping(values: dict[str, str]) -> dict[str, str]:
    """Resolve any reference-valued entries in a mapping, leaving literals as-is."""
    return {
        key: (resolve_secret(value) if is_reference(value) else value)
        for key, value in values.items()
    }

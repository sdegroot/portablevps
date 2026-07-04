#!/usr/bin/env bash
# Generates the local cloud administrator SSH keypair used by provider installs.
set -euo pipefail

private_key="${CLOUD_ADMIN_KEY:-.local/ssh/cloud-admin_ed25519}"
public_key="${CLOUD_ADMIN_PUBKEY:-keys/cloud-admin.pub}"
force="${FORCE:-false}"

if [ -e "$private_key" ] || [ -e "$public_key" ]; then
  if [ "$force" != "true" ]; then
    echo "error: key already exists; set FORCE=true to replace it" >&2
    echo "private: $private_key" >&2
    echo "public:  $public_key" >&2
    exit 73
  fi
fi

mkdir -p "$(dirname "$private_key")" "$(dirname "$public_key")"

if [ "$force" = "true" ]; then
  rm -f "$private_key" "$public_key"
fi

ssh-keygen \
  -t ed25519 \
  -a 64 \
  -N "" \
  -C "epistola-cloud-admin" \
  -f "$private_key" >/dev/null

cp "$private_key.pub" "$public_key"
rm -f "$private_key.pub"

chmod 0600 "$private_key"
chmod 0644 "$public_key"

echo "created private key: $private_key"
echo "created public key:  $public_key"

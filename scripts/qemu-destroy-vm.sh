#!/usr/bin/env bash
# Deletes local QEMU VM state for a named VM.
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
usage: scripts/qemu-destroy-vm.sh NAME

Deletes the local QEMU VM directory under QEMU_STATE_DIR.

Environment:
  QEMU_STATE_DIR   Base directory for VM state. Default: .local/qemu
USAGE
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi

if [ "$#" -ne 1 ]; then
  usage
  exit 64
fi

name="$1"
state_dir="${QEMU_STATE_DIR:-.local/qemu}"
vm_dir="$state_dir/$name"

if [ ! -d "$vm_dir" ]; then
  echo "error: VM does not exist: $vm_dir" >&2
  exit 66
fi

echo "This will permanently delete: $vm_dir"
if [ "${CI:-}" != "true" ] && [ "${EP_TEST_MODE:-}" != "true" ]; then
  printf "Type DESTROY to continue: "
  read -r confirmation
  if [ "$confirmation" != "DESTROY" ]; then
    echo "aborted" >&2
    exit 75
  fi
fi

rm -rf "$vm_dir"
echo "deleted VM: $name"

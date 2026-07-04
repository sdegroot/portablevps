#!/usr/bin/env bash
# Creates local QEMU VM disk images and test SSH credentials.
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
usage: scripts/qemu-create-vm.sh NAME

Creates local QEMU disk images for a NixOS aarch64 VM.

Environment:
  QEMU_STATE_DIR   Base directory for VM state. Default: .local/qemu
  ROOT_SIZE        Root disk size. Default: 40G
  DATA_SIZE        ZFS data disk size. Default: 20G
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
case "$name" in
  *[!A-Za-z0-9._-]* | "")
    echo "error: VM name may only contain letters, numbers, dots, underscores, and dashes" >&2
    exit 64
    ;;
esac

if ! command -v qemu-img >/dev/null 2>&1; then
  echo "error: qemu-img not found; install qemu first" >&2
  exit 69
fi

state_dir="${QEMU_STATE_DIR:-.local/qemu}"
vm_dir="$state_dir/$name"
root_size="${ROOT_SIZE:-40G}"
data_size="${DATA_SIZE:-20G}"
firmware_vars_src="${QEMU_FIRMWARE_VARS:-/opt/homebrew/share/qemu/edk2-arm-vars.fd}"

if [ -e "$vm_dir" ]; then
  echo "error: VM already exists: $vm_dir" >&2
  exit 73
fi

if [ ! -r "$firmware_vars_src" ]; then
  echo "error: firmware vars template not found: $firmware_vars_src" >&2
  exit 66
fi

mkdir -p "$vm_dir"
qemu-img create -f qcow2 "$vm_dir/root.qcow2" "$root_size"
qemu-img create -f qcow2 "$vm_dir/data.qcow2" "$data_size"
cp "$firmware_vars_src" "$vm_dir/efi-vars.fd"

cat <<EOF
created VM: $name
  root disk: $vm_dir/root.qcow2 ($root_size)
  data disk: $vm_dir/data.qcow2 ($data_size)
  EFI vars:  $vm_dir/efi-vars.fd

Install boot example:
  scripts/qemu-boot-vm.sh $name --iso /path/to/nixos-minimal-aarch64-linux.iso --ssh-port 2222

Normal boot example:
  scripts/qemu-boot-vm.sh $name --ssh-port 2222
EOF

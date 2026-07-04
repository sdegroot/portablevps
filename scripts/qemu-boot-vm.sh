#!/usr/bin/env bash
# Boots a named local QEMU VM with optional ISO, SSH forwarding, and host sharing.
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
usage: scripts/qemu-boot-vm.sh NAME [options]

Boot a local aarch64 NixOS VM with QEMU/HVF.

Options:
  --iso PATH        Boot with an installer ISO attached.
  --ssh-port PORT  Forward host PORT to guest port 22. Default: 2222.
  --memory MB      Guest memory in MiB. Default: 4096.
  --cpus N         Guest CPU count. Default: 2.
  --share PATH     Share a host directory as 9p mount tag hostshare.
  --graphics       Use QEMU graphical output instead of -nographic.

Environment:
  QEMU_STATE_DIR       Base directory for VM state. Default: .local/qemu
  QEMU_FIRMWARE_CODE   EDK2 firmware code path.
                       Default: /opt/homebrew/share/qemu/edk2-aarch64-code.fd
USAGE
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi

if [ "$#" -lt 1 ]; then
  usage
  exit 64
fi

name="$1"
shift

state_dir="${QEMU_STATE_DIR:-.local/qemu}"
vm_dir="$state_dir/$name"
iso=""
ssh_port="2222"
memory="4096"
cpus="2"
graphics="false"
share_path=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --iso)
      iso="${2:-}"
      shift 2
      ;;
    --ssh-port)
      ssh_port="${2:-}"
      shift 2
      ;;
    --memory)
      memory="${2:-}"
      shift 2
      ;;
    --cpus)
      cpus="${2:-}"
      shift 2
      ;;
    --share)
      share_path="${2:-}"
      shift 2
      ;;
    --graphics)
      graphics="true"
      shift
      ;;
    *)
      echo "error: unknown argument: $1" >&2
      usage
      exit 64
      ;;
  esac
done

if ! command -v qemu-system-aarch64 >/dev/null 2>&1; then
  echo "error: qemu-system-aarch64 not found; install qemu first" >&2
  exit 69
fi

firmware_code="${QEMU_FIRMWARE_CODE:-/opt/homebrew/share/qemu/edk2-aarch64-code.fd}"
root_disk="$vm_dir/root.qcow2"
data_disk="$vm_dir/data.qcow2"
efi_vars="$vm_dir/efi-vars.fd"

for path in "$firmware_code" "$root_disk" "$data_disk" "$efi_vars"; do
  if [ ! -e "$path" ]; then
    echo "error: required file missing: $path" >&2
    exit 66
  fi
done

if [ -n "$iso" ] && [ ! -r "$iso" ]; then
  echo "error: ISO not readable: $iso" >&2
  exit 66
fi

display_args=(-nographic)
if [ "$graphics" = "true" ]; then
  display_args=()
fi

boot_args=()
if [ -n "$iso" ]; then
  boot_args=(-drive "file=$iso,media=cdrom,if=none,id=cdrom,readonly=on" -device virtio-scsi-pci -device scsi-cd,drive=cdrom -boot d)
fi

share_args=()
if [ -n "$share_path" ]; then
  if [ ! -d "$share_path" ]; then
    echo "error: shared path is not a directory: $share_path" >&2
    exit 66
  fi
  share_args=(-virtfs "local,path=$share_path,mount_tag=hostshare,security_model=mapped-xattr")
fi

echo "booting VM: $name"
echo "SSH forwarding: localhost:$ssh_port -> guest:22"
echo "QEMU console escape: Ctrl-a x"

exec qemu-system-aarch64 \
  -machine virt,accel=hvf \
  -cpu host \
  -smp "$cpus" \
  -m "$memory" \
  -drive "if=pflash,format=raw,file=$firmware_code,readonly=on" \
  -drive "if=pflash,format=raw,file=$efi_vars" \
  -drive "file=$root_disk,if=virtio,format=qcow2" \
  -drive "file=$data_disk,if=virtio,format=qcow2" \
  ${boot_args[@]+"${boot_args[@]}"} \
  ${share_args[@]+"${share_args[@]}"} \
  -device virtio-rng-pci \
  -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:$ssh_port-:22" \
  -device virtio-net-pci,netdev=net0 \
  "${display_args[@]}"

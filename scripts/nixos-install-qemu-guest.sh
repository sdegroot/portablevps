#!/usr/bin/env bash
# Installs this flake into a QEMU guest from the NixOS installer environment.
set -euo pipefail

root_disk="${ROOT_DISK:-/dev/vda}"
host_mount="${HOST_MOUNT:-/host}"
target_repo="${TARGET_REPO:-/mnt/etc/nixos}"

if [ ! -b "$root_disk" ]; then
  echo "error: root disk not found: $root_disk" >&2
  exit 66
fi

if mountpoint -q /mnt; then
  echo "error: /mnt is already mounted" >&2
  exit 70
fi

sudo parted -s "$root_disk" -- mklabel gpt
sudo parted -s "$root_disk" -- mkpart ESP fat32 1MiB 512MiB
sudo parted -s "$root_disk" -- set 1 esp on
sudo parted -s "$root_disk" -- mkpart primary ext4 512MiB 100%

sudo mkfs.vfat -F 32 -n BOOT "${root_disk}1"
sudo mkfs.ext4 -F -L nixos "${root_disk}2"

sudo mount /dev/disk/by-label/nixos /mnt
sudo mkdir -p /mnt/boot
sudo mount /dev/disk/by-label/BOOT /mnt/boot

sudo mkdir -p "$host_mount"
if ! mountpoint -q "$host_mount"; then
  sudo mount -t 9p -o trans=virtio,version=9p2000.L hostshare "$host_mount"
fi

sudo mkdir -p "$target_repo"
sudo find "$target_repo" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
sudo tar \
  --exclude=.git \
  --exclude=.local \
  --exclude=plan.md \
  -C "$host_mount" \
  -cf - . | sudo tar -C "$target_repo" -xf -

sudo nixos-install --no-root-passwd --flake "$target_repo#local-vm"
printf 'FS0:\\EFI\\BOOT\\BOOTAA64.EFI\r\n' | sudo tee /mnt/boot/startup.nsh >/dev/null
echo "install complete; power off this VM and boot without --iso"

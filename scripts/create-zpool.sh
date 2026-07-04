#!/usr/bin/env bash
# Creates the local QEMU ZFS data pool and datasets for prototype storage.
set -euo pipefail

usage() {
  echo "usage: $0 /dev/disk/by-id/..." >&2
}

if [ "$#" -ne 1 ]; then
  usage
  exit 64
fi

disk="$1"

if [ ! -b "$disk" ]; then
  echo "error: $disk is not a block device" >&2
  exit 66
fi

if zpool list -H tank >/dev/null 2>&1; then
  echo "error: zpool tank already exists" >&2
  exit 70
fi

echo "This will destroy all data on: $disk"
if [ "${CI:-}" != "true" ] && [ "${EP_TEST_MODE:-}" != "true" ]; then
  printf "Type DESTROY to continue: "
  read -r confirmation
  if [ "$confirmation" != "DESTROY" ]; then
    echo "aborted" >&2
    exit 75
  fi
fi

zpool create \
  -f \
  -o ashift=12 \
  -O compression=lz4 \
  -O atime=off \
  -O xattr=sa \
  -O acltype=posixacl \
  -O mountpoint=none \
  tank "$disk"

zfs create -o mountpoint=/data tank/data
zfs create -o mountpoint=/data/postgres tank/data/postgres
zfs create -o mountpoint=/data/container-state tank/data/container-state

mkdir -p /data/postgres /data/container-state
zfs list tank tank/data tank/data/postgres tank/data/container-state

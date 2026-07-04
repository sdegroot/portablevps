#!/usr/bin/env bash
# Runs the local two-VM disaster recovery validation against host-local MinIO.
set -euo pipefail

VM_A_SSH="${VM_A_SSH:--i .local/qemu/test_ed25519 -o IdentitiesOnly=yes -p 2224 admin@127.0.0.1}"
VM_B_SSH="${VM_B_SSH:--i .local/qemu/test_ed25519 -o IdentitiesOnly=yes -p 2225 admin@127.0.0.1}"
REMOTE_REPO="${REMOTE_REPO:-/host}"

remote() {
  local target="$1"
  shift
  read -r -a target_args <<<"$target"
  ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new "${target_args[@]}" "$@"
}

wait_ssh() {
  local name="$1"
  local target="$2"

  for i in $(seq 1 90); do
    if remote "$target" true >/dev/null 2>&1; then
      echo "ssh ready: $name"
      return 0
    fi
    if [ "$i" -eq 90 ]; then
      echo "error: ssh not ready: $name" >&2
      return 70
    fi
    sleep 2
  done
}

mount_host_repo() {
  local name="$1"
  local target="$2"

  echo "mount: ensuring host repo is mounted on $name at $REMOTE_REPO"
  remote "$target" "
    sudo mkdir -p '$REMOTE_REPO'
    if ! findmnt '$REMOTE_REPO' >/dev/null 2>&1; then
      sudo mount -t 9p -o trans=virtio,version=9p2000.L hostshare '$REMOTE_REPO'
    fi
    test -f '$REMOTE_REPO/flake.nix'
  "
}

sync_guest_clock() {
  local name="$1"
  local target="$2"
  local host_time

  host_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "time: syncing $name clock to $host_time"
  remote "$target" "sudo date -u -s '$host_time' >/dev/null"
}

wait_ssh vm-a "$VM_A_SSH"
wait_ssh vm-b "$VM_B_SSH"
sync_guest_clock vm-a "$VM_A_SSH"
sync_guest_clock vm-b "$VM_B_SSH"
mount_host_repo vm-a "$VM_A_SSH"
mount_host_repo vm-b "$VM_B_SSH"

exec task dr:test-clean \
  VM_A_SSH="$VM_A_SSH" \
  VM_B_SSH="$VM_B_SSH" \
  REMOTE_REPO="$REMOTE_REPO"

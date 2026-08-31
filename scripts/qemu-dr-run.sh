#!/usr/bin/env bash
# One-command local two-VM disaster-recovery run: boot both throwaway QEMU VMs in
# the background, run the backup/restore validation against them, then tear the
# VMs and the test MinIO down. The headless equivalent of the three-terminal
# qemu:boot-a / qemu:boot-b / validate:qemu dance.
#
# Run from the portablevps directory (the task wrappers set the working dir).
# Overridable via environment:
#   SHARE      host directory shared into the guests, mounted at /host
#              (default: $PWD, the tool's own repo). Consumers share the monorepo
#              root so a path:../portablevps flake input resolves.
#   CONFIG     <server>-local-vm flake config to validate (default: local-vm).
#   FLAKE_DIR  directory holding the flake inside the guest mount
#              (default: /host; consumers use /host/<subdir>, e.g. /host/your-consumer).
#   VM_A_PORT / VM_B_PORT   host SSH forward ports (default: 2224 / 2225).
set -euo pipefail

SHARE="${SHARE:-$PWD}"
CONFIG="${CONFIG:-local-vm}"
FLAKE_DIR="${FLAKE_DIR:-/host}"
VM_A_PORT="${VM_A_PORT:-2224}"
VM_B_PORT="${VM_B_PORT:-2225}"
KEY="${QEMU_SSH_KEY:-.local/qemu/test_ed25519}"
STATE_DIR="${QEMU_STATE_DIR:-.local/qemu}"

boot_pids=()

cleanup() {
  local status=$?
  echo "dr:run: tearing down VMs and test MinIO" >&2
  for pid in "${boot_pids[@]:-}"; do
    [ -n "$pid" ] && kill "$pid" 2>/dev/null || true
  done
  scripts/local-s3-stop.sh >/dev/null 2>&1 || true
  exit "$status"
}
trap cleanup EXIT INT TERM

boot_vm() {
  local name="$1" port="$2"
  local log="$STATE_DIR/$name-dr-run.log"
  echo "dr:run: booting $name (ssh 127.0.0.1:$port), log: $log" >&2
  # qemu-boot-vm.sh execs qemu, so the recorded pid is the qemu process itself.
  scripts/qemu-boot-vm.sh "$name" --ssh-port "$port" --share "$SHARE" >"$log" 2>&1 &
  boot_pids+=("$!")
}

wait_ssh() {
  local port="$1" name="$2"
  for _ in $(seq 1 90); do
    if ssh -i "$KEY" -o IdentitiesOnly=yes -o BatchMode=yes \
        -o StrictHostKeyChecking=accept-new -o ConnectTimeout=4 \
        -p "$port" admin@127.0.0.1 true >/dev/null 2>&1; then
      echo "dr:run: ssh ready on $name ($port)" >&2
      return 0
    fi
    sleep 2
  done
  echo "dr:run: error: ssh never came up on $name ($port); see $STATE_DIR/$name-dr-run.log" >&2
  return 70
}

boot_vm vm-a "$VM_A_PORT"
boot_vm vm-b "$VM_B_PORT"
wait_ssh "$VM_A_PORT" vm-a
wait_ssh "$VM_B_PORT" vm-b

VM_A_SSH="-i $KEY -o IdentitiesOnly=yes -p $VM_A_PORT admin@127.0.0.1"
VM_B_SSH="-i $KEY -o IdentitiesOnly=yes -p $VM_B_PORT admin@127.0.0.1"

# qemu-validate.sh waits for SSH, mounts the host repo, resets + starts MinIO,
# and runs the disaster-recovery test.
CONFIG="$CONFIG" FLAKE_DIR="$FLAKE_DIR" \
  VM_A_SSH="$VM_A_SSH" VM_B_SSH="$VM_B_SSH" \
  scripts/qemu-validate.sh

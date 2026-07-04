#!/usr/bin/env bash
# Orchestrates the two-VM backup and restore validation scenario.
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
usage: VM_A_SSH=admin@vm-a VM_B_SSH=admin@vm-b tests/test-disaster-recovery.sh

Required environment:
  VM_A_SSH       SSH target for the source VM with working PostgreSQL.
  VM_B_SSH       SSH target for the fresh restore VM.

Optional environment:
  REMOTE_REPO    Repo path on both VMs. Default: /home/admin/portablevps-nix-infra
  MARKER         Test marker. Default: generated timestamp marker.
  INITIAL_MARKER First marker used to seed the full backup. Default: generated.
  RESTIC_REPOSITORY
                 Shared restic repo, for example s3:http://10.0.2.2:9000/portablevps-dr.

Preconditions:
  - Both VMs can be reached by SSH.
  - This repo is cloned at REMOTE_REPO on both VMs.
  - VM A has the local-vm profile applied and PostgreSQL is running.
  - VM B has the local-vm profile applied, but apps are not running.
  - RESTIC_REPOSITORY points to a repository both VMs can reach.
USAGE
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi

: "${VM_A_SSH:?set VM_A_SSH, for example admin@192.0.2.10}"
: "${VM_B_SSH:?set VM_B_SSH, for example admin@192.0.2.11}"

REMOTE_REPO="${REMOTE_REPO:-/home/admin/portablevps-nix-infra}"
MARKER="${MARKER:-fresh-server-restore-$(date -u +%Y%m%dT%H%M%SZ)-$RANDOM}"
INITIAL_MARKER="${INITIAL_MARKER:-fresh-server-full-$(date -u +%Y%m%dT%H%M%SZ)-$RANDOM}"
RESTIC_REPOSITORY="${RESTIC_REPOSITORY:-s3:http://10.0.2.2:9000/portablevps-dr}"
RESTIC_PASSWORD="${RESTIC_PASSWORD:-dev-password}"
AWS_ACCESS_KEY_ID="${AWS_ACCESS_KEY_ID:-portablevps}"
AWS_SECRET_ACCESS_KEY="${AWS_SECRET_ACCESS_KEY:-portablevps-minio-password}"
REMOTE_ENV="RESTIC_REPOSITORY='$RESTIC_REPOSITORY'"
if [ -n "${RESTIC_PASSWORD:-}" ]; then
  REMOTE_ENV="$REMOTE_ENV RESTIC_PASSWORD='$RESTIC_PASSWORD'"
fi
if [ -n "$AWS_ACCESS_KEY_ID" ]; then
  REMOTE_ENV="$REMOTE_ENV AWS_ACCESS_KEY_ID='$AWS_ACCESS_KEY_ID'"
fi
if [ -n "$AWS_SECRET_ACCESS_KEY" ]; then
  REMOTE_ENV="$REMOTE_ENV AWS_SECRET_ACCESS_KEY='$AWS_SECRET_ACCESS_KEY'"
fi

case "$RESTIC_REPOSITORY" in
  /*)
    echo "error: tests/test-disaster-recovery.sh requires a shared non-local restic repository" >&2
    echo "run scripts/local-s3-start.sh and eval \"\$(scripts/local-s3-env.sh)\"" >&2
    exit 64
    ;;
esac

remote() {
  local target="$1"
  shift
  read -r -a target_args <<<"$target"
  ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new "${target_args[@]}" "$@"
}

remote_repo() {
  local target="$1"
  shift
  remote "$target" "cd '$REMOTE_REPO' && $*"
}

require_remote_tools() {
  local target="$1"
  remote_repo "$target" "command -v sudo >/dev/null && command -v nixos-rebuild >/dev/null"
}

echo "test: fresh server can restore PostgreSQL data from backup"
echo "initial marker: $INITIAL_MARKER"
echo "marker: $MARKER"
echo "restic repository: $RESTIC_REPOSITORY"

require_remote_tools "$VM_A_SSH"
require_remote_tools "$VM_B_SSH"

echo "configuring VM A in normal mode"
remote_repo "$VM_A_SSH" "sudo nixos-rebuild switch --flake .#local-vm"
remote "$VM_A_SSH" "sudo systemctl start apps.target"
remote "$VM_A_SSH" "sudo rm -rf /var/lib/portablevps-backups/postgres-physical"

echo "inserting initial marker on VM A"
remote_repo "$VM_A_SSH" "sudo insert-test-data.sh '$INITIAL_MARKER'"
remote_repo "$VM_A_SSH" "sudo verify-test-data.sh '$INITIAL_MARKER'"

echo "creating or reusing restic repository on VM A"
remote "$VM_A_SSH" "if ! sudo env $REMOTE_ENV bash -lc 'source /etc/portablevps/restic.env; timeout 30s restic snapshots' >/dev/null 2>&1; then sudo env $REMOTE_ENV init-backup-repo.sh; fi"

echo "creating full backup on VM A"
full_backup_output="$(
  remote_repo "$VM_A_SSH" "sudo env $REMOTE_ENV backup.sh"
)"
printf '%s\n' "$full_backup_output"
if ! grep -q "postgres backup mode: full" <<<"$full_backup_output"; then
  echo "expected first backup to be full" >&2
  exit 1
fi

echo "inserting final marker on VM A"
remote_repo "$VM_A_SSH" "sudo insert-test-data.sh '$MARKER'"
remote_repo "$VM_A_SSH" "sudo verify-test-data.sh '$MARKER'"

echo "writing container file state on VM A"
remote "$VM_A_SSH" "sudo mkdir -p /data/container-state/demo && printf '%s\n' '$MARKER' | sudo tee /data/container-state/demo/marker.txt >/dev/null"

echo "creating incremental backup on VM A"
incremental_backup_output="$(
  remote_repo "$VM_A_SSH" "sudo env $REMOTE_ENV backup.sh"
)"
printf '%s\n' "$incremental_backup_output"
if ! grep -q "postgres backup mode: incremental" <<<"$incremental_backup_output"; then
  echo "expected second backup to be incremental" >&2
  exit 1
fi

echo "using shared restic repository directly: $RESTIC_REPOSITORY"

echo "configuring VM B in restore mode"
remote_repo "$VM_B_SSH" "sudo nixos-rebuild switch --flake .#local-vm-restore"
remote "$VM_B_SSH" "if sudo systemctl is-active --quiet postgres.service; then echo 'postgres.service started in restore mode' >&2; exit 1; fi"

echo "restoring VM B before apps start"
remote_repo "$VM_B_SSH" "sudo env $REMOTE_ENV restore.sh"
remote "$VM_B_SSH" "if sudo systemctl is-active --quiet postgres.service; then echo 'postgres.service started during restore' >&2; exit 1; fi"

echo "switching VM B to normal mode"
remote_repo "$VM_B_SSH" "sudo nixos-rebuild switch --flake .#local-vm"
remote "$VM_B_SSH" "sudo systemctl start apps.target"

echo "verifying restored PostgreSQL marker on VM B"
remote_repo "$VM_B_SSH" "sudo verify-test-data.sh '$MARKER'"

echo "verifying restored container file state on VM B"
remote "$VM_B_SSH" "test \"\$(sudo cat /data/container-state/demo/marker.txt)\" = '$MARKER'"

echo "PASS: fresh server restored PostgreSQL data from backup"

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
  CONFIG         Flake config to apply on the VMs (a <name>-local-vm built by
                 lib.mkFlake). Default: local-vm. Restore mode uses
                 "<CONFIG>-restore". e.g. CONFIG=myserver-local-vm.
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

CONFIG="${CONFIG:-local-vm}"
REMOTE_REPO="${REMOTE_REPO:-/home/admin/portablevps-nix-infra}"
# Directory holding the flake to apply. Defaults to REMOTE_REPO (the tool's own
# flake IS the shared dir). For a consumer whose flake references a sibling
# (path:../portablevps), share the monorepo root as REMOTE_REPO and point
# FLAKE_DIR at the consumer subdir, e.g. FLAKE_DIR=$REMOTE_REPO/epistola.
FLAKE_DIR="${FLAKE_DIR:-$REMOTE_REPO}"
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

# Run the REAL production backup path — the systemd service (which self-inits the
# repo via ExecStartPre) — rather than invoking backup.sh directly, so the test
# exercises the same execution environment (service PATH, EnvironmentFile) as a
# live host. When a PostgreSQL component is present, assert its backup mode from
# the journal; appliance-style apps may not register a PostgreSQL component.
run_backup() {
  local target="$1" expected="${2:-}"
  remote "$target" "sudo systemctl start portablevps-backup.service"
  if [ -z "$expected" ]; then
    return
  fi
  local mode
  mode="$(remote "$target" "sudo journalctl -u portablevps-backup.service -o cat --no-pager | grep -oE 'postgres backup mode: (full|incremental)' | tail -1")"
  printf '%s\n' "$mode"
  if ! grep -q "postgres backup mode: $expected" <<<"$mode"; then
    echo "expected $expected backup, got: '$mode'" >&2
    remote "$target" "sudo journalctl -u portablevps-backup.service -o cat --no-pager | tail -40" >&2
    exit 1
  fi
}

has_postgres_component() {
  local target="$1"
  remote "$target" "test -e /etc/portablevps/backups/paths.d/postgres"
}

# Seed a marker into every non-postgres backup path the running config declares.
# The backup coordinator materialises each component's paths at
# /etc/portablevps/backups/paths.d/<component>, so this exercises each app's
# registered file state (e.g. authentik's media + custom-templates) generically,
# rather than a single hardcoded demo path. The postgres component's path is the
# physical-backup scratch directory, which is covered by the PostgreSQL marker.
seed_component_markers() {
  local target="$1" marker="$2"
  remote "$target" "
    set -eu
    for f in /etc/portablevps/backups/paths.d/*; do
      [ -e \"\$f\" ] || continue
      if [ \"\$(basename \"\$f\")\" = postgres ]; then continue; fi
      while IFS= read -r p; do
        [ -n \"\$p\" ] || continue
        sudo mkdir -p \"\$p\"
        printf '%s\n' '$marker' | sudo tee \"\$p/dr-marker.txt\" >/dev/null
      done < \"\$f\"
    done
  "
}

# Assert every seeded marker survived the restore. Reads the same manifest on the
# restored host (both VMs run the same config, so the declared paths match).
verify_component_markers() {
  local target="$1" marker="$2"
  remote "$target" "
    set -eu
    rc=0
    for f in /etc/portablevps/backups/paths.d/*; do
      [ -e \"\$f\" ] || continue
      if [ \"\$(basename \"\$f\")\" = postgres ]; then continue; fi
      while IFS= read -r p; do
        [ -n \"\$p\" ] || continue
        got=\"\$(sudo cat \"\$p/dr-marker.txt\" 2>/dev/null || true)\"
        if [ \"\$got\" != '$marker' ]; then
          echo \"FAIL: dr-marker in \$p is '\$got', expected '$marker'\" >&2
          rc=1
        else
          echo \"ok: \$p/dr-marker.txt survived restore\"
        fi
      done < \"\$f\"
    done
    exit \$rc
  "
}

run_dr_hooks() {
  local target="$1" phase="$2" marker="$3"
  remote "$target" "
    set -eu
    hook_dir=/etc/portablevps/dr/$phase.d
    [ -d \"\$hook_dir\" ] || exit 0
    for hook in \$(find \"\$hook_dir\" -mindepth 1 -maxdepth 1 ! -type d | sort); do
      sudo \"\$hook\" '$marker'
    done
  "
}

verify_discourse_archive_retention() {
  local target="$1"
  remote "$target" "
    set -eu
    if [ ! -e /etc/portablevps/backups/paths.d/discourse ]; then
      exit 0
    fi
    keep=\"\$(cat /etc/portablevps/dr/discourse-local-archives-to-keep 2>/dev/null || printf '2')\"
    while IFS= read -r p; do
      [ -n \"\$p\" ] || continue
      count=\"\$(find \"\$p\" -maxdepth 1 -type f -name '*.tar.gz' 2>/dev/null | wc -l)\"
      if [ \"\$count\" -gt \"\$keep\" ]; then
        echo \"FAIL: Discourse backup archive retention kept \$count archives in \$p, expected <= \$keep\" >&2
        exit 1
      fi
      echo \"ok: Discourse backup archive retention kept \$count archives in \$p\"
    done < /etc/portablevps/backups/paths.d/discourse
  "
}

echo "test: fresh server can restore declared app state from backup"
echo "initial marker: $INITIAL_MARKER"
echo "marker: $MARKER"
echo "restic repository: $RESTIC_REPOSITORY"

require_remote_tools "$VM_A_SSH"
require_remote_tools "$VM_B_SSH"

# Reset VM A's data so the target config initialises fresh. Reused VMs may hold
# a previous run's cluster or appliance state. Clear the *contents* of generic
# dirs and known app roots before switching into the tested config.
echo "resetting VM A data for a clean ${CONFIG} install"
remote "$VM_A_SSH" "
  sudo systemctl stop apps.target >/dev/null 2>&1 || true
  # 'systemctl stop apps.target' propagates a stop to its PartOf members but
  # returns without waiting for those container units to finish stopping, so the
  # app containers can still be 'deactivating' (and writing) here. Wiping their
  # data immediately then races a live container into 'Directory not empty' ->
  # 'PostgreSQL is not ready'. Wait for the app containers to actually exit first
  # (generic: no per-app knowledge, just 'no app containers left').
  for _ in \$(seq 1 30); do
    podman_running=\"\$(command -v podman >/dev/null 2>&1 && sudo podman ps -q || true)\"
    docker_running=\"\$(command -v docker >/dev/null 2>&1 && sudo docker ps -q || true)\"
    [ -z \"\$podman_running\$docker_running\" ] && break
    sleep 1
  done
  sudo mkdir -p /data/postgres /data/container-state
  sudo find /data/postgres -mindepth 1 -maxdepth 1 -exec rm -rf {} +
  sudo find /data/container-state -mindepth 1 -maxdepth 1 -exec rm -rf {} +
  sudo rm -rf /data/discourse
  sudo rm -rf /var/lib/portablevps-backups/postgres-physical
  sudo systemctl mask --runtime portablevps-backup.timer portablevps-backup-maintenance.timer >/dev/null 2>&1 || true
"

echo "configuring VM A in normal mode"
remote "$VM_A_SSH" "cd '$FLAKE_DIR' && sudo nixos-rebuild switch --flake .#${CONFIG}"
remote "$VM_A_SSH" "sudo systemctl start apps.target"
# The scheduled timers are enabled in normal mode. Disable them for this
# controlled sequence so a Persistent= true timer cannot race the manual
# full/incremental backups and make journal-mode assertions non-deterministic.
remote "$VM_A_SSH" "sudo systemctl stop portablevps-backup.timer portablevps-backup-maintenance.timer >/dev/null 2>&1 || true"

if has_postgres_component "$VM_A_SSH"; then
  echo "inserting initial PostgreSQL marker on VM A"
  remote_repo "$VM_A_SSH" "sudo insert-test-data.sh '$INITIAL_MARKER'"
  remote_repo "$VM_A_SSH" "sudo verify-test-data.sh '$INITIAL_MARKER'"

  echo "creating full backup on VM A (portablevps-backup.service)"
  run_backup "$VM_A_SSH" full

  echo "inserting final PostgreSQL marker on VM A"
  remote_repo "$VM_A_SSH" "sudo insert-test-data.sh '$MARKER'"
  remote_repo "$VM_A_SSH" "sudo verify-test-data.sh '$MARKER'"
else
  echo "no PostgreSQL backup component registered; skipping PostgreSQL marker seed"
fi

echo "writing container file state on VM A"
remote "$VM_A_SSH" "sudo mkdir -p /data/container-state/demo && printf '%s\n' '$MARKER' | sudo tee /data/container-state/demo/marker.txt >/dev/null"

echo "seeding a marker into every app-registered backup path on VM A"
seed_component_markers "$VM_A_SSH" "$MARKER"

echo "running app-owned DR seed hooks on VM A"
run_dr_hooks "$VM_A_SSH" seed "$MARKER"

if has_postgres_component "$VM_A_SSH"; then
  echo "creating incremental backup on VM A (portablevps-backup.service)"
  run_backup "$VM_A_SSH" incremental
else
  echo "creating backup on VM A (portablevps-backup.service)"
  run_backup "$VM_A_SSH"
fi
verify_discourse_archive_retention "$VM_A_SSH"

echo "using shared restic repository directly: $RESTIC_REPOSITORY"

echo "configuring VM B in restore mode"
remote "$VM_B_SSH" "cd '$FLAKE_DIR' && sudo nixos-rebuild switch --flake .#${CONFIG}-restore"
remote "$VM_B_SSH" "if systemctl list-unit-files postgres.service >/dev/null 2>&1 && sudo systemctl is-active --quiet postgres.service; then echo 'postgres.service started in restore mode' >&2; exit 1; fi"

echo "restoring VM B before apps start"
remote "$VM_B_SSH" "sudo restore.sh"
remote "$VM_B_SSH" "if systemctl list-unit-files postgres.service >/dev/null 2>&1 && sudo systemctl is-active --quiet postgres.service; then echo 'postgres.service started during restore' >&2; exit 1; fi"

echo "switching VM B to normal mode"
remote "$VM_B_SSH" "cd '$FLAKE_DIR' && sudo nixos-rebuild switch --flake .#${CONFIG}"
remote "$VM_B_SSH" "sudo systemctl start apps.target"
verify_discourse_archive_retention "$VM_B_SSH"

if has_postgres_component "$VM_B_SSH"; then
  echo "verifying restored PostgreSQL marker on VM B"
  remote_repo "$VM_B_SSH" "sudo verify-test-data.sh '$MARKER'"
else
  echo "no PostgreSQL backup component registered; skipping PostgreSQL marker verify"
fi

echo "verifying restored container file state on VM B"
remote "$VM_B_SSH" "test \"\$(sudo cat /data/container-state/demo/marker.txt)\" = '$MARKER'"

echo "verifying every app-registered backup path was restored on VM B"
verify_component_markers "$VM_B_SSH" "$MARKER"

echo "running app-owned DR verify hooks on VM B"
run_dr_hooks "$VM_B_SSH" verify "$MARKER"

echo "PASS: fresh server restored declared app state from backup"

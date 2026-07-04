#!/usr/bin/env bash
# Prints backup timer, restore rehearsal markers, and latest restic snapshot status.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
if [ -r "$script_dir/lib/runtime-env.sh" ]; then
  # shellcheck disable=SC1091
  source "$script_dir/lib/runtime-env.sh"
else
  # shellcheck disable=SC1091
  source "$script_dir/runtime-env.sh"
fi

load_restic_env

echo "== systemd timers =="
systemctl list-timers epistola-backup.timer --no-pager || true

echo
echo "== backup service =="
systemctl status epistola-backup.service --no-pager -l || true

echo
echo "== latest restic snapshots =="
if wait_restic_available; then
  restic snapshots --latest 5
else
  echo "restic repository unavailable: $RESTIC_REPOSITORY"
fi

echo
echo "== restore rehearsal markers =="
if [ -d .local/cloud-restore ]; then
  find .local/cloud-restore -mindepth 1 -maxdepth 1 -type f -name '*.marker' -print | sort
else
  echo "no local restore rehearsal markers"
fi

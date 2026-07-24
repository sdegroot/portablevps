#!/usr/bin/env bash
# Initializes the configured restic repository if it is not already usable.
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

if [ "${PORTABLEVPS_BACKUP_LOCKED:-0}" != 1 ] && command -v flock >/dev/null 2>&1; then
  mkdir -p /run/lock
  export PORTABLEVPS_BACKUP_LOCKED=1
  exec flock -w "${PORTABLEVPS_BACKUP_LOCK_WAIT_SECONDS:-900}" /run/lock/portablevps-backups.lock "$0" "$@"
fi

case "$RESTIC_REPOSITORY" in
  /*)
    mkdir -p "$RESTIC_REPOSITORY"
    ;;
esac

if wait_restic_available; then
  echo "restic repository already initialized: $RESTIC_REPOSITORY"
  exit 0
fi

init_output="$(mktemp)"
trap 'rm -f "$init_output"' EXIT
if restic init >"$init_output" 2>&1; then
  cat "$init_output"
  exit 0
fi
init_rc=$?

if wait_restic_available; then
  echo "restic repository initialized by another process: $RESTIC_REPOSITORY"
  exit 0
fi

cat "$init_output" >&2
exit "$init_rc"

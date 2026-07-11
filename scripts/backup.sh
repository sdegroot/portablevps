#!/usr/bin/env bash
# Runs all registered backup component hooks and stores their paths in one restic snapshot.
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

BACKUP_CONFIG_DIR="${BACKUP_CONFIG_DIR:-/etc/portablevps/backups}"

if ! wait_restic_available; then
  echo "error: restic repository is not initialized: $RESTIC_REPOSITORY" >&2
  echo "run: RESTIC_REPOSITORY=$RESTIC_REPOSITORY RESTIC_PASSWORD=... restic init" >&2
  exit 69
fi

run_hooks() {
  local hook_dir="$1"

  if [ ! -d "$hook_dir" ]; then
    return 0
  fi

  while IFS= read -r hook; do
    [ -n "$hook" ] || continue
    "$hook"
  done < <(find "$hook_dir" -mindepth 1 -maxdepth 1 ! -type d | sort)
}

read_paths() {
  local paths_dir="$1"

  if [ ! -d "$paths_dir" ]; then
    return 0
  fi

  while IFS= read -r path_file; do
    cat "$path_file"
  done < <(find "$paths_dir" -mindepth 1 -maxdepth 1 ! -type d | sort) |
    sed '/^[[:space:]]*$/d' |
    sort -u
}

run_hooks "$BACKUP_CONFIG_DIR/pre-backup.d"

mapfile -t registered_paths < <(read_paths "$BACKUP_CONFIG_DIR/paths.d")
if [ "${#registered_paths[@]}" -eq 0 ]; then
  echo "error: no backup component paths are registered in $BACKUP_CONFIG_DIR/paths.d" >&2
  exit 78
fi

# Only hand restic paths that currently exist. Some registered paths are created
# lazily by their service — most notably Traefik's acme.json, which appears only
# after the first certificate is issued. restic exits non-zero when given a
# missing path, and under `set -euo pipefail` that command substitution failure
# aborts the whole backup, silently leaving a fresh host with ZERO snapshots
# until the file materialises. Warn about missing paths (visible in the journal)
# but back up everything that does exist.
backup_paths=()
for path in "${registered_paths[@]}"; do
  if [ -e "$path" ]; then
    backup_paths+=("$path")
  else
    echo "warning: registered backup path does not exist yet, skipping: $path" >&2
  fi
done
if [ "${#backup_paths[@]}" -eq 0 ]; then
  echo "error: none of the registered backup paths exist yet in $BACKUP_CONFIG_DIR/paths.d" >&2
  exit 78
fi

backup_output="$(restic backup --json "${backup_paths[@]}")"
printf '%s\n' "$backup_output" |
  jq -r '
    select(.message_type == "summary") |
    "processed \(.total_files_processed) files, \(.total_bytes_processed) bytes in \(.total_duration | floor)s\nsnapshot \(.snapshot_id[0:8]) saved"
  '
snapshot_id="$(
  printf '%s\n' "$backup_output" |
    jq -r 'select(.message_type == "summary") | .snapshot_id[0:8]' |
    tail -n 1
)"

echo "backup complete: $snapshot_id"

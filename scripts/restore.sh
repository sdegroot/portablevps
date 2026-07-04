#!/usr/bin/env bash
# Restores the latest restic snapshot and runs module-owned post-restore hooks.
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

restore_mode="false"
if [ -r /etc/portablevps/restore-mode ]; then
  restore_mode="$(tr -d '[:space:]' </etc/portablevps/restore-mode)"
fi

apps_inactive=true
if systemctl is-active --quiet apps.target; then
  apps_inactive=false
fi

if [ "$restore_mode" != "true" ] && [ "$apps_inactive" != "true" ]; then
  echo "error: refusing restore unless portablevps.restoreMode=true or apps.target is inactive" >&2
  exit 78
fi

if ! wait_restic_available 12 5; then
  echo "error: restic repository is not initialized or unavailable: $RESTIC_REPOSITORY" >&2
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

require_safe_clear_path() {
  local path="$1"

  case "$path" in
    "" | / | /data | /var | /var/lib | /home | /root | /etc | /run | /tmp)
      echo "error: refusing unsafe restore clear path: $path" >&2
      exit 78
      ;;
  esac
}

run_hooks "$BACKUP_CONFIG_DIR/pre-restore.d"

mapfile -t clear_paths < <(read_paths "$BACKUP_CONFIG_DIR/clear-before-restore.d")
for path in "${clear_paths[@]}"; do
  require_safe_clear_path "$path"
  rm -rf "$path"
done

restic restore latest --target /
run_hooks "$BACKUP_CONFIG_DIR/post-restore.d"

echo "restore complete; module restore hooks ran and app services are intentionally not started"
echo "next: nixos-rebuild switch --flake .#local-vm"

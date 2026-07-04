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

case "$RESTIC_REPOSITORY" in
  /*)
    mkdir -p "$RESTIC_REPOSITORY"
    ;;
esac

if wait_restic_available; then
  echo "restic repository already initialized: $RESTIC_REPOSITORY"
  exit 0
fi

restic init

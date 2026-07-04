#!/usr/bin/env bash
# Lists local QEMU VMs and their disk images.
set -euo pipefail

state_dir="${QEMU_STATE_DIR:-.local/qemu}"

if [ ! -d "$state_dir" ]; then
  exit 0
fi

find "$state_dir" -mindepth 1 -maxdepth 1 -type d -print | sort

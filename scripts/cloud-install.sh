#!/usr/bin/env bash
# Compatibility wrapper for installing a provider VPS through the Python cloud CLI.
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
usage: SERVER=test-vps TARGET=root@1.2.3.4 [DISK=/dev/sda] [RESTORE_MODE=1] scripts/cloud-install.sh

Required:
  SERVER  Logical server key from servers/.
  TARGET      Existing VPS or rescue system reachable over SSH.

Optional:
  DISK      Target disk to partition. Default: provider defaultDisk.
  DEPLOYMENT Compatibility alias for SERVER.
USAGE
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
exec python3 "$script_dir/cloud.py" install

#!/usr/bin/env bash
# Compatibility wrapper for cloud launch smoke tests implemented by the Python cloud CLI.
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
usage: SERVER=test-vps HOST=1.2.3.4 CONFIRM_DESTROY=1.2.3.4 scripts/cloud-launch-smoke-test.sh

Required:
  SERVER            Logical server key from servers/.
  HOST              Public host/IP to test after installation.
  CONFIRM_DESTROY   Must exactly match HOST before the disk-wiping install runs.

Optional:
  TARGET            Install SSH target. Default: provider defaultTargetUser@$HOST.
  DISK              Target disk. Default: auto-detect first disk from lsblk.
  SSH_PORT          SSH port for root/admin access. Default: 22.
  ROOT_IDENTITY     SSH private key for the root/rescue target.
  CLOUD_ADMIN_KEY   SSH private key for the installed admin user.
  DEPLOYMENT        Compatibility alias for SERVER.
USAGE
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
exec python3 "$script_dir/cloud.py" smoke-test

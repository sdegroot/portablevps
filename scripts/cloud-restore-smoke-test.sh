#!/usr/bin/env bash
# Compatibility wrapper for the two-phase cloud restore test in the Python cloud CLI.
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
usage:
  PHASE=prepare SERVER=test-vps SOURCE_HOST=1.2.3.4 scripts/cloud-restore-smoke-test.sh
  PHASE=restore SERVER=test-vps SOURCE_HOST=1.2.3.4 RESTORE_HOST=5.6.7.8 CONFIRM_DESTROY=5.6.7.8 scripts/cloud-restore-smoke-test.sh

Legacy same-host form:
  PHASE=prepare SERVER=test-vps HOST=1.2.3.4 scripts/cloud-restore-smoke-test.sh
  PHASE=restore SERVER=test-vps HOST=1.2.3.4 CONFIRM_DESTROY=1.2.3.4 scripts/cloud-restore-smoke-test.sh

prepare:
  Runs against an installed source host. Inserts a marker and backs it up to
  the configured restic repository.

restore:
  Runs after a disposable restore VPS has been manually booted into rescue
  mode. It wipes the restore disk, installs the restore profile, restores data,
  starts apps for validation, and verifies the prepared marker.

Optional:
  TARGET            Rescue SSH target for restore. Default: provider defaultTargetUser@$RESTORE_HOST.
  DISK              Target disk. Default: auto-detect first disk from lsblk.
  SSH_PORT          SSH port for root/admin access. Default: 22.
  ROOT_IDENTITY     SSH private key for the root/rescue target.
  KEXEC_EXTRA_FLAGS Extra nixos-anywhere kexec flags.
  CLOUD_ADMIN_KEY   SSH private key for the installed admin user.
  RESTORE_HOSTNAME  Hostname override for the restore host.
  RESTORE_NETBIRD_NAME
                    Netbird peer-name override for the restore host.
  FINALIZE_NORMAL   Switch restored host to normal profile. Default: true.
  MARKER            Explicit marker to verify during restore.
  DEPLOYMENT        Compatibility alias for SERVER.
USAGE
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
exec python3 "$script_dir/cloud.py" restore-test

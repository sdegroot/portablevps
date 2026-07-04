#!/usr/bin/env bash
# Creates a timestamped ZFS snapshot of the local prototype data pool.
set -euo pipefail

name="${1:-manual-test-$(date -u +%Y%m%dT%H%M%SZ)}"
snapshot="tank/data/postgres@$name"

zfs snapshot "$snapshot"
echo "$snapshot"

#!/usr/bin/env bash
# Fails unless every pvps binary under DIST carries the pinned tool flake refs
# from cli/flake.lock (see tool-flake-refs.sh). Guards against shipping a
# release whose nixos-rebuild silently follows the operator's flake registry.
#
#   scripts/verify-tool-flake-pins.sh dist
set -euo pipefail

dist="${1:?usage: $0 <goreleaser dist dir>}"
here="$(cd "$(dirname "$0")" && pwd)"
refs=$("$here/tool-flake-refs.sh")

status=0
found=0
while IFS= read -r bin; do
  found=$((found + 1))
  while IFS='=' read -r key ref; do
    if grep -qF -- "$ref" "$bin"; then
      echo "ok   $bin carries $key=$ref"
    else
      echo "FAIL $bin lacks $key=$ref" >&2
      status=1
    fi
  done <<<"$refs"
done < <(find "$dist" -type f -name pvps)
if [ "$found" -eq 0 ]; then
  echo "no pvps binaries under $dist" >&2
  exit 1
fi
exit "$status"

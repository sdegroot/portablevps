#!/usr/bin/env bash
# Prints the exact flake refs the CLI runs tools from (nixpkgs#nixos-rebuild and
# nixos-anywhere), pinned to cli/flake.lock, as KEY=value lines for
# $GITHUB_ENV / GoReleaser's ldflags. A build without them resolves `nixpkgs`
# through the operator's flake registry (nixpkgs-unstable HEAD), whose
# nixos-rebuild broke deploys on 2026-09-30. `nix build` pins the same inputs
# through the flake itself (flake.nix ldflags).
#
#   scripts/tool-flake-refs.sh [path/to/flake.lock]
set -euo pipefail

lock="${1:-$(cd "$(dirname "$0")/.." && pwd)/flake.lock}"

ref() {
  jq -er --arg input "$1" '
    .nodes[.nodes.root.inputs[$input]].locked
    | select(.type == "github" and (.rev | test("^[0-9a-f]{40}$")))
    | "github:\(.owner)/\(.repo)/\(.rev)"' "$lock"
}

nixpkgs=$(ref nixpkgs)
nixos_anywhere=$(ref nixos-anywhere)
echo "PVPS_NIXPKGS_FLAKE=$nixpkgs"
echo "PVPS_NIXOS_ANYWHERE_FLAKE=$nixos_anywhere"

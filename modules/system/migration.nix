# Declares application writers that must be stopped for a consistent planned
# service cutover. PostgreSQL intentionally stays online for the final backup.
{ config, lib, pkgs, ... }:

let
  cfg = config.portablevps.migration;
  quotedUnits = lib.concatMapStringsSep " " lib.escapeShellArg cfg.quiesceUnits;
in
{
  options.portablevps.migration.quiesceUnits = lib.mkOption {
    type = lib.types.listOf lib.types.str;
    default = [ ];
    description = ''
      Application writer units stopped before a final migration backup. Do not
      include postgres.service: the physical backup requires it to remain live.
    '';
  };

  config.environment.systemPackages = [
    (pkgs.writeShellScriptBin "portablevps-quiesce-writers" ''
      set -euo pipefail
      units=(${quotedUnits})
      if [ "''${#units[@]}" -eq 0 ]; then
        echo "error: no portablevps.migration.quiesceUnits declared" >&2
        exit 78
      fi
      systemctl stop "''${units[@]}"
      for unit in "''${units[@]}"; do
        if systemctl is-active --quiet "$unit"; then
          echo "error: writer unit is still active: $unit" >&2
          exit 70
        fi
      done
      if ! systemctl is-active --quiet postgres.service; then
        echo "error: postgres.service stopped while writers were quiesced" >&2
        exit 70
      fi
    '')
  ];
}

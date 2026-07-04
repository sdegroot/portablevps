# Adds restore mode and the apps.target gate used to keep services stopped during restore.
{ lib, config, ... }:

let
  cfg = config.portablevps;
in
{
  options.portablevps.restoreMode = lib.mkOption {
    type = lib.types.bool;
    default = false;
    description = "Keep application services stopped so /data can be restored first.";
  };

  config = {
    environment.etc."portablevps/restore-mode".text =
      if cfg.restoreMode then "true\n" else "false\n";

    system.activationScripts.restoreModeRuntimeMarker.text =
      if cfg.restoreMode then ''
        mkdir -p /run/portablevps
        touch /run/portablevps/restore-mode
        if [ -e /run/systemd/system ]; then
          /run/current-system/sw/bin/systemctl stop postgres.service apps.target || true
        fi
      '' else ''
        rm -f /run/portablevps/restore-mode
      '';

    systemd.targets.apps = {
      description = "portablevps application services";
      wantedBy = lib.optional (!cfg.restoreMode) "multi-user.target";
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];
    };
  };
}

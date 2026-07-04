# Declares durable container-owned file state and registers it for backup/restore.
{ lib, config, ... }:

let
  cfg = config.portablevps.containerState;
in
{
  options.portablevps.containerState = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Enable durable container-owned file state.";
    };

    path = lib.mkOption {
      type = lib.types.str;
      default = "/data/container-state";
      description = "Directory for durable container-owned files included in backups.";
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.tmpfiles.rules = [
      "d ${cfg.path} 0755 root root -"
    ];

    portablevps.backups.components."container-state" = {
      order = 20;
      paths = [ cfg.path ];
      clearBeforeRestore = [ cfg.path ];
    };
  };
}

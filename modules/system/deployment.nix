# Records logical server identity used by orchestration and host validation.
{ lib, config, ... }:

{
  options.portablevps.server = {
    name = lib.mkOption {
      type = lib.types.str;
      default = config.networking.hostName;
      description = "Logical server name.";
    };

    provider = lib.mkOption {
      type = lib.types.str;
      default = config.portablevps.cloud.providerName or "cloud";
      description = "Active provider placement for this server.";
    };

    backupRepository = lib.mkOption {
      type = lib.types.str;
      default = "";
      description = "Explicit restic repository URL for this server.";
    };
  };

  config = {
    portablevps.backups.restic.repository = lib.mkIf (config.portablevps.server.backupRepository != "")
      (lib.mkDefault config.portablevps.server.backupRepository);
  };
}

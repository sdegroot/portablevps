# Records logical server identity used by orchestration and host validation.
{ lib, config, ... }:

{
  options.my.server = {
    name = lib.mkOption {
      type = lib.types.str;
      default = config.networking.hostName;
      description = "Logical server name.";
    };

    provider = lib.mkOption {
      type = lib.types.str;
      default = config.my.cloud.providerName or "cloud";
      description = "Active provider placement for this server.";
    };

    backupRepository = lib.mkOption {
      type = lib.types.str;
      default = "";
      description = "Explicit restic repository URL for this server.";
    };
  };

  options.my.deployment = {
    name = lib.mkOption {
      type = lib.types.str;
      default = config.networking.hostName;
      description = "Compatibility alias for my.server.name.";
    };

    provider = lib.mkOption {
      type = lib.types.str;
      default = config.my.cloud.providerName or "cloud";
      description = "Compatibility alias for my.server.provider.";
    };

    backupRepository = lib.mkOption {
      type = lib.types.str;
      default = "";
      description = "Compatibility alias for my.server.backupRepository.";
    };
  };

  config = {
    my.server = {
      name = lib.mkDefault config.my.deployment.name;
      provider = lib.mkDefault config.my.deployment.provider;
      backupRepository = lib.mkDefault config.my.deployment.backupRepository;
    };

    my.backups.restic.repository = lib.mkIf (config.my.server.backupRepository != "")
      (lib.mkDefault config.my.server.backupRepository);
  };
}

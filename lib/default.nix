# portablevps flake library: assembles NixOS configurations for portable
# single-instance servers from a consumer's logical server definitions.
#
# Consumers call `portablevps.lib.mkFlake` from their own flake; the tool's
# own flake uses `mkHost` directly for its local QEMU disaster-recovery hosts.
{ nixpkgs, nixpkgs-postgres, nixpkgs-netbird, disko, sops-nix, toolRoot }:

let
  lib = nixpkgs.lib;

  # Always-applied system modules. Every portablevps host gets restore mode,
  # secrets wiring, and the logical-server identity, regardless of platform.
  coreModules = [
    sops-nix.nixosModules.sops
    (toolRoot + "/modules/system/deployment.nix")
    (toolRoot + "/modules/system/restore-mode.nix")
    (toolRoot + "/modules/system/secrets.nix")
  ];

  mkHost =
    { system
    , hostModule
    , self ? null
    , restoreMode ? false
    , extraModules ? [ ]
    }:
    let
      postgresPkgs = import nixpkgs-postgres { inherit system; };
      netbirdPkgs = import nixpkgs-netbird { inherit system; };
    in
    lib.nixosSystem {
      inherit system;
      specialArgs = { inherit self restoreMode postgresPkgs netbirdPkgs; };
      modules = [ hostModule ]
        ++ extraModules
        ++ coreModules
        ++ [ ({ ... }: { my.restoreMode = restoreMode; }) ];
    };

  # Read provider metadata (providers/<name>/provider.json) from a directory.
  readProviders = providerDir:
    let
      entries = builtins.readDir providerDir;
      names = lib.filter (name: entries.${name} == "directory") (builtins.attrNames entries);
    in
    lib.genAttrs names
      (name: builtins.fromJSON (builtins.readFile (providerDir + "/${name}/provider.json")));

  # Built-in server profiles, referenced by name from a server definition's
  # `profile` field. A server may also set `profile` to a path for a custom
  # profile defined in the consumer repository.
  builtinProfiles = {
    single-instance-app = toolRoot + "/modules/profiles/single-instance-app.nix";
  };

  resolveProfile = profile:
    if builtins.isString profile
    then builtinProfiles.${profile} or (throw "portablevps: unknown built-in profile \"${profile}\"; known profiles: ${lib.concatStringsSep ", " (builtins.attrNames builtinProfiles)}")
    else profile;

  # Read logical servers (servers/<name>.nix) from a directory.
  readServers = serverDir:
    let
      entries = builtins.readDir serverDir;
      fileNames = lib.filter
        (name: entries.${name} == "regular" && lib.hasSuffix ".nix" name)
        (builtins.attrNames entries);
      names = map (lib.removeSuffix ".nix") fileNames;
    in
    lib.genAttrs names (name: import (serverDir + "/${name}.nix") { });

  # The portablevps CLI writes install-time overrides (disk device, temporary
  # hostname / VPN name during migration) as cloud-override.nix at the consumer
  # flake root. It is injected here from the consumer's own source tree so the
  # tool can remain an external flake input.
  overrideModules = self:
    lib.optional
      (self != null && builtins.pathExists (self + "/cloud-override.nix"))
      (self + "/cloud-override.nix");

  mkCloudServer =
    { self, servers, providers, serverName, restoreMode }:
    let
      server = servers.${serverName};
      providerName = server.placement.provider;
      providerConfig = providers.${providerName};
    in
    mkHost {
      inherit self restoreMode;
      system = providerConfig.system;
      hostModule = toolRoot + "/hosts/cloud-vps.nix";
      extraModules = [
        disko.nixosModules.disko
        (resolveProfile server.profile)
        server.module
        ({ ... }: {
          _module.args = {
            serverConfig = server.info;
            inherit serverName providerName providerConfig;
          };
        })
      ] ++ overrideModules self;
    };

  # Consumer-facing entry point. Produces nixosConfigurations (a normal and a
  # -restore variant per logical server) and a serverInfo attrset consumed by
  # the portablevps CLI.
  mkFlake =
    { self
    , serverDir
    , providerDir ? (toolRoot + "/providers")
    }:
    let
      providers = readProviders providerDir;
      servers = readServers serverDir;
      serverNames = builtins.attrNames servers;
      cloudConfigurations = lib.listToAttrs (lib.concatMap
        (serverName: [
          { name = serverName; value = mkCloudServer { inherit self servers providers serverName; restoreMode = false; }; }
          { name = "${serverName}-restore"; value = mkCloudServer { inherit self servers providers serverName; restoreMode = true; }; }
        ])
        serverNames);
    in
    {
      nixosConfigurations = cloudConfigurations;
      serverInfo = lib.mapAttrs (_name: server: server.info) servers;
    };
in
{
  inherit mkHost mkCloudServer mkFlake readProviders readServers;
}

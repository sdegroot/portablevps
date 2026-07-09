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
  # auto-upgrade is available fleet-wide but disabled unless a host opts in.
  coreModules = [
    sops-nix.nixosModules.sops
    (toolRoot + "/modules/system/deployment.nix")
    (toolRoot + "/modules/system/restore-mode.nix")
    (toolRoot + "/modules/system/secrets.nix")
    (toolRoot + "/modules/system/auto-upgrade.nix")
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
        ++ [ ({ ... }: { portablevps.restoreMode = restoreMode; }) ];
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
    idle = toolRoot + "/modules/profiles/idle.nix";
    single-instance-app = toolRoot + "/modules/profiles/single-instance-app.nix";
    monitoring-server = toolRoot + "/modules/profiles/monitoring-server.nix";
    web-app = toolRoot + "/modules/profiles/web-app.nix";
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

  # Build a logical server as a local QEMU VM: its real profile + module + app
  # stack + backup components, but on the local-vm platform with prototype
  # secrets and no mesh (see hosts/local-vm-server.nix). Used to exercise a
  # server's backup/restore steps on a laptop. Defaults to the operator's likely
  # local arch; override `system` for a different VM architecture.
  mkLocalVm =
    { self, servers, providers ? { }, serverName, restoreMode }:
    let
      server = servers.${serverName};
      # "local" is a provider like any other; its metadata carries the VM
      # architecture and (for documentation) its capabilities.
      localProvider = providers.local or { };
      system = localProvider.system or "aarch64-linux";
    in
    mkHost {
      inherit self restoreMode system;
      hostModule = toolRoot + "/hosts/local-vm-server.nix";
      extraModules = [
        (resolveProfile server.profile)
        server.module
        ({ ... }: {
          _module.args = {
            serverConfig = server.info;
            inherit serverName;
            providerName = "local-vm";
            providerConfig = { };
          };
        })
      ] ++ overrideModules self;
    };

  # Consumer-facing entry point. Produces nixosConfigurations (a normal and a
  # -restore variant per logical server, plus local-VM variants for DR testing)
  # and a serverInfo attrset consumed by the portablevps CLI.
  mkFlake =
    { self
    , serverDir
    , providerDir ? (toolRoot + "/providers")
    , netbird ? { }
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
      localVmConfigurations = lib.listToAttrs (lib.concatMap
        (serverName: [
          { name = "${serverName}-local-vm"; value = mkLocalVm { inherit self servers providers serverName; restoreMode = false; }; }
          { name = "${serverName}-local-vm-restore"; value = mkLocalVm { inherit self servers providers serverName; restoreMode = true; }; }
        ])
        serverNames);
    in
    {
      nixosConfigurations = cloudConfigurations // localVmConfigurations;
      serverInfo = lib.mapAttrs (_name: server: server.info) servers;
      # Fleet-level NetBird intent (access policies), consumed by
      # `cloud:netbird-policy-sync`. Not per-server: policies are cross-cutting.
      netbird = {
        policies = netbird.policies or [ ];
        disableDefaultPolicy = netbird.disableDefaultPolicy or false;
      };
    };
in
{
  inherit mkHost mkCloudServer mkLocalVm mkFlake readProviders readServers;
}

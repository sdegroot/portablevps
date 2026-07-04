# Installs and joins Netbird using sops-managed setup credentials.
{ config, lib, pkgs, netbirdPkgs ? pkgs, ... }:

let
  cfg = config.portablevps.netbird;
  netbirdPackage = netbirdPkgs.netbird;
  netbirdDaemon = pkgs.writeShellScript "netbird-daemon" ''
    exec ${netbirdPackage}/bin/netbird \
      --management-url ${lib.escapeShellArg cfg.managementUrl} \
      --admin-url ${lib.escapeShellArg cfg.adminUrl} \
      --hostname ${lib.escapeShellArg cfg.name} \
      --log-file console \
      service run
  '';
  netbirdJoin = pkgs.writeShellScript "netbird-join" ''
    for _ in $(seq 1 60); do
      if [ -S /run/netbird/sock ]; then
        break
      fi
      sleep 1
    done

    if [ ! -S /run/netbird/sock ]; then
      echo "error: Netbird daemon socket did not become ready" >&2
      exit 1
    fi

    exec ${netbirdPackage}/bin/netbird \
      --management-url ${lib.escapeShellArg cfg.managementUrl} \
      --admin-url ${lib.escapeShellArg cfg.adminUrl} \
      --hostname ${lib.escapeShellArg cfg.name} \
      up --setup-key-file ${config.sops.secrets."netbird/setup-key".path}
  '';
in
{
  options.portablevps.netbird = {
    enable = lib.mkEnableOption "Netbird auto-join";

    interface = lib.mkOption {
      type = lib.types.str;
      default = config.portablevps.cloud.netbirdInterface or "wt0";
      description = "Expected Netbird WireGuard interface.";
    };

    name = lib.mkOption {
      type = lib.types.str;
      default = config.networking.hostName;
      description = "Explicit Netbird peer name for this server.";
    };

    managementUrl = lib.mkOption {
      type = lib.types.str;
      default = "https://api.netbird.io:443";
      description = "Netbird management service URL.";
    };

    adminUrl = lib.mkOption {
      type = lib.types.str;
      default = "https://app.netbird.io:443";
      description = "Netbird admin panel URL.";
    };
  };

  config = lib.mkIf cfg.enable {
    environment.systemPackages = [ netbirdPackage ];

    sops.secrets."netbird/setup-key" = { };

    systemd.tmpfiles.rules = [
      "d /var/lib/netbird 0700 root root -"
      "d /var/log/netbird 0750 root root -"
    ];

    systemd.services.netbird = {
      description = "Netbird daemon";
      wantedBy = [ "multi-user.target" ];
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];
      serviceConfig = {
        Type = "simple";
        Restart = "always";
        RestartSec = "5s";
        RuntimeDirectory = "netbird";
        ExecStart = netbirdDaemon;
      };
    };

    systemd.services.netbird-join = {
      description = "Join Netbird network";
      wantedBy = [ "multi-user.target" ];
      after = [ "netbird.service" ];
      requires = [ "netbird.service" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        Restart = "on-failure";
        RestartSec = "10s";
        ExecStart = netbirdJoin;
      };
    };
  };
}

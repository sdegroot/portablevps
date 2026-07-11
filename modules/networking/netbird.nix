# NetBird backend for portablevps.network. Installs and joins NetBird using
# sops-managed setup credentials, and reports the contract values
# (interface, joinUnit) that the rest of portablevps consumes.
{ config, lib, pkgs, netbirdPkgs ? pkgs, ... }:

let
  net = config.portablevps.network;
  cfg = net.netbird;
  active = net.enable && net.backend == "netbird";
  netbirdPackage = netbirdPkgs.netbird;
  netbirdDaemon = pkgs.writeShellScript "netbird-daemon" ''
    exec ${netbirdPackage}/bin/netbird \
      --management-url ${lib.escapeShellArg cfg.managementUrl} \
      --admin-url ${lib.escapeShellArg cfg.adminUrl} \
      --hostname ${lib.escapeShellArg net.name} \
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
      echo "error: NetBird daemon socket did not become ready" >&2
      exit 1
    fi

    # Retry the join: at boot the management URL or DNS can be briefly
    # unreachable. This is a Type=oneshot unit, and systemd ignores Restart= for
    # oneshot services, so the retry MUST live here — a mesh-only host that fails
    # to join is otherwise unreachable until a manual restart. Once the peer is
    # registered, subsequent boots reconnect from stored state (the single-use
    # setup key is already spent, which is fine).
    for attempt in $(seq 1 10); do
      if ${netbirdPackage}/bin/netbird \
          --management-url ${lib.escapeShellArg cfg.managementUrl} \
          --admin-url ${lib.escapeShellArg cfg.adminUrl} \
          --hostname ${lib.escapeShellArg net.name} \
          up --setup-key-file ${config.sops.secrets.${cfg.setupKeySecret}.path}; then
        exit 0
      fi
      echo "netbird up failed (attempt $attempt/10); retrying in 15s" >&2
      sleep 15
    done
    echo "error: NetBird join failed after 10 attempts" >&2
    exit 1
  '';
in
{
  options.portablevps.network.netbird = {
    interface = lib.mkOption {
      type = lib.types.str;
      default = config.portablevps.cloud.netbirdInterface or "wt0";
      description = "Expected NetBird WireGuard interface.";
    };

    managementUrl = lib.mkOption {
      type = lib.types.str;
      default = "https://api.netbird.io:443";
      description = "NetBird management service URL.";
    };

    adminUrl = lib.mkOption {
      type = lib.types.str;
      default = "https://app.netbird.io:443";
      description = "NetBird admin panel URL.";
    };

    setupKeySecret = lib.mkOption {
      type = lib.types.str;
      default = "netbird/setup-key";
      description = "sops secret key holding the NetBird setup key.";
    };
  };

  config = lib.mkIf active {
    portablevps.network.interface = lib.mkDefault cfg.interface;
    portablevps.network.joinUnit = "netbird-join.service";

    environment.systemPackages = [ netbirdPackage ];

    sops.secrets.${cfg.setupKeySecret} = { };

    systemd.tmpfiles.rules = [
      "d /var/lib/netbird 0700 root root -"
      "d /var/log/netbird 0750 root root -"
    ];

    systemd.services.netbird = {
      description = "NetBird daemon";
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
      description = "Join NetBird network";
      wantedBy = [ "multi-user.target" ];
      after = [ "netbird.service" ];
      requires = [ "netbird.service" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        # No Restart= here: systemd ignores it for Type=oneshot. The join script
        # retries internally instead.
        ExecStart = netbirdJoin;
      };
    };
  };
}

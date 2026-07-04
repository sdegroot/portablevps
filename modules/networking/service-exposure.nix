# Translates service port exposure declarations into firewall rules and Netbird proxies.
{ config, lib, pkgs, ... }:

let
  cfg = config.portablevps.serviceExposure;
  netbirdEnabled = config.portablevps.netbird.enable or false;
  netbirdInterface = config.portablevps.netbird.interface or (config.portablevps.cloud.netbirdInterface or "wt0");

  tcpTargetType = lib.types.submodule {
    options = {
      listenPort = lib.mkOption {
        type = lib.types.port;
        description = "TCP port exposed on the selected interface.";
      };

      targetHost = lib.mkOption {
        type = lib.types.str;
        default = "127.0.0.1";
        description = "Local target host that receives proxied traffic.";
      };

      targetPort = lib.mkOption {
        type = lib.types.port;
        description = "Local target port that receives proxied traffic.";
      };

      afterServices = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = "Services this exposure starts after.";
      };

      wantsServices = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = "Services this exposure wants.";
      };
    };
  };

  serviceExposureType = lib.types.submodule {
    options = {
      public.tcp = lib.mkOption {
        type = lib.types.listOf lib.types.port;
        default = [ ];
        description = "TCP ports exposed on the public host firewall.";
      };

      netbird.tcp = lib.mkOption {
        type = lib.types.listOf tcpTargetType;
        default = [ ];
        description = "TCP ports exposed on Netbird through local proxies.";
      };
    };
  };

  services = cfg.services;
  publicTcpPorts = lib.unique (
    lib.flatten (lib.mapAttrsToList (_: service: service.public.tcp) services)
  );
  netbirdTcpExposures = lib.flatten (
    lib.mapAttrsToList
      (serviceName: service:
        map (exposure: { inherit serviceName exposure; }) service.netbird.tcp)
      services
  );
  netbirdTcpPorts = lib.unique (map (entry: entry.exposure.listenPort) netbirdTcpExposures);

  mkNetbirdProxy = entry:
    let
      serviceName = "${entry.serviceName}-netbird-${toString entry.exposure.listenPort}-proxy";
      proxyScript = pkgs.writeShellScript serviceName ''
        set -eu

        for _ in $(seq 1 120); do
          address="$(${pkgs.iproute2}/bin/ip -4 -o addr show dev ${lib.escapeShellArg netbirdInterface} 2>/dev/null | ${pkgs.gawk}/bin/awk '{ split($4, a, "/"); print a[1]; exit }')"
          if [ -n "$address" ]; then
            exec ${pkgs.socat}/bin/socat \
              "TCP-LISTEN:${toString entry.exposure.listenPort},bind=$address,fork,reuseaddr" \
              "TCP:${lib.escapeShellArg entry.exposure.targetHost}:${toString entry.exposure.targetPort}"
          fi
          sleep 1
        done

        echo "error: Netbird interface ${netbirdInterface} has no IPv4 address" >&2
        exit 1
      '';
    in
    lib.nameValuePair serviceName {
      description = "Expose ${entry.serviceName} TCP ${toString entry.exposure.listenPort} on Netbird";
      wantedBy = lib.optional (!config.portablevps.restoreMode) "apps.target";
      partOf = [ "apps.target" ];
      after = [
        "netbird-join.service"
      ] ++ entry.exposure.afterServices;
      wants = [
        "netbird-join.service"
      ] ++ entry.exposure.wantsServices;
      serviceConfig = {
        Type = "simple";
        Restart = "always";
        RestartSec = "5s";
        ExecStart = proxyScript;
      };
    };
in
{
  options.portablevps.serviceExposure.services = lib.mkOption {
    type = lib.types.attrsOf serviceExposureType;
    default = { };
    description = "Network exposure declarations for application services.";
  };

  config = lib.mkMerge [
    (lib.mkIf (publicTcpPorts != [ ]) {
      networking.firewall.allowedTCPPorts = publicTcpPorts;
    })
    (lib.mkIf (netbirdEnabled && netbirdTcpExposures != [ ]) {
      networking.firewall.interfaces.${netbirdInterface}.allowedTCPPorts = netbirdTcpPorts;
      systemd.services = lib.listToAttrs (map mkNetbirdProxy netbirdTcpExposures);
    })
  ];
}

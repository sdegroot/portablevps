# Mesh VPN contract. Selects a backend (NetBird or Tailscale) and exposes a
# small, backend-neutral interface that the rest of portablevps consumes:
# whether the VPN is enabled, its peer name, its network interface, and the
# systemd unit that establishes membership. Break-glass SSH, private service
# exposure, and the proxy firewall all read these instead of a specific VPN.
{ config, lib, ... }:

let
  cfg = config.portablevps.network;
in
{
  imports = [
    ./netbird.nix
    ./tailscale.nix
  ];

  options.portablevps.network = {
    enable = lib.mkEnableOption "mesh VPN connectivity";

    backend = lib.mkOption {
      type = lib.types.enum [ "netbird" "tailscale" ];
      default = "netbird";
      description = "Mesh VPN implementation to run on this host.";
    };

    name = lib.mkOption {
      type = lib.types.str;
      default = config.networking.hostName;
      description = "Peer name this host registers under on the mesh.";
    };

    interface = lib.mkOption {
      type = lib.types.str;
      default = "";
      description = "Mesh VPN network interface. Set by the active backend; may be overridden.";
    };

    joinUnit = lib.mkOption {
      type = lib.types.str;
      default = "";
      description = "systemd unit that establishes mesh membership. Set by the active backend.";
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [
      {
        assertion = cfg.interface != "" && cfg.joinUnit != "";
        message = "portablevps.network is enabled but backend ${cfg.backend} did not set interface/joinUnit.";
      }
    ];
  };
}

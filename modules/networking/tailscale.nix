# Tailscale backend for portablevps.network. Brings the host onto a Tailscale
# (or Headscale) tailnet using a sops-managed auth key, and reports the
# contract values (interface, joinUnit) the rest of portablevps consumes.
{ config, lib, ... }:

let
  net = config.portablevps.network;
  cfg = net.tailscale;
  active = net.enable && net.backend == "tailscale";
in
{
  options.portablevps.network.tailscale = {
    interface = lib.mkOption {
      type = lib.types.str;
      default = "tailscale0";
      description = "Tailscale network interface.";
    };

    loginServer = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "https://headscale.example.com";
      description = "Control server URL. Null uses the default Tailscale coordination server.";
    };

    authKeySecret = lib.mkOption {
      type = lib.types.str;
      default = "tailscale/auth-key";
      description = "sops secret key holding the Tailscale auth key.";
    };

    extraUpFlags = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      example = [ "--ssh" "--accept-routes" ];
      description = "Extra flags passed to tailscale up.";
    };
  };

  config = lib.mkIf active {
    portablevps.network.interface = lib.mkDefault cfg.interface;
    # services.tailscale.authKeyFile generates a tailscaled-autoconnect oneshot
    # that runs `tailscale up`; that is what establishes membership.
    portablevps.network.joinUnit = "tailscaled-autoconnect.service";

    sops.secrets.${cfg.authKeySecret} = { };

    services.tailscale = {
      enable = true;
      authKeyFile = config.sops.secrets.${cfg.authKeySecret}.path;
      extraUpFlags =
        cfg.extraUpFlags
        ++ [ "--hostname" net.name ]
        ++ lib.optionals (cfg.loginServer != null) [ "--login-server" cfg.loginServer ];
    };
  };
}

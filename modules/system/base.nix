# Defines baseline OS users, SSH, firewall, sudo, and common tools.
{ config, lib, pkgs, ... }:

let
  cfg = config.portablevps.base;
in
{
  options.portablevps.base = {
    # Secure by default: the failure mode for a host that forgets its platform
    # is "unreachable", not "internet-exposed with a known password". Local /
    # prototype hosts opt into the conveniences (see qemu-test-access.nix).
    passwordAuthentication = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Whether OpenSSH accepts password authentication (default: key-only).";
    };

    adminInitialPassword = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "Initial password for the admin user (default: null = key-only access).";
    };

    adminAuthorizedKeyFiles = lib.mkOption {
      type = lib.types.listOf lib.types.path;
      default = [ ];
      description = "Public key files authorized for the admin user.";
    };

    allowedTCPPorts = lib.mkOption {
      type = lib.types.listOf lib.types.port;
      default = [ ];
      description = "TCP ports opened in the host firewall (default: none — open only what a host needs).";
    };
  };

  config = {
    # nixos-rebuild switch writes /etc/hostname but does not re-apply the running
    # kernel hostname (systemd sets it from /etc/hostname only at boot), so a
    # repurposed/renamed box otherwise keeps a stale live hostname until reboot —
    # which leaks into telemetry (host.name), logs, and prompts. Apply it on every
    # activation so the running hostname always matches the machine identity.
    system.activationScripts.applyRunningHostname =
      "${pkgs.coreutils}/bin/printf '%s' ${lib.escapeShellArg config.networking.hostName} > /proc/sys/kernel/hostname";

    # NetBird serves peer-name DNS (<peer>.epistola.int) from its embedded
    # resolver, but on Linux it needs systemd-resolved to register that split-DNS
    # domain. Without it, servers can't resolve mesh names and fall through to
    # public DNS. Enabling resolved lets NetBird wire up peer resolution.
    services.resolved = {
      enable = true;
      # Insurance: if the DHCP-provided upstream isn't picked up, resolved still
      # has working public resolvers so ACME / egress DNS never breaks.
      settings.Resolve.FallbackDNS = [ "1.1.1.1" "9.9.9.9" ];
    };

    # NixOS 26.05 changed this default to dbus-broker, but a 25.05 system does
    # not carry the new switch-inhibitor metadata. Its first live
    # `nixos-rebuild switch` therefore attempts to reload the old dbus-daemon
    # through the new broker unit, waits 90 seconds, and leaves an otherwise
    # applied deployment with exit status 4. Keep the supported classic daemon
    # explicit for the 25.05 -> 26.05 fleet upgrade. Moving to dbus-broker must
    # be a separately rehearsed boot + reboot rollout after every host is on
    # 26.05, where the inhibitor can enforce the reboot boundary.
    services.dbus.implementation = "dbus";

    services.openssh = {
      enable = true;
      openFirewall = false;
      settings = {
        PasswordAuthentication = cfg.passwordAuthentication;
        PermitRootLogin = "no";
      };
    };

    networking.firewall = {
      enable = true;
      allowedTCPPorts = cfg.allowedTCPPorts;
      # Public-IP boxes get constant internet port-scan traffic the firewall
      # already drops; logging each refused connection floods the logs (and their
      # storage) with non-actionable noise. Don't log them — the refused-packet
      # COUNT is exposed as a metric instead (drops/sec is what you actually want
      # to graph/alert on, not per-packet lines). See the monitoring app's
      # firewall-refused metric.
      logRefusedConnections = false;
    };

    security.sudo = {
      enable = true;
      wheelNeedsPassword = false;
    };

    users.users.admin = {
      isNormalUser = true;
      description = "portablevps administrator";
      extraGroups = [ "wheel" ];
      openssh.authorizedKeys.keyFiles = cfg.adminAuthorizedKeyFiles;
    } // lib.optionalAttrs (cfg.adminInitialPassword != null) {
      initialPassword = cfg.adminInitialPassword;
    } // lib.optionalAttrs (cfg.adminInitialPassword == null) {
      hashedPassword = "!";
    };

    environment.systemPackages = with pkgs; [
      curl
      git
      jq
      vim
    ];

    assertions = [
      {
        assertion = cfg.adminInitialPassword != null || cfg.adminAuthorizedKeyFiles != [ ];
        message = "The admin user has neither an initial password nor authorized keys; this host would be unreachable. Set portablevps.base.adminInitialPassword or portablevps.base.adminAuthorizedKeyFiles.";
      }
    ];
  };
}

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

    # NetBird serves peer-name DNS (<peer>.example.int) from its embedded
    # resolver, but on Linux it needs systemd-resolved to register that split-DNS
    # domain. Without it, servers can't resolve mesh names and fall through to
    # public DNS. Enabling resolved lets NetBird wire up peer resolution.
    services.resolved = {
      enable = true;
      # Insurance: if the DHCP-provided upstream isn't picked up, resolved still
      # has working public resolvers so ACME / egress DNS never breaks.
      settings.Resolve.FallbackDNS = [ "1.1.1.1" "9.9.9.9" ];
      # LAN name discovery has no use on a server, and by default resolved
      # listens for it on every interface, the public one included. The firewall
      # drops it, but nothing should be listening there in the first place.
      settings.Resolve.LLMNR = lib.mkDefault "no";
      settings.Resolve.MulticastDNS = lib.mkDefault "no";
    };

    # A server is not a router: ignore ICMP redirects, which could reroute its
    # traffic, and never send them.
    boot.kernel.sysctl = lib.mapAttrs (_: lib.mkDefault) {
      "net.ipv4.conf.all.accept_redirects" = 0;
      "net.ipv4.conf.default.accept_redirects" = 0;
      "net.ipv4.conf.all.secure_redirects" = 0;
      "net.ipv4.conf.default.secure_redirects" = 0;
      "net.ipv6.conf.all.accept_redirects" = 0;
      "net.ipv6.conf.default.accept_redirects" = 0;
      "net.ipv4.conf.all.send_redirects" = 0;
      "net.ipv4.conf.default.send_redirects" = 0;
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

    # Every deploy and every autoUpgrade run adds a system generation, and
    # nothing removed them: the website box had 192 generations and a 21 GB
    # store. Collect generations older than two weeks; the current one is always
    # kept, so a rollback window of two weeks remains. The GRUB limit keeps
    # /boot from filling with kernels for generations that still exist.
    nix.gc = {
      automatic = lib.mkDefault true;
      dates = lib.mkDefault "weekly";
      options = lib.mkDefault "--delete-older-than 14d";
    };
    boot.loader.grub.configurationLimit = lib.mkDefault 20;

    services.openssh = {
      enable = true;
      openFirewall = false;
      settings = {
        PasswordAuthentication = cfg.passwordAuthentication;
        # Keyboard-interactive is the PAM path a password login takes, so it
        # follows passwordAuthentication. On a key-only host it is just surface.
        KbdInteractiveAuthentication = cfg.passwordAuthentication;
        PermitRootLogin = "no";
        AllowUsers = [ "admin" ];
        # Admin sessions run commands; nothing in portablevps tunnels through
        # them (nixos-rebuild and nix copy use the plain session channel). Off,
        # a stolen admin key cannot turn the host into a pivot into the mesh,
        # and an agent forwarded to the host cannot be borrowed there.
        AllowTcpForwarding = "no";
        AllowAgentForwarding = false;
        AllowStreamLocalForwarding = "no";
        X11Forwarding = false;
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

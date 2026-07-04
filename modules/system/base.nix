# Defines baseline OS users, SSH, firewall, sudo, and common tools.
{ config, lib, pkgs, ... }:

let
  cfg = config.portablevps.base;
in
{
  options.portablevps.base = {
    passwordAuthentication = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Whether OpenSSH accepts password authentication.";
    };

    adminInitialPassword = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = "dev-password";
      description = "Initial password for the admin user. Set to null for key-only access.";
    };

    adminAuthorizedKeyFiles = lib.mkOption {
      type = lib.types.listOf lib.types.path;
      default = [ ];
      description = "Public key files authorized for the admin user.";
    };

    allowedTCPPorts = lib.mkOption {
      type = lib.types.listOf lib.types.port;
      default = [ 22 5432 ];
      description = "TCP ports opened in the host firewall.";
    };
  };

  config = {
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

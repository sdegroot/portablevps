# Local QEMU host for exercising the Discourse role without assigning a fleet server.
{ lib, ... }:

let
  domain = "community.example.test";
in
{
  imports = [
    ../modules/system/base.nix
    ../modules/platforms/local-zfs-data.nix
    ../modules/runtime/docker.nix
    ../modules/services/container-state.nix
    ../modules/system/backups.nix
    ../modules/networking/service-exposure.nix
    ../modules/networking/proxy.nix
    ../modules/system/telemetry.nix
    ../modules/system/scripts.nix
    ../modules/platforms/qemu-test-access.nix
    ../apps/discourse
  ];

  networking.hostName = "portablevps-discourse-local-vm";
  portablevps.secrets.allowPrototypeDefaults = true;
  portablevps.network.enable = lib.mkForce false;

  portablevps.apps.discourse = {
    enable = true;
    inherit domain;
    developerEmails = [ "admin@example.com" ];
  };

  boot.loader.systemd-boot.enable = true;
  boot.loader.efi.canTouchEfiVariables = false;

  fileSystems."/" = {
    device = "/dev/disk/by-label/nixos";
    fsType = "ext4";
  };

  fileSystems."/boot" = {
    device = "/dev/disk/by-label/BOOT";
    fsType = "vfat";
  };

  system.stateVersion = "25.05";
}

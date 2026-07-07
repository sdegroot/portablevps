# Local QEMU VM host profile used for disaster recovery validation.
{ ... }:

{
  imports = [
    ../modules/system/base.nix
    ../modules/platforms/local-zfs-data.nix
    ../modules/runtime/podman.nix
    ../modules/services/postgres
    ../modules/services/container-state.nix
    ../modules/system/backups.nix
    ../modules/networking/service-exposure.nix
    ../modules/networking/proxy.nix
    ../modules/system/telemetry.nix
    ../modules/system/scripts.nix
    ../modules/platforms/qemu-test-access.nix
  ];

  networking.hostName = "portablevps-local-vm";
  portablevps.secrets.allowPrototypeDefaults = true;

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

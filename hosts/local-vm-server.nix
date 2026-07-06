# Local QEMU VM host for a CONSUMER server, used by lib.mkFlake's
# `<server>-local-vm` / `<server>-local-vm-restore` configurations to boot a
# real server config (its profile + app stack + backup components) on a laptop
# for backup/restore validation.
#
# It provides the local platform (boot, filesystems, data dir, test SSH access)
# and forces the local-only posture — prototype secrets (no sops/host key) and
# no mesh — while the server's profile + module supply everything else. The
# proxy goes inert under prototype defaults (see modules/networking/proxy.nix),
# so no Traefik/ACME/mesh binding is attempted.
{ lib, ... }:

{
  imports = [
    ../modules/system/base.nix
    ../modules/platforms/local-zfs-data.nix
    ../modules/networking/network.nix
    ../modules/system/monitoring.nix
    ../modules/system/scripts.nix
    ../modules/platforms/qemu-test-access.nix
  ];

  portablevps.secrets.allowPrototypeDefaults = lib.mkForce true;
  portablevps.network.enable = lib.mkForce false;

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

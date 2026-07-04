# Defines optional local QEMU ZFS data mounts used by the prototype environment.
{ pkgs, ... }:

{
  boot.supportedFilesystems = [ "zfs" ];
  boot.zfs.forceImportRoot = false;

  networking.hostId = "e915701a";

  environment.systemPackages = with pkgs; [
    zfs
  ];

  services.zfs = {
    autoScrub.enable = true;
    trim.enable = true;
  };

  systemd.tmpfiles.rules = [
    "d /data 0755 root root -"
    "d /data/postgres 0755 root root -"
    "d /data/container-state 0755 root root -"
  ];
}

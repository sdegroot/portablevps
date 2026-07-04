# Defines the single-disk GPT/EFI/ext4 layout used by cloud VPS installs.
{ config, lib, ... }:

{
  options.portablevps.cloud.diskDevice = lib.mkOption {
    type = lib.types.str;
    default = "/dev/sda";
    description = "Primary disk device that nixos-anywhere/disko will partition.";
  };

  config.disko.devices = {
    disk.main = {
      type = "disk";
      device = config.portablevps.cloud.diskDevice;
      content = {
        type = "gpt";
        partitions = {
          biosBoot = {
            size = "1M";
            type = "EF02";
          };

          ESP = {
            size = "512M";
            type = "EF00";
            content = {
              type = "filesystem";
              format = "vfat";
              mountpoint = "/boot";
              mountOptions = [ "umask=0077" ];
            };
          };

          root = {
            size = "100%";
            content = {
              type = "filesystem";
              format = "ext4";
              mountpoint = "/";
            };
          };
        };
      };
    };
  };
}

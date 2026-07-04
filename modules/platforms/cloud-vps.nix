# Applies cloud VPS hardening, boot, storage, diagnostics, and provider disk settings.
{ config, lib, ... }:

{
  options.portablevps.cloud = {
    providerName = lib.mkOption {
      type = lib.types.str;
      default = "cloud";
      description = "Cloud provider identifier from providers/<provider>/provider.json.";
    };

    netbirdInterface = lib.mkOption {
      type = lib.types.str;
      default = "wt0";
      description = "Netbird interface used for private service exposure.";
    };
  };

  imports = [
    ./cloud-disko.nix
  ];

  config = {
    portablevps.base = {
      passwordAuthentication = false;
      adminInitialPassword = null;
      allowedTCPPorts = [ ];
    };

    services.fail2ban = {
      enable = true;
      maxretry = 5;
      bantime = "1h";
      bantime-increment = {
        enable = true;
        maxtime = "24h";
        rndtime = "10m";
      };
      jails.sshd.settings = {
        findtime = "10m";
      };
    };

    boot.loader.grub = {
      enable = true;
      efiSupport = true;
      efiInstallAsRemovable = true;
      device = config.portablevps.cloud.diskDevice;
      extraConfig = ''
        serial --unit=0 --speed=115200 --word=8 --parity=no --stop=1
        terminal_input console serial
        terminal_output console serial
      '';
    };
    boot.loader.efi.canTouchEfiVariables = false;
    boot.initrd.availableKernelModules = [
      "ata_piix"
      "ehci_pci"
      "nvme"
      "sd_mod"
      "sr_mod"
      "uhci_hcd"
      "virtio_blk"
      "virtio_pci"
      "virtio_scsi"
      "xhci_pci"
    ];
    boot.kernelModules = [
      "virtio_net"
    ];
    boot.kernelParams = [
      "console=tty0"
      "console=ttyS0,115200n8"
    ];
    systemd.services."serial-getty@ttyS0".wantedBy = [ "getty.target" ];
    services.journald.extraConfig = ''
      Storage=persistent
    '';

    # Provider VPS profiles use ordinary single-disk storage. Restic remains the
    # recovery mechanism; ZFS is only part of the local VM prototype.
    systemd.tmpfiles.rules = [
      "d /data 0755 root root -"
      "d /data/postgres 0755 root root -"
      "d /data/container-state 0755 root root -"
    ];

    assertions = [
      {
        assertion = config.portablevps.base.adminAuthorizedKeyFiles != [ ];
        message = "Cloud VPS hosts use key-only SSH; set portablevps.base.adminAuthorizedKeyFiles (e.g. from your provisioned cloud admin key).";
      }
    ];
  };
}

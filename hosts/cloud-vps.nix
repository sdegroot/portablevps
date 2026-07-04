# Shared cloud VPS platform profile parameterized by provider defaults.
{ providerName ? "cloud", providerConfig ? { }, ... }:

let
  netbirdInterface = providerConfig.netbirdInterface or "wt0";
in
{
  imports = [
    ../modules/system/base.nix
    ../modules/platforms/cloud-vps.nix
    ../modules/networking/break-glass-ssh.nix
    ../modules/networking/netbird.nix
    ../modules/system/monitoring.nix
    ../modules/system/scripts.nix
  ];

  portablevps.cloud = {
    providerName = providerName;
    netbirdInterface = netbirdInterface;
  };
  portablevps.netbird = {
    enable = true;
    interface = netbirdInterface;
  };
  portablevps.breakGlassSsh = {
    enable = true;
    netbirdInterface = netbirdInterface;
  };
  portablevps.secrets.allowPrototypeDefaults = false;

  system.stateVersion = "25.05";
}

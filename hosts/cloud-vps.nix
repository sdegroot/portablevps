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
    ../modules/networking/network.nix
    ../modules/system/telemetry.nix
    ../modules/system/scripts.nix
  ];

  portablevps.cloud = {
    providerName = providerName;
    netbirdInterface = netbirdInterface;
  };
  portablevps.network = {
    enable = true;
    backend = "netbird";
  };
  portablevps.breakGlassSsh.enable = true;
  portablevps.secrets.allowPrototypeDefaults = false;

  system.stateVersion = "25.05";
}

# Enables Podman as the container runtime for service Quadlets.
{ pkgs, ... }:

{
  virtualisation.podman = {
    enable = true;
    dockerCompat = false;
    autoPrune = {
      enable = true;
      dates = "weekly";
    };
  };

  # Use netavark's nftables firewall driver instead of the (default) iptables one.
  # The iptables driver hand-creates per-network `NETAVARK-<hash>` chains and does
  # not reliably clean them up on container teardown; restarting a single container
  # on a custom bridge network then fails with "iptables: Chain already exists" as
  # netavark's on-disk state diverges from the live chains (which previously forced
  # a full reboot to recover). The nftables driver manages a single self-contained
  # `netavark` nft table applied atomically, so restarts are idempotent and never
  # strand chains. This is also podman's own direction for the default driver.
  virtualisation.containers.containersConf.settings.network.firewall_driver = "nftables";

  environment.systemPackages = with pkgs; [
    podman
  ];
}

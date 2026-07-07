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
    # Put `nft` on the PATH podman hands to netavark. nixpkgs' netavark has no
    # firewall tools in its own closure — it finds them via podman's wrapper PATH
    # (`binPath = makeBinPath ([ iptables ] ++ extraPackages)`), which is exactly
    # how it finds iptables today. This adds nftables the same way, WITHOUT
    # enabling host `networking.nftables.enable` — so the host firewall stays
    # iptables-nft and the iptables/ipset break-glass-ssh module is untouched.
    extraPackages = [ pkgs.nftables ];
  };

  # Use netavark's nftables firewall driver instead of the default iptables one.
  # The iptables driver hand-creates per-network `NETAVARK-<hash>` chains and does
  # not reliably clean them up on container teardown, so restarting a single
  # container on a custom bridge network fails with "iptables: Chain already exists"
  # once netavark's on-disk state diverges from the live chains (previously only
  # recoverable by cycling the whole network or rebooting). The nftables driver
  # manages a single self-contained `netavark` nft table applied atomically, making
  # container restarts idempotent. It creates its own table and coexists with the
  # host's iptables-nft firewall (both on the nf_tables kernel backend). Switching
  # requires a one-time reboot (or full network cycle) per host to clear the stale
  # iptables chains left by the old driver.
  virtualisation.containers.containersConf.settings.network.firewall_driver = "nftables";
  # NB: `virtualisation.podman.enable` already installs the (extraPackages-wrapped)
  # podman into systemPackages. Do NOT add `pkgs.podman` here — a second, unwrapped
  # podman would shadow it on PATH and break CLI `podman run --network …` (netavark
  # would not find `nft`).
}

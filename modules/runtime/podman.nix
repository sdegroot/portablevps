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

  # NOTE ON RESTARTS: netavark uses its default `iptables` firewall driver, which
  # hand-creates per-network `NETAVARK-<hash>` chains and does not reliably clean
  # them up on container teardown. Restarting a single container on a custom bridge
  # network can then fail with "iptables: Chain already exists" as netavark's
  # on-disk state diverges from the live chains, recoverable only by cycling the
  # whole network (stop all containers + the *-network unit, then start) or a reboot.
  # The `nftables` driver would make restarts idempotent, but nixpkgs' netavark is
  # built referencing iptables only (no `nft` in its closure), and enabling it via
  # `networking.nftables.enable` conflicts with the iptables/ipset break-glass-ssh
  # module. Proper fix (deferred): a netavark override that bundles nftables so the
  # driver can be switched without touching the host firewall backend. Until then,
  # prefer a full-stack recycle over single-container restarts (see apps/monitoring).

  environment.systemPackages = with pkgs; [
    podman
  ];
}

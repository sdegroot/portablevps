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
  #
  # See docs/adr/0002-netavark-nftables-firewall-driver.md for the full rationale
  # and tradeoffs. Two exit ramps if this becomes a liability: (1) if a nixpkgs
  # bump breaks the `nft`-on-PATH assumption above, switch to an explicit netavark
  # override; (2) if break-glass-ssh ever moves off iptables/ipset, unify the host
  # on `networking.nftables.enable` and delete this special-casing.
  virtualisation.containers.containersConf.settings.network.firewall_driver = "nftables";
  # NB: `virtualisation.podman.enable` already installs the (extraPackages-wrapped)
  # podman into systemPackages. Do NOT add `pkgs.podman` here — a second, unwrapped
  # podman would shadow it on PATH and break CLI `podman run --network …` (netavark
  # would not find `nft`).

  # Trust the podman bridges so containers can reach their gateway — in particular
  # the built-in DNS (aardvark-dns) at gateway:53. A consequence of the nftables
  # driver above: netavark's accept rules live in its own nft table, but the NixOS
  # host firewall (a separate table) default-drops the untrusted `podman*` bridge
  # interface, and in nftables a drop in any table wins — so container->gateway:53
  # is dropped and container name resolution silently fails. (Under the old iptables
  # driver netavark shared the firewall's chains, so this didn't arise.) Trusting the
  # bridges lets container->host traffic (DNS + published-port loopbacks) through.
  networking.firewall.trustedInterfaces = [ "podman+" ];

  # Restart quadlet containers whose definition changed on `nixos-rebuild switch`.
  #
  # Quadlet containers are GENERATED units: podman's systemd generator reads
  # /etc/containers/systemd/*.container at daemon-reload and synthesizes the
  # <name>.service. They live outside NixOS's managed-unit set, so switch updates
  # the .container file but never restarts the running container — a bumped image
  # tag or changed env silently has no effect until a manual restart or reboot.
  #
  # This activation step closes that gap: it hashes each rendered .container and,
  # when one changes, `try-restart`s that unit (only the changed ones bounce).
  # `try-restart` never STARTS a container, so restore-mode / apps.target gating
  # and first-boot ordering are respected. The FIRST run on any box only seeds the
  # hashes (no restarts), so rolling this out doesn't bounce already-running
  # containers; only genuine subsequent changes trigger a restart.
  #
  # TODO: consider migrating the quadlet apps to the `quadlet-nix` module, which
  # manages them as tracked NixOS units (restart-on-change built in), retiring
  # this step. Deferred for now: young/unversioned dependency, ~single
  # maintainer, and restart-on-change isn't confirmed in its docs — worth an ADR
  # + a spike before adopting it under the whole fleet's container runtime.
  system.activationScripts.restartChangedQuadlets = {
    deps = [ "etc" ];
    text = ''
      stateDir=/var/lib/portablevps/quadlet-hashes
      # First run (state dir absent): seed hashes only, never restart.
      seed=0
      [ -d "$stateDir" ] || seed=1
      ${pkgs.coreutils}/bin/mkdir -p "$stateDir"
      changed=""
      for unit in /etc/containers/systemd/*.container; do
        [ -e "$unit" ] || continue
        name="$(${pkgs.coreutils}/bin/basename "$unit" .container)"
        new="$(${pkgs.coreutils}/bin/sha256sum "$unit" | ${pkgs.coreutils}/bin/cut -d' ' -f1)"
        hashFile="$stateDir/$name"
        old=""
        [ -f "$hashFile" ] && old="$(${pkgs.coreutils}/bin/cat "$hashFile")"
        if [ "$new" != "$old" ]; then
          ${pkgs.coreutils}/bin/printf '%s' "$new" > "$hashFile"
          [ "$seed" = "0" ] && changed="$changed $name.service"
        fi
      done
      if [ -n "$changed" ]; then
        # Regenerate the .service units from the new .container files, then bounce
        # only the changed units that are currently running.
        ${pkgs.systemd}/bin/systemctl daemon-reload
        for svc in $changed; do
          echo "portablevps: quadlet $svc definition changed -> try-restart"
          ${pkgs.systemd}/bin/systemctl try-restart "$svc" || true
        done
      fi
    '';
  };
}

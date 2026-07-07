# ADR 0002: netavark nftables firewall driver for podman networks

- Status: Accepted
- Date: 2026-07-07
- Deciders: portablevps maintainers

## Context

Every portablevps app runs as podman Quadlet containers on a custom bridge
network (e.g. the monitoring stack on `10.89.0.0/24`). podman's default network
backend, **netavark**, uses its **iptables** firewall driver: for each network it
hand-creates `NETAVARK-<hash>` chains in the iptables `nat`/`filter` tables and is
responsible for tearing them down when the last container leaves.

That teardown is not reliable. When a single container on a custom network is
restarted (`podman rm -f` + `podman run --replace`, i.e. exactly what
`systemctl restart <container>` does), netavark can leave a chain behind. Its
on-disk firewall state then disagrees with the live iptables chains, and the
*next* container create on that network fails with:

```
Error: netavark: code: 1, msg: iptables: Chain already exists.
```

This is not theoretical — it took the monitoring stack down in production. The
trigger was mundane: after the operator filled in the Brevo SMTP secret, we
restarted Alertmanager to load it, and Alertmanager crash-looped on the chain
conflict. Because the chains are shared per-network, the corruption blocks *any*
container create on that network, so the only recovery was a full network cycle
(stop every container + the `*-network` unit) or a reboot. Both mean downtime and
neither is a fix — the hazard returns on the next single-container restart.

Two facts shape the options:

- **nixpkgs' netavark carries no firewall tools in its own closure.** It is a
  plain `buildRustPackage` that finds `iptables`/`nft` on `$PATH` at runtime. The
  `iptables` it uses today comes from **podman's** wrapper: podman is wrapped with
  `binPath = makeBinPath ([ iptables ] ++ extraPackages)` and netavark inherits
  that `PATH`. So `nft` can be supplied the same way netavark already gets
  `iptables` — via `virtualisation.podman.extraPackages` — with no override.
- **The host firewall is iptables-nft, and `break-glass-ssh` uses raw
  iptables + ipset** (`-m set --match-set` geo-allow chains). Switching the whole
  host to native nftables (`networking.nftables.enable`) would put that
  security-critical, ipset-based module on a fragile mixed-rule footing.

## Decision drivers

- Make single-container restarts idempotent — no more reboot-to-recover, no
  operational rule of "never restart one container."
- Do not destabilise the security-critical `break-glass-ssh` path.
- Prefer supported NixOS options over derivation overrides we own and maintain.
- Keep the change small, reversible, and consistent across the fleet.

## Options considered

1. **Keep the iptables driver; adopt operational discipline** — never restart a
   single container; always cycle the whole stack (all containers + the network
   unit) instead.
   - `+` no new technology or version-coupling risk; the driver is the
     best-battle-tested one.
   - `−` fragile: one accidental `systemctl restart <container>`, one sops-driven
     unit restart, or one operator reflex re-triggers the outage. Doesn't scale
     with the number of apps/boxes and pushes a footgun onto every future
     maintainer. Not a fix, a caution.

2. **Enable host-wide native nftables (`networking.nftables.enable`)**, which
   also provides `nft` to netavark, then switch the driver.
   - `+` single firewall backend across host and containers; the cleanest
     long-term end state; `nft` is present for netavark as a side effect.
   - `−` breaks / endangers `break-glass-ssh`'s iptables + ipset rules, which
     would have to be ported to nftables first — real work on the one path we
     least want to destabilise. Too much blast radius for the payoff right now.

3. **Override the netavark (or podman) derivation to bundle `nft`.**
   - `+` explicit and robust — netavark's firewall tool becomes a property of the
     package, not of an inherited `PATH`.
   - `−` a derivation we own and must keep building across nixpkgs bumps;
     re-discovers packaging work that the module option already does for us.

4. **Switch netavark to the nftables driver and supply `nft` via
   `virtualisation.podman.extraPackages`; leave the host firewall on
   iptables-nft.** **(Recommended.)**
   - `+` smallest change that fixes the bug; netavark manages one self-contained
     `inet netavark` nft table applied atomically, so restarts are idempotent; the
     host firewall and `break-glass-ssh` are untouched; uses only supported module
     options; revert is a 2-minute commit + reboot; aligns with podman's own
     direction (nftables is becoming the upstream default).
   - `−` the host now runs **two firewall toolchains** (iptables-nft for the host
     + native nft for containers), and it relies on the **implicit** contract that
     podman keeps handing netavark its `PATH`.

Options 2 and 4 both end on nftables for containers; the real fork is **how much
of the host to move now**. Option 4 moves only the container plane and explicitly
leaves the host — and `break-glass` — alone.

## Decision

Adopt **option 4**, fleet-wide in `modules/runtime/podman.nix`:

```nix
virtualisation.podman.extraPackages = [ pkgs.nftables ];                 # nft on netavark's PATH
virtualisation.containers.containersConf.settings.network.firewall_driver = "nftables";
```

We also removed an explicit `environment.systemPackages = [ pkgs.podman ]` from
that module: `virtualisation.podman.enable` already installs the
`extraPackages`-wrapped podman, and the redundant plain package was shadowing it
on `PATH`, so CLI `podman run --network …` used an `nft`-less podman and failed
under the new driver. The CLI and Quadlet podman are now the same wrapped binary.

Adopting the driver on an existing host needs a **one-time reboot** (or full
network cycle) to clear the stale iptables `NETAVARK-*` chains left by the old
driver. Verified on the monitoring server: `table inet netavark` is created with
the expected masquerade/forward rules, coexists with the host `ip filter`/`mangle`
and `ip netbird` tables, 0 iptables `NETAVARK` chains remain, and restarting a
single container (Alertmanager) now succeeds with no chain conflict.

This rests on one explicit assumption: **netavark keeps discovering its firewall
tool via podman's wrapper `PATH`.** If a future nixpkgs/podman/netavark change
breaks that, the fallback is option 3 (an explicit override). If we ever port
`break-glass-ssh` off iptables/ipset, revisit toward option 2 and delete this
special-casing.

## Consequences

Positive:

- Single-container restarts are idempotent; no reboot-to-recover, no
  "don't restart one container" operational rule.
- `break-glass-ssh` and the host firewall are unchanged — the security-critical
  path was not touched.
- Achieved with supported module options only; no derivation we maintain.
- Reversible in ~2 minutes (revert commit + reboot).

Negative / risks:

- **Two firewall toolchains on one host.** Full firewall state now spans both
  `iptables -L` (host + break-glass) and `nft list ruleset` (netavark);
  debugging must consult both. Native nft and iptables-nft tables share the
  nf_tables backend and today occupy separate tables with no conflict, but a
  future rule with an overlapping hook/priority is a latent interaction surface.
- **Implicit mechanism.** `nft` reaches netavark through PATH inheritance from
  podman's wrapper, not an explicit "use this nft" setting; a change to how
  netavark is invoked could silently drop it and re-break the stack.
- **Off the NixOS default** (iptables driver), so we are more likely to hit an
  undocumented edge after a version bump, with less community precedent.
- **Newer driver, different bug surface.** We trade a well-understood bug
  (iptables chain leak, reboot-recoverable) for a better-designed but
  less-battle-tested driver.
- **Fleet-wide blast radius + per-host migration reboot.** A regression breaks
  every box's container networking at once, and each host needs a reboot to adopt
  the driver.

## Follow-up

- Migrate the remaining fleet host(s) — the authentik box still runs the iptables
  driver and adopts nftables on its next `cloud:deploy` + reboot.
- Add a short in-module pointer to the two exit ramps (override on PATH breakage;
  `networking.nftables.enable` if/when break-glass moves off iptables).
- If `break-glass-ssh` is ever ported to nftables, reconsider option 2 (unify the
  host on native nftables) and remove the driver special-casing.

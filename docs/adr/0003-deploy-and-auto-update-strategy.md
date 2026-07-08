# ADR 0003: deploy path and pull-based auto-update

- Status: Accepted
- Date: 2026-07-08
- Deciders: portablevps maintainers

## Context

We want the Epistola website (a stateless Astro app shipped as a private ghcr
image) deployed "the Nix way" — the image **pinned in git** — and to **auto-update**
when a new release is published, with a safety net if a deploy goes bad.

As a trial we adopted **deploy-rs** (magic/auto rollback) on the website box. Testing
it surfaced a disqualifying constraint: deploy-rs rolls back by executing
`{profile}/deploy-rs-activate`, a helper script that **only exists in generations
deployed by deploy-rs**. Our operator deploys use `cloud:deploy` (plain
`nixos-rebuild switch`), which does not produce that script. So when a deploy-rs
rollback lands on a `cloud:deploy` generation, it fails with
`No such file or directory` — confirmed on the box (`/nix/var/nix/profiles/system/
deploy-rs-activate` absent; the generation only carries the stock `activate`/
`dry-activate`). Making rollback reliable would require every deploy on that box to
go through deploy-rs (**deploy-rs-exclusive per box**), and mixing the two tools
**silently voids the safety net** — the worst kind of failure.

Separately, deploy-rs does not replace `cloud.py` (lifecycle create/rescue/install,
guarded restore, `netbird-dns-sync`, preflight), so keeping it means running two
tools; and its magic rollback largely overlaps what break-glass SSH + `nixos-rebuild
--rollback` already provide manually on this small, reinstallable fleet.

## Decision drivers

- One source of truth (git) and reproducibility (Nix generations).
- A rollback that genuinely works, not a false safety net.
- Minimize secret blast radius — avoid putting the operator SSH key / age key in CI.
- One deploy tool and one mental model.
- The website box is stateless/disposable and recoverable (break-glass, reinstall),
  so the stakes of a bad deploy are low.

## Options considered

1. **deploy-rs everywhere, deploy-rs-exclusive per box.** Real auto-rollback, but the
   consistency tax (mixing silently voids it), a second tool beside `cloud.py`, darwin
   quirks (`NIX_CONFIG`/`--skip-checks`), and false rollbacks when a legitimate switch
   bounces netbird/resolved/firewall.
2. **Drop deploy-rs; podman auto-update (floating tag + healthcheck rollback).** Native
   self-heal, but the app image then lives **outside** Nix — `nixos-rebuild --rollback`
   cannot revert the app, leaving two disjoint rollback worlds, and a generation no
   longer fully describes what runs (not reproducible).
3. **Drop deploy-rs; keep the app git-pinned; auto-update via pull (`system.autoUpgrade`).**
   Everything stays declarative and git-pinned, one **unified Nix rollback** covers infra
   *and* the app pin, and — unlike a CI push — **no operator key or age key in CI** (the
   box needs only read access to the repo). Costs: the opted-in box tracks the committed
   flake (GitOps/canary blast radius) and there is no automatic *runtime* rollback.

## Decision

- **`cloud:deploy` stays the operator deploy path**, unchanged (retains all of
  `cloud.py`'s lifecycle/restore/dns-sync/preflight).
- **Drop deploy-rs** (removed from `epistola/flake.nix`).
- The **app image stays git-pinned** (immutable `sha-<sha>` tag) in the server def, so it
  is part of the declarative system and unified Nix rollback covers it.
- **Auto-update via pull:** the reusable `portablevps.autoUpgrade` module wraps the
  built-in `system.autoUpgrade`; a box rebuilds **itself** from the committed flake on a
  timer. Opt-in per box (website first), tracking `main`, fetching the private repo with a
  **read-only fine-grained PAT** (`Contents: Read-only`, in the box's own sops, passed to
  nix as an `access-tokens` entry). A token — not an SSH deploy key — because deploy keys
  are un-expiring, per-repo SSH credentials outside org token governance and are disabled at
  the org level; a fine-grained token is centrally visible, revocable, and expiring.
- **Failure visibility:** a successful self-upgrade stamps
  `portablevps_autoupgrade_last_success_timestamp_seconds` over OTLP; the gateway's
  `AutoUpgradeStale` rule alerts when it goes stale, so a silently-failing upgrade (fetch/
  build failures never switch) is noticed instead of the box quietly ceasing to update.
- **Companion (website repo):** on release, CI bumps the pinned image in the infra repo;
  the box applies it on the next tick and the quadlet restart-on-change step
  (`modules/runtime/podman.nix`) rolls the container.

## Consequences

- **Unified rollback:** `nixos-rebuild --rollback` / the boot menu reverts infra **and**
  the app pin together; any generation is reproducible from its committed `flake.lock`.
- **GitOps / canary blast radius:** an opted-in box auto-applies any commit that changes
  its config (including shared `portablevps` modules) — the website box is effectively a
  fleet canary. A gate is a one-line change to a `production` branch/tag if this is too
  eager.
- **No automatic runtime rollback (accepted):** a builds-fine-but-breaks change stays live
  until a `git revert` (+ next tick) or a manual `cloud:deploy`. A failed **build** never
  switches (safe). The image `HEALTHCHECK` + monitoring alerts surface runtime breakage.
- **Don't `cloud:deploy` *uncommitted* changes to an autoUpgrade box** — the next tick
  reverts them to the committed state (git is the source of truth).
- **New dependency surface:** the infra repo must be hosted on a private remote, and each
  opted-in box holds a read-only, repo-scoped fine-grained PAT in sops. The token **expires**
  (fine-grained max ~1 year) so it needs rotation — the trade for the governance the org
  wants (deploy keys, which never expire, are disabled there).
- **Retry / self-heal:** the nixos-upgrade timer keeps retrying every tick with no backoff;
  a failed build is safe (no switch) and self-corrects once a good commit lands, and a bad
  commit that still lets the box fetch is healed by committing a revert (the box converges on
  the next tick). Only a change that severs the box's own fetch path needs break-glass.
- deploy-rs's operational quirks (darwin flags, `confirmTimeout` tuning, false rollbacks)
  are gone.

## Follow-up

- Wire the website repo's CI to bump the pin on release (a GitHub App/token scoped to the
  infra repo's contents; direct commit or PR).
- Consider a gated `production` branch/tag if tracking `main` proves too eager.
- Revisit deploy-rs-exclusive-per-box only if a box appears where a connectivity-breaking
  deploy is catastrophic *and* break-glass is insufficient.
- `quadlet-nix` migration (tracked separately) would fold the restart-on-change step into
  the module system but does not change this decision.

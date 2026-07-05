# ADR 0001: Resource plane vs. machine plane — how far to lean on OpenTofu

- Status: Proposed
- Date: 2026-07-05
- Deciders: portablevps maintainers

## Context

portablevps began as `nixos-anywhere` plus a flake for building disposable
single-instance servers. The CLI has since grown an *operator plane*:

- provider VPS lifecycle (Hetzner create/rescue/delete + local JSON state under
  `.local/cloud-state`),
- NetBird group and reusable-setup-key reconciliation (`cloud:netbird-sync`),
- NetBird DNS sync (`cloud:netbird-dns-sync`),
- and a documented-but-unbuilt S3 backup bucket + object-lock (deny-delete)
  policy.

That operator-plane work is a hand-rolled, mostly single-provider, partial
re-implementation of what Terraform/OpenTofu already does: declare desired
state, let providers make the API calls, track reality in a state file, and
reconcile with plan/apply/destroy.

We have already hit the failure modes a real engine is built to prevent:

- `promote-candidate` did not update lifecycle state (state drift);
- state writes were non-atomic and lock-free (since fixed, but symptomatic of
  owning a state store by hand);
- there is no drift detection, dependency graph, or destroy;
- the Scaleway bucket + deny-delete object-lock policy — the single biggest
  backup-integrity gap from the original review — was documented but never
  built, because there was no engine to apply it.

Meanwhile the *machine plane* — the NixOS system, its configuration, secrets
rendering, disko, restic backup/restore, restore-mode — is well served by Nix +
`nixos-anywhere` and is the project's real differentiator. OpenTofu cannot and
should not build a NixOS closure.

## Decision drivers

- Stop re-implementing (and re-debugging) a resource-management engine.
- Keep Nix as the single source of truth where it already is (server defs).
- Do not destabilise the working install / backup / restore path.
- Reuse existing providers (Hetzner, Scaleway, NetBird, deSEC) instead of
  hand-writing and maintaining API clients.
- Keep the tool approachable — avoid piling on mental models for consumers.

## Options considered

1. **Stay fully bespoke** (hand-rolled Python adapters + JSON state).
   - `+` no new dependencies, one language, self-contained appliance.
   - `−` keep reinventing resource management and inheriting its bugs; every new
     resource (access policies, buckets, more providers) is more hand-written
     API + state code, exactly where it gets hardest.

2. **Adopt OpenTofu directly (HCL)** for the resource plane.
   - `+` mature engine: plan/apply/destroy, state + locking, large provider
     ecosystem.
   - `−` HCL and a state backend become a second source of truth alongside Nix;
     two mental models for consumers.

3. **Terranix** — declare OpenTofu resources *in Nix*, generate the JSON, apply;
   `nixos-anywhere` still owns the machine. **(Recommended.)**
   - `+` Nix stays the single source of truth (server defs already are); the
     OpenTofu engine/state/providers sit underneath; the machine plane is
     unchanged.
   - `−` adds Terranix + OpenTofu to the toolchain; a state backend still needs
     a home.

## Decision

Adopt an explicit **two-plane architecture**:

- **Machine plane** stays Nix + `nixos-anywhere`, unchanged: OS, configuration,
  secrets, disko, backup/restore, restore-mode.
- **Resource plane** moves onto **OpenTofu, expressed in Nix via Terranix**:
  VMs, DNS, NetBird groups/keys/policies, the backup bucket + object-lock,
  provider-side firewall.

Adopt **incrementally, not big-bang**:

1. Introduce the seam at the *next* resource-plane feature — the Scaleway backup
   bucket + deny-delete policy, or NetBird access policies — rather than
   rewriting the working Hetzner adapter and `cloud-state` now.
2. Migrate the existing Hetzner lifecycle and `netbird-sync` onto the seam only
   after it is proven on one new resource.
3. Keep the CLI as the operator entrypoint; it may call OpenTofu for resources
   and `nixos-anywhere` for the machine. `cloud:adopt` (reinstalling an existing
   hand-made host) stays partly bespoke — the install step is `nixos-anywhere`
   regardless.

## Consequences

Positive:

- Resource management gains real plan/apply/destroy, drift detection, and state.
- The unbuilt bucket + object-lock policy becomes buildable and enforced.
- Hand-rolled provider API code is deleted over time.
- We join an established pattern (Terraform/OpenTofu + `nixos-anywhere`), not a
  novel bet.

Negative / risks:

- New toolchain dependencies (OpenTofu, Terranix) and a state backend to site.
  The operator-control-plane single-point-of-failure conversation applies;
  OpenTofu's mature remote state + locking is the answer, and its state must be
  backed up alongside the age/admin keys.
- Two engines coexist during the migration window.
- A create-oriented resource model fits "provision a new VM" better than "adopt
  an already-running box"; expect `adopt` to remain a Nix-side handoff.

## Follow-up

- Prototype the seam on the Scaleway backup bucket + deny-delete object-lock
  policy: smallest scope, highest value (closes the ransomware/insider gap from
  the original review).
- Choose the state backend (local vs. remote; encrypted; part of the operator
  control-plane backup set).
- Decide whether the Hetzner adapter + `cloud-state` migrate or stay.

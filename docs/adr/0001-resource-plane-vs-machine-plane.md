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
- **How large and ambitious will the resource plane become?** This is the
  pivotal variable. A deliberately small, capped set of resource types favours
  owning a minimal engine; a growing set (access policies, buckets, more
  providers, provider firewalls, DNS) favours an existing engine that amortises
  that work across a maintained ecosystem.

## Options considered

1. **Keep the current ad-hoc state** (`.local/cloud-state` JSON grown
   organically, plus per-command reconcile loops).
   - `+` no new dependencies; already written.
   - `−` not a real engine: no drift detection, ordering, or destroy; already
     the source of the bugs above. This is the status quo, not a design.

2. **Build our own reconciliation engine, deliberately** — desired state from
   server defs, observed state from provider APIs, a diff, apply, and recorded
   state, with per-resource adapters, all in Python.
   - `+` fully ours; one language; self-contained (no HCL / state backend /
     Terranix dependency); can be tailored to a small resource set and stay
     lean.
   - `−` the hard parts of a state engine — drift detection, dependency
     ordering, partial-failure recovery, locking, importing/moving existing
     resources, and the long tail of provider-API quirks — are exactly what we
     would re-discover one bug at a time (we already have). It is undifferentiated
     work others maintain, and every provider is ours to write and keep working.
     Gets steadily worse as the resource plane grows; only defensible if that
     plane is deliberately capped small.

3. **Adopt OpenTofu directly (HCL)** for the resource plane.
   - `+` mature engine: plan/apply/destroy, state + locking, large provider
     ecosystem.
   - `−` HCL and a state backend become a second source of truth alongside Nix;
     two mental models for consumers.

4. **Terranix** — declare OpenTofu resources *in Nix*, generate the JSON, apply;
   `nixos-anywhere` still owns the machine. **(Recommended.)**
   - `+` Nix stays the single source of truth (server defs already are); the
     OpenTofu engine/state/providers sit underneath; the machine plane is
     unchanged.
   - `−` adds Terranix + OpenTofu to the toolchain; a state backend still needs
     a home.

Options 3 and 4 are the same engine with a different authoring surface (HCL vs.
Nix); the real fork is **own the engine (1/2) vs. reuse one (3/4)**, and that
fork turns on the resource-plane-size driver above.

## Decision

Adopt an explicit **two-plane architecture**:

- **Machine plane** stays Nix + `nixos-anywhere`, unchanged: OS, configuration,
  secrets, disko, backup/restore, restore-mode.
- **Resource plane** moves onto **OpenTofu, expressed in Nix via Terranix**:
  VMs, DNS, NetBird groups/keys/policies, the backup bucket + object-lock,
  provider-side firewall.

This chooses **reuse an engine (option 4)** over **own an engine (option 2)**,
and it rests on one explicit assumption: **the resource plane will keep
growing.** If instead we deliberately *cap* the resource plane to a tiny, stable
set, option 2 (a minimal home-grown reconciler) becomes the better call, and
this decision should be revisited. The bet is that access policies, the backup
bucket, more providers, and provider firewalls are all coming — which makes
owning a state engine a growing, undifferentiated maintenance burden.

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

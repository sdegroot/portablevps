# Publishability plan

Tracking doc for `feat/publishable-tool`: the work to turn portablevps from an
internal tool with one supervised consumer (epistola) into something a stranger
can adopt to manage their own servers.

Source: the 2026-07 business-viability review + the "internal tool → publishable
product" addendum. Two north-star metrics drive the work:

- **Time-to-first-server ≤ 15 min** (from `nix flake init` to a reachable box).
- **Time-to-first-proven-restore ≤ 60 min** (watch your own server die and come
  back).

Every change must keep both flakes `nix flake check`-green and the CLI unit
tests green. Nothing here runs against live infrastructure; API-touching flows
are implemented as code + tests only.

## Guardrails

- The tool must carry **no epistola-specific defaults**. `grep -ri epistola
  portablevps/modules portablevps/apps portablevps/lib` should return nothing
  but neutral examples.
- Every silent fallback becomes a loud assertion.
- New public options are typed and documented (they become generated reference
  docs).
- Breaking changes to the consumer contract are made now, before publication,
  and the legacy alias shims are pruned.

## Items

### 1. One entrypoint + doctor + wizard  (item 1)
- [ ] `portablevps` as a single flake app (`nix run …#portablevps -- <cmd>`),
      with `--help` and shell completion; Taskfile stays an internal convenience.
- [ ] `portablevps doctor` — prereq + token + key + eval + bucket + mesh checks
      with fix-it messages. (highest support-load reducer)
- [ ] `portablevps init` wizard — scaffold consumer repo AND run the whole
      secrets ceremony (age keys, `.sops.yaml`, `updatekeys`, prompt for the six
      secret values). The user never hand-edits `.sops.yaml`.

### 2. Absorb the external-account burden  (item 2)
- [ ] NetBird: fold `netbird-sync` into install so the user provides only an API
      token, never touches the console.
- [ ] Make **Tailscale the beginner default** backend; NetBird the fleet option.
- [ ] Decide + implement an explicit **no-mesh mode** (public HTTPS + hardened
      SSH) for the hobbyist tier.
- [ ] Bucket + deny-delete/object-lock + scoped keys created by the tool
      (Terranix resource plane, ADR 0001).
- [ ] First-class lego DNS providers (Cloudflare at least); generate the CNAME
      instructions from the domain plan.

### 3. Bring-your-own container as the paved path  (item 3)
- [ ] Declarative custom-app schema (image, ports, env+secret refs, data paths,
      backup component, healthcheck) → quadlet + backup registration, no module
      authoring required.

### 4. Type the contract + invest in failure messages  (item 4)
- [ ] Server definition becomes a real typed submodule (named-option errors on
      typos in `placement`/`profile`/`info`).
- [ ] Assertions: cloud host without a real `backupRepository`; missing sops
      keys; unknown profile (already throws — improve message); mesh-off proxy.
- [ ] `portablevps validate` for pre-flight eval.

### 5. Make the proof portable  (item 5)
- [ ] Port the DR harness off Apple-Silicon-only to Linux/KVM and/or the NixOS
      VM-test framework, so any user can `portablevps drill` their own config and
      CI runs it on every commit.

### 6. Release engineering + de-epistola-ize  (item 6)
- [ ] Remove epistola-specific defaults (region `nl-ams`, `sander@degroot.dev`,
      `EPIS_*` chain names, `nl`-only break-glass, `portablevps.io` placeholders).
- [ ] Prune legacy alias shims (`vpn`/`public`, `deployment` block) — one
      consumer today, so break now.
- [ ] Full EUPL-1.2 text in LICENSE; SECURITY.md; name availability; binary
      cache for substitution.
- [ ] Repo split (or subtree publish) so the `?dir=`/`path:` monorepo coupling
      (which already breaks CI) stops leaking to consumers.

### 7. Docs for a stranger's journey  (item 7)
- [ ] Landing "why this vs Coolify/Kamal/plain NixOS".
- [ ] 15-min quickstart; 60-min "kill and restore" tutorial.
- [ ] Reference docs generated from module options (`nixosOptionsDoc`).
- [ ] Graduate AGENTS.md "gotchas" into a public troubleshooting page.

## Sequencing

Foundational first (unblocks the rest, all mechanical/testable): **4 → 6
(de-epistola + LICENSE/SECURITY) → 3 → 1 (doctor) → 7 (generated options)**.
Then the larger, API-touching/interactive items: **1 (wizard) → 2 → 5**, each
behind 2–3 supervised design partners before a public tag.

Operational trust items from the business report (deny-delete applied, mesh
default-deny flipped, deSEC rotated, key escrow, dead-man's switch) are
prerequisites for *publishing*, not for this branch's code, but #2's bucket work
and a dead-man's-switch module land here.

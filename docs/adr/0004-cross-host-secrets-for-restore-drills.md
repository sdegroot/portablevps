# ADR 0004: cross-host secrets for restore drills

- Status: Accepted
- Date: 2026-09-12
- Deciders: portablevps maintainers

## Context

`test dr` (`RestoreDrill`/`Restore`) temporarily switches a *different*
physical host — a spare, never provisioned under the source's identity —
into the exact identity of a live server, to rehearse backup/restore. It does
this via `nixos-rebuild switch` (`env.switchTo`, `internal/core/service.go`),
which renders the *entire* declared config onto the spare: hostname,
`admin`'s authorized_keys, `portablevps.secrets.file`, everything.

Secrets are sops-encrypted per **Model A**: each server's
`secrets/<name>.yaml` is encrypted to *that server's own* age key only — no
shared operator/master recipient (`.sops.yaml`'s own header comment: "Access
to a server's secrets == holding that server's age key"). The spare's own
`/etc/sops/age/keys.txt` was never registered as a recipient on the *source*
identity it's being switched into, so `sops-install-secrets` fails:

```
sops-install-secrets: failed to decrypt '.../hetzner-fsn-c4r8d160-20260710c.yaml':
Error getting data key: 0 successful groups required, got 0
```

**Confirmed live** on 2026-09-12: `test dr hetzner-fsn-c4r8d160-20260710c
--restore-server hetzner-nbg1-c4r8d160-20260912c` got past seeding (a
separate, already-fixed bug) and failed here instead. `nixos-rebuild switch`
had already rendered the *rest* of the source's identity onto the spare
(hostname, `admin`'s authorized_keys) before the secrets-dependent activation
steps failed, so the spare was left mid-switch, reachable only with the
*source's* admin key, several services down. No production impact — the
spare carries no live traffic — but recovery required manually running
`nixos-rebuild switch` back to the spare's own flake output, authenticating
with the source's key.

**`service migrate` and `service restore` are unaffected — verified, not
assumed.** Both are explicitly documented
(`docs/operations-runbooks.md`, "Service Migration") to require the target to
already be its own independently provisioned identity, with its own sops
secrets already populated, *before* either command runs: "This is not
machine-identity replacement: the target keeps its own hostname, NetBird
peer, sops age key, and secrets file." Their target's `/etc/sops/age/keys.txt`
already holds a valid recipient for its *own* `secrets/<target>.yaml` from
its original `server install`/`server adopt`, entirely independent of these
commands — so this ADR's fix is scoped to `RestoreDrill` only. An earlier
draft of this ADR incorrectly claimed `Migrate` shared the gap, reasoning
from `switchTo`'s structural similarity alone without tracing what its
`Server` parameter actually means in that caller — corrected before
implementation.

## Decision drivers

- Preserve Model A's minimization intent: a server's secrets should be
  decryptable by as few standing holders as possible, for as short a time as
  possible.
- The source identity's *private key* should never need to leave the
  operator's machine — the same trust boundary that already legitimately
  holds it for `secret show`/`secret edit` today.
- `test dr` must leave the restore/spare host clean afterward, on **every**
  exit path (success or failure) — today's incident required manual recovery
  with the source's admin key, and shouldn't again.
- A crash mid-drill (killed process, defer never runs) must have a trivial,
  unambiguous recovery.
- Reuse existing tooling (sops, the CLI's existing age-key resolution,
  `.sops.yaml`'s existing rule format) over inventing new machinery.

## Options considered

1. **Ship the source's private key onto the restore host**, reusing
   `Repurpose`'s existing `/etc/sops/age/keys.txt` mechanism
   (`internal/core/repurpose.go:60-65`), with a guaranteed revert. Simple,
   reuses proven code — but puts a real production private key in plaintext
   on a second host's disk, even briefly. A spare reused across multiple
   servers' drills (exactly this fleet's pattern) accumulates disk history
   from several different production keys over time. Rejected: this is a
   real regression against Model A's own stated minimization goal, not just
   a bounded inconvenience.
2. **Permanent shared DR-recipient.** Designate one or more boxes as a
   standing additional recipient on every server's `secrets/*.yaml`. Needs no
   per-drill step, but reintroduces exactly the shared-key blast radius Model
   A exists to avoid, and needs a `secret sync-keys` against the DR
   recipient on every onboarding/rotation. Rejected.
3. **Re-encrypt the secrets file to the restore host's own already-resident
   recipient, temporarily.** Decrypt the source's tracked secrets file
   locally (the *same* trust boundary `secret show` already uses), re-encrypt
   the plaintext to the restore host's own already-registered `.sops.yaml`
   recipient (public data, no secret needed to look up), and temporarily
   overwrite the tracked `secrets/<source>.yaml` bytes so the next local
   build embeds it. The restore host's own `/etc/sops/age/keys.txt` is never
   touched — no private key ever crosses a host boundary. Reverting is a
   pure local file write; a crash mid-drill recovers with a plain
   `git checkout -- secrets/<source>.yaml`, since the swap requires the file
   to be git-clean beforehand. **Chosen.**

Option 3 was reached by re-examining Option 1's own stated cost (a real key
briefly resident on a second host) rather than accepting it as bounded and
acceptable — the same production key crossing hosts repeatedly, for routine
quarterly drills, against spares that don't get the same scrutiny as
production boxes, is a real and avoidable exposure once there's a way to
avoid it that doesn't cost meaningfully more to build.

## Decision

`RestoreDrill` re-encrypts, does not relocate, the source's secrets:

- `internal/core/dr_secrets.go`: `RepoSecrets.Swap(identity, hostServer,
  ageEnv)` decrypts `secrets/<identity>.yaml` locally (via `ageEnv`, the
  identity's own key, resolved by the CLI exactly as `secret show` already
  does), looks up `hostServer`'s existing `.sops.yaml` recipient, re-encrypts
  to it, and overwrites the tracked file. Refuses to run against a
  not-git-clean file. Returns a `restore func()` that puts the original bytes
  back; a no-op `(nil, nil)` when `identity` carries no secrets file at all
  (most idle/monitoring servers).
- `ServiceEnv` gains `Secrets CrossHostSecrets` (nil-safe, same pattern as
  `Certs`/`SyncDNS`) and `AgeEnv map[string]string`. `DrillOpts` gains
  `RestoreHostServer string` — the restore host's own server identity,
  needed to look up its recipient.
- `RestoreDrill` calls `Swap` right before its `Restore(...)` call and
  registers the revert as a `defer` — a named return (`marker string, err
  error`) lets that defer compound a revert failure into the already-being-
  returned error (`combineCleanupError`, mirroring `Migrate`'s existing
  `rollback` message-composition) without ever silently swallowing either
  failure. The defer fires on **every** exit path once `Swap` has actually
  mutated the tracked file — success, a failed switch, a failed restore, or
  a failed verification — closing exactly the gap that caused today's manual
  recovery.
- CLI wiring (`internal/cli/test.go`, `drSecretsSupport`): a no-op when the
  source has no tracked secrets file. When it does, `--restore-server` (not
  `--restore-host`) becomes required, since re-encrypting for the restore
  host needs its own resolvable server name to look up its recipient — a
  clear `ExitError` explains why rather than failing deep inside
  `sops-install-secrets` later.
- `Migrate`/`Restore`/`service restore` are untouched — see Context.

## Consequences

- No private key ever leaves the machine running the CLI. The restore host's
  own `/etc/sops/age/keys.txt` is never written to by a drill.
- No `.sops.yaml` changes, no new standing recipients — Model A's
  per-server sole-recipient invariant is completely unchanged.
- A drill against a secrets-bearing source now requires `--restore-server`
  and a git-clean `secrets/<source>.yaml`; a dirty file refuses with a clear
  message rather than an ambiguous partial-swap state.
- A drill that fails partway now **always** reverts the tracked secrets file
  before returning, and a killed process mid-drill (the one case the defer
  can't cover) is recoverable with `git checkout -- secrets/<source>.yaml`.
- Verified end-to-end with real `sops`/`age` binaries (not mocked): decrypt
  with the source's key, re-encrypt to the target's, confirm the target
  decrypts and the source no longer can, revert, confirm the source decrypts
  again and the bytes match exactly
  (`internal/core/dr_secrets_test.go`).

## Follow-up

- Re-run the real demos-server drill (`test dr hetzner-fsn-c4r8d160-20260710c
  --restore-server hetzner-nbg1-c4r8d160-20260912c`) end-to-end to confirm it
  now completes and the spare reverts cleanly on its own.
- Note in `docs/operations-runbooks.md`'s "Restore Rehearsal" section that a
  secrets-bearing drill now requires `--restore-server` and a git-clean
  secrets file.

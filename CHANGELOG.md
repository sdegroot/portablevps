# Changelog

## Unreleased

- **Fix: upstream copies no longer issue a new sync-bot token on every
  deploy.** `provision-upstream-copies.sh` validated the stored token with
  `GET /user`, which needs `read:user`. The token deliberately has only
  `write:repository`, so the check always got 403. Every `forgejo-provision`
  run therefore issued another token and left the previous ones valid.
  - **Now:** the check uses `GET /repos/search?limit=1`, which is within the
    token's scope and works before any copy exists.
  - **Tested against Forgejo 16.0.5** with sign-in required and restricted
    users: two provisionings issue one token (unchanged, one in the database),
    and a corrupted token is still re-issued.
  - **Tokens already issued** by earlier deploys stay valid until removed by
    hand.

- **Forgejo package publishers: CI can publish packages.** Forgejo's
  automatic Actions token can't publish packages, and Forgejo has no
  `permissions: packages: write`. New
  `portablevps.apps.forgejo.organizations.<org>.packagePublisher.enable`
  gives the org a local bot (`<org>-packages`) in a `package-publishers` team
  that only grants `repo.packages` = write.
  - **Token:** the bot's `write:package` token lives in
    `/var/lib/portablevps/forgejo-package-publishers` (not backed up). It is
    re-issued only when missing or invalid.
  - **Org Actions:** the token is written to the org's secrets as
    `PACKAGES_TOKEN`, and the bot's name to its variables as
    `PACKAGES_USER`, on every run, so both self-heal.
  - **Tested against Forgejo 16.0.5:**
    - provisioning, then publishing with the token (201);
    - an idempotent re-run, with the token unchanged;
    - a deleted secret restored;
    - a corrupted token re-issued and working;
    - the bot refused on another org (401) and unable to read the org's repos.
  - **Docs:** `docs/forgejo-package-registry.md` (registry addresses, the bot,
    workflow examples for Docker, npm and Maven, limitations).

- **Forgejo upstream copies: a repository that follows an upstream and can
  still carry private branches.** New
  `portablevps.apps.forgejo.upstreamCopies."<owner>/<repo>" = { upstream; privatePrefix ? "internal/"; }`,
  plus `upstreamSync.{botUsername, interval}`.

  A pull mirror is read-only as a whole, and Forgejo 16's branch protection
  can't express "everything is upstream's except `internal/**`": a `**` rule
  overrides a more specific one, and there are no rule priorities. So this
  combines three pieces:
  - **Sync.** A `forgejo-upstream-sync` timer (every 5 minutes by default)
    copies branches and tags from the upstream, read anonymously.
    - The default branch and tags move fast-forward only. A rewrite upstream
      fails the run loudly instead of rewriting history here.
    - Other branches follow upstream exactly, including rebases.
    - Deletions are copied.
    - The private prefix is never fetched, pushed or deleted.
    - Only changed refs are pushed. One failing copy doesn't stop the others.
    - The bot's token reaches git through `GIT_CONFIG_*` environment
      variables, never argv.
  - **Hook.** A `pre-receive` hook, installed in the repo's
    `hooks/pre-receive.d` (still honoured by Forgejo 16's central hooks):
    - Only the sync bot may move upstream-owned refs.
    - Everyone else may only push under the private prefix. This also covers
      merges done in the web UI.
    - The bot may never touch the private prefix.
  - **Provisioning.** `forgejo-provision` creates the local bot (no SSO, a
    random password nobody knows) and its token. The token is kept in
    `/var/lib`, outside the backups, and re-issued if missing or invalid.
    Provisioning then creates missing repositories with upstream's default
    branch, and refuses an existing pull mirror, which can't take pushes. It
    gives the bot write access and installs the hook.

  Tested against Forgejo 16.0.5 with a local upstream:
  - first sync;
  - developer pushes to the default branch and other branches refused, and to
    `internal/` allowed;
  - new, rebased, deleted and tagged refs copied, with `internal/` untouched;
  - an upstream rewrite of the default branch refused while other refs still
    applied;
  - an upstream `internal/` branch not copied;
  - re-runs idempotent.

  The three scripts are shellcheck-clean.

  Documented in `docs/forgejo-upstream-copies.md` (configuration, sync and hook
  rules, the developer workflow, provisioning, removal and limitations). The
  design is in ADR 0005. A runbook entry, "Forgejo Upstream Copy Sync Failing",
  covers the rest; its recovery for a deliberate upstream rewrite was run
  against the test instance.

- **Fix: `server adopt --password` now reaches the password prompt.** The
  password bootstrap ran `sshpass ssh …` without restricting authentication.
  So ssh first offered every key the agent holds, including an `IdentityAgent`
  set in `~/.ssh/config` such as 1Password's. With more keys than the host's
  `MaxAuthTries` (6), sshd disconnected with "Too many authentication failures"
  before the password was tried, which made adopt unusable for anyone with a
  well-stocked agent. Observed adopting a fresh Leaseweb VPS on 2026-09-28.

  The password path now passes `PubkeyAuthentication=no`, `IdentityAgent=none`,
  `IdentitiesOnly=yes` and
  `PreferredAuthentications=password,keyboard-interactive` ahead of the other
  options; ssh takes the first value it sees. The initial-key and agent paths
  are unchanged. Unit tests cover both.

- **Runner jobs find the Docker daemon without per-workflow configuration.**
  New `portablevps.apps.forgejoRunner.jobEnv` sets environment variables in
  every job (the runner's `runner.envs`).

  When jobs share the host network, the default sets
  `DOCKER_HOST=tcp://127.0.0.1:2375`, the Docker-in-Docker sidecar. So
  Testcontainers, `docker build` and similar tools just work, and workflows
  stay portable across runners. Before, each workflow had to hard-code this
  runner's layout. On any other network the default is empty, because the
  sidecar's loopback address isn't reachable from a job there.

  This grants no new access: with host networking, jobs could already reach
  the sidecar.

  Tested:
  - The rendered runner config contains `runner.envs.DOCKER_HOST` on a
    host-network runner.
  - With `container.network = "bridge"` the default evaluates to `{}`.

- **`network policy-sync` now reconciles people and group membership, not
  only policies.**
  - **People:** the fleet's `.#netbird` output gains `users`
    (`{ "<email>" = [ groups ]; }`). Each declared user's NetBird
    `auto_groups` become their unmanaged groups plus their declared ones, and
    undeclared users lose managed groups.
  - **Managed groups:** every group named by a policy, a server's
    `netbird.groups` or a user. policy-sync sets each one's membership to
    exactly the declared servers' and users' peers and removes anything else,
    so a role change or an offboarding takes access away. NetBird's `All` and
    console-made groups are never touched.
  - **`--dry-run`:** reads the account and prints every group, policy, user
    and `Default` change it would make. Policies that already match report
    `unchanged` instead of being rewritten on every run.
  - **Recovery:** `disableDefaultPolicy = false` now **re-enables** NetBird's
    `Default` policy. Before, the code could only disable it, so the
    documented lockout recovery did not work.
- **`network sync <server>` removes the peer from managed groups it no longer
  declares.** Before, it only ever added, so a repurposed server kept its old
  role's access. Seen live: an Authentik box still in `website`.
- The legacy Python `task cloud:netbird-policy-sync` still reconciles
  policies only. It ignores `users` and membership, so use
  `pvps network policy-sync`.

- **Forgejo 16.0.5 and Forgejo Runner 13 are the new defaults** (were 15.0.4
  and 12). The upgrade was rehearsed on PostgreSQL 18:
  - 15.0.4 was provisioned like production (orgs and teams through
    `provision-orgs.sh`, an OIDC source with group mapping, a private repo),
    then swapped to 16.0.5 on the same data.
  - The migrations ran cleanly and everything survived.
  - Anonymous access is still refused.
  - Re-provisioning is a no-op.
  - All the CLI commands `forgejo-provision` uses still work:
    `change-password --must-change-password=false`, `update-oauth` with the
    group flags, `generate-runner-token`, `generate-access-token`.
  - Runner v13.2.0 registers against 16.0.5.

  Upgrade notes:
  - **Forgejo 16:** the Docker image no longer defaults
    `REVERSE_PROXY_TRUSTED_PROXIES` to `*`. That only matters for
    reverse-proxy authentication; the proxy here reaches Forgejo from
    loopback. 16 also adds a cancel-run API
    (`POST /repos/{owner}/{repo}/actions/runs/{id}/cancel`) and job-log
    download.
  - **Runner 13:** requires Docker ≥ 25 (the DinD sidecar is 28), and drops
    `container.network_mode` (the module uses `container.network`) and the
    `GITEA_*` environment variables (unused).
  - **Runner 13 is stricter about workflows.** Invalid expressions and invalid
    matrices now fail the job, and `::set-output`, `::add-path` and
    `::set-env` are gone; use `$FORGEJO_OUTPUT`, `$FORGEJO_PATH` and
    `$FORGEJO_ENV`.
- **Restore drills no longer let the restore host back up into the source's
  repository.** `pvps dr --mode remote` leaves the restore host running the
  source's configuration, including its backup timers. Those timers are
  `Persistent`, so they fired within seconds of the drill's finalize switch,
  before PASS. On the Epistola Forgejo drill (2026-09-27) the spare's backup
  had computed a PostgreSQL incremental and was inside restic when it was
  stopped by hand.

  Now `RestoreOpts.Drill` makes the drill write the drilled server's name to
  `/var/lib/portablevps/restore-drill-host` while the host is still in restore
  mode, before any timer is armed. `portablevps-backup`, `-maintenance` and the
  immutability probe gain an `ExecCondition` that skips the run while that file
  names the host's own `portablevps.server.name`.

  The guard disarms itself when the spare is switched back to its own
  configuration, because the name no longer matches. A real restore
  (`service restore`) deletes the file at the same step, so a guard left by an
  earlier drill can never silence a genuinely recovered server.

  Tested in two places:
  - Go unit tests cover guard-before-finalize and clear-on-real-restore.
  - The generated guard script, taken from an evaluated host, skips only for
    its own name, and runs with no file, another server's name, or an empty
    file.

- **Fix: Forgejo's break-glass admin is no longer forced to change its
  password on every deploy.** `forgejo-provision` re-applied the sops password
  with `forgejo admin user change-password`, which defaults to
  `--must-change-password=true`. Forgejo then answered every API call as that
  user with 403 ("You must change your password"), which broke the new org and
  team provisioning on its first real deploy. It also meant a break-glass login
  demanded a new password first. It now passes `--must-change-password=false`,
  reproduced and verified against Forgejo 15.0.4.

- **Forgejo can take its roles from the identity provider.** New
  `portablevps.apps.forgejo.oidc.{groupClaimName,adminGroup,restrictedGroup,groupTeamMap,groupTeamMapRemoval}`
  are passed to the OAuth2 login source, so an IdP group can make someone a
  site admin or put them in an org team at every login, and with removal on,
  taking the group away in the IdP takes the team away at the next login. Setting
  `adminGroup` also revokes site admin from SSO users outside the group. The
  flags are always passed, empty values included, so that unsetting an option
  also clears it on `update-oauth` instead of leaving the old value behind.

  The teams a map points at have to exist, so the new
  `portablevps.apps.forgejo.organizations.<org>.teams.<team>` declares them:
  visibility, per-unit access (`units_map`: code, issues, pulls, releases,
  packages, actions, …), "all repositories" and "may create repos".
  `forgejo-provision` upserts them through the API as the break-glass admin
  (`apps/forgejo/provision-orgs.sh`). The password goes to curl through a 0600
  config file, not argv. It is additive: undeclared orgs and teams are never
  deleted, and assigning repositories to teams is left to the operator. Tested
  against Forgejo 15.0.4 with the password form hidden: a first run creates, a
  second run is a no-op, and a changed unit or description is patched.

- **Applications no longer have to connect to PostgreSQL as the cluster
  superuser.** `portablevps.postgres.user` names the role the container image
  creates on first boot, and that role is the superuser: it can read every
  database on the box, create roles, bypass row-level security and open a
  replication stream. Backups genuinely need it, since `pg_basebackup` requires
  `REPLICATION` — applications never did, yet pointing them at it was the only
  thing the module offered. New `portablevps.postgres.appRoles.<name>` declares
  an unprivileged login role instead: created and then explicitly held to
  `NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`, granted
  `CONNECT`/`TEMPORARY` on its one database with `PUBLIC` revoked, and made owner
  of its schema (`public` by default, or a namespace of its own via `schema`) so a
  migration tool can still create and alter its own tables. The superuser stays
  for administration and for the backup chain.

  Roles are reconciled on every activation rather than seeded into
  `/docker-entrypoint-initdb.d`, because an init script only ever runs on an empty
  cluster: a rotated password, or a host rebuilt by `restore.sh` from a physical
  backup, would otherwise never converge. The password is read from its secret
  file at activation and reaches `psql` through the environment (`\getenv`), so it
  appears in neither the process list nor the world-readable store — the new
  `postgres-app-roles` check asserts that, alongside every privilege clause.
  Prototype/local-VM hosts fall back to a fixed password file, so
  disaster-recovery runs still need no sops.

  Assertions catch the two ways this goes wrong quietly: naming an app role after
  the superuser, and pointing one at a database the cluster never creates.

- **The dead-man's switch now also goes quiet when alerts can't be
  delivered, not only when the pipeline is down.** An Alertmanager whose
  receiver rejects every send still answers `/-/healthy`. So when an SMTP relay
  started refusing the monitoring box (`525 5.7.1 Unauthorized IP address`),
  every alert email was lost for six days while the pulse kept reporting all
  well. The pulse is now also withheld while Alertmanager's self-scraped
  `alertmanager_notifications_failed_total` rose within
  `deadMansSwitch.notificationFailureWindow` (default `30m`; `null` disables).
  The external monitor is then the one channel left that can say so. The
  check fails closed: an unanswerable query withholds too. Replayed against
  the real outage, the query is empty before and after it and non-empty
  throughout, so the external alarm would have fired within minutes of the
  first failed notification.
- **New log alert `AuthentikEmailSendFailing`** (`rules-vlogs/authentik.yml`):
  any authentik `send_mail` attempt that ended with an exception in the last
  15 minutes. A failed signup confirmation leaves the enrollee with an
  inactive account and no email, and nothing else surfaces it. Validated with
  `vmalert -dryRun` and replayed against real logs: it fires on both failure
  windows of that outage and stays silent on successful sends.

- **Container environment values with spaces now reach the app intact.** The `website` and
  `custom` apps wrote `Environment=KEY=value` unquoted into their Quadlet units, and Quadlet
  (like systemd) splits that line on spaces: `OIDC_SCOPES=openid profile email offline_access`
  arrived as `OIDC_SCOPES=openid`, plus stray `profile`, `email` and `offline_access` variables.
  Every `Environment=` line is now quoted, with `\`, `"` and `%` escaped (`lib/quadlet.nix`), and
  invalid names or values with newlines fail at evaluation. A `quadlet-environment` flake check
  guards the rendering. **Check deployed apps after upgrading:** a value that used to be cut off
  now arrives in full.
- **The installed CLI binary is now `pvps`, not `portablevps`.** `mise use -g
  github:sdegroot/portablevps` and `nix run github:sdegroot/portablevps`
  both now give you a `pvps` command; `nix build .#pvps`/`nix run .#pvps`
  from a checkout too (`.#portablevps` no longer exists). The project,
  repository, Go module path, and NixOS option namespace (`portablevps.*`)
  are unchanged — only the CLI command got shorter.
- **portablevps is now a standalone repository**, extracted from the
  `epistola-nix-infra` monorepo with its own release cycle. The Nix flake
  library and Go CLI live at the repo root (no more `?dir=portablevps`);
  consume it as `github:sdegroot/portablevps`.
- **`portablevps --version` now exists.** Wired through `-ldflags`; the Nix
  build reports a git-revision-derived version, and tagged releases will
  report their real semver tag.
- **Server definitions now catch typos in `placement`/`info` immediately**,
  with a named-option error (and a "did you mean...?" suggestion) instead of
  a confusing failure deep inside unrelated module evaluation later.
- **`service migrate` now verifies the target's TLS certificate before
  touching the source**, aborting the cutover early if one never appears,
  and **automatically repoints internal NetBird DNS** and reports a
  restore-drill metric after a verified migration (previously manual
  follow-up steps only present in the legacy Python CLI).
- **No more Epistola-specific defaults in the tool itself.** The
  `epistola-suite` app (Epistola's own product, not a generically
  self-hostable one) moved to a consumer-side overlay on the generic
  `portablevps.apps.custom` schema; remaining example values and comments
  referencing `epistola.*` domains were genericized.
- **Fixed a false "registered backup path does not exist" failure during
  `test dr`/`service migrate`** for backup paths that live inside a
  root-only-accessible directory (e.g. Traefik's `acme.json`, under a `700`
  directory). The seed/verify existence checks now run as root (`sudo test
  -d`/`-f`) instead of as the unprivileged admin user, which previously
  couldn't even traverse into the directory to see the file.
- **Fixed `test dr` against a secrets-bearing server**: the restore host,
  never provisioned under the source's identity, could never decrypt the
  source's sops secrets once switched into it (`0 successful groups
  required, got 0`), stranding the restore host mid-switch on failure. `test
  dr` now re-encrypts the source's tracked secrets file to the restore
  host's own already-registered recipient before switching (no private key
  ever leaves the operator's machine) and guarantees a revert on every exit
  path, success or failure. Secrets-bearing drills now require
  `--restore-server` (not `--restore-host`). See `docs/adr/0004-cross-host-
  secrets-for-restore-drills.md`.

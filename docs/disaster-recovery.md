# Disaster Recovery Runbook

Use this runbook when a PostgreSQL VM has been lost and must be restored from a
restic backup.

For a failed NixOS upgrade where the disk and data are intact, prefer the
rollback procedures in `docs/operations-runbooks.md`. Backup restore is for
lost hosts, damaged disks, corrupted data, provider moves, or cases where no
usable previous generation can be booted.

## Recovery Objectives

- **RPO: up to one hour.** Backups run on an hourly timer and WAL is only
  captured at backup time; there is no continuous WAL archiving. Work
  committed after the last successful backup is lost on host loss. If a
  tighter RPO is ever required, WAL shipping to S3 has to be added first.
- **RTO: operator-driven, typically well under an hour for small datasets.**
  Restore requires provisioning or rescue-booting a host, a restore-mode
  install, `restore.sh`, and a profile switch. `pg_combinebackup` replays one
  backup chain; `portablevps.postgres.backup.maxChainLength` bounds chain length so
  restore time stays predictable.
- Watch `portablevps_backup_last_success_timestamp_seconds` from the monitoring
  server: the real RPO at any moment is the age of the last successful
  backup, not the timer schedule.

## Preconditions

- The Git repository is available.
- The restic repository is outside the destroyed server and reachable by the
  fresh server.
- The S3 repository allows normal restic operation. Backup objects should be
  protected against deletion, but restic must be able to delete temporary
  `lock/*` objects.
- The fresh server has storage available for `/data/postgres` and
  `/data/container-state`. This can be the root filesystem, an attached disk, or
  ZFS datasets.

## Steps

1. Provision a fresh server or VM.
2. Boot NixOS and enable SSH.
3. Clone this repository.
4. Apply restore mode:

   ```sh
   sudo nixos-rebuild switch --flake .#local-vm-restore
   ```

5. Prepare `/data/postgres` and `/data/container-state`. PostgreSQL 18 stores
   the actual cluster below the Postgres mount at `/data/postgres/18/docker`.
   Containers should put durable file state below `/data/container-state`. For
   the local QEMU/ZFS prototype, create the ZFS pool:

   ```sh
   sudo create-zpool.sh /dev/disk/by-id/YOUR-DATA-DISK
   ```

6. Export the restic environment for the shared backup repository. For local
   QEMU testing, use:

   ```sh
   eval "$(scripts/local-s3-env.sh)"
   ```

7. Restore file state and combine the PostgreSQL physical backup chain:

   ```sh
   sudo restore.sh
   ```

8. Confirm PostgreSQL is still stopped after the data directory is restored:

   ```sh
   ! systemctl is-active --quiet postgres.service
   ```

9. Switch to normal mode:

    ```sh
    sudo nixos-rebuild switch --flake .#local-vm
    ```

10. Verify PostgreSQL data:

    ```sh
    sudo verify-test-data.sh "expected-marker"
    ```

11. Start remaining apps if needed:

    ```sh
    sudo systemctl start apps.target
    ```

## Automated Local DR Test (any server)

The steps above are the manual restore procedure. To validate a *specific
server's* backup/restore automatically on your laptop — including its app
(e.g. authentik's database + media survive) — use the two-VM local harness.
It boots two throwaway QEMU VMs, installs the server, seeds a marker, and runs
**the real `portablevps-backup.service`** (not `backup.sh` directly) followed by
`restore.sh` on a fresh VM, then verifies the data survived.

Local VMs are the `local` provider: no NetBird mesh, no public DNS/ACME, and
backups go to a local test S3 (the QEMU MinIO), never the real bucket. Each
`<server>` gets a `<server>-local-vm` / `<server>-local-vm-restore` config via
`lib.mkFlake`.

Every registered backup directory receives a marker, and individually
registered files are compared by checksum so the generic harness covers both
forms without corrupting application state. Apps that own state outside the
generic platform PostgreSQL marker can add
executable hooks under `/etc/portablevps/dr/seed.d` and
`/etc/portablevps/dr/verify.d`. The harness passes the marker string to each
hook, so an app can seed and verify its own database tables, uploads, or other
state without hardcoding app logic into the test driver.

**The tool's own demo** (from `portablevps/`):

```sh
scripts/qemu-create-vm.sh vm-a                  # once: create the VM disks
scripts/qemu-create-vm.sh vm-b                  # (first boot installs NixOS from an ISO)
task qemu:boot-a                                # terminal 1
task qemu:boot-b                                # terminal 2
task validate:qemu                              # terminal 3 (CONFIG defaults to local-vm)
```

**A consumer server** (from the consumer repo, e.g. `epistola/`) — the flake
references `path:../portablevps`, so the VMs share the monorepo root and the
flake dir is the consumer subdir:

```sh
task dr:boot-a                                  # terminal 1
task dr:boot-b                                  # terminal 2
task dr:validate SERVER=<machine-name>          # terminal 3
```

`dr:validate` runs `validate:qemu CONFIG=<machine>-local-vm FLAKE_DIR=/host/epistola`.
A green run ends with `PASS: fresh server restored PostgreSQL data from backup`.

Because this drives the same systemd units, PATH, and repo self-init
(`ExecStartPre = init-backup-repo.sh`) as a live host, it catches
service-level backup regressions that invoking the scripts directly would miss.

## Cloud Provider Flow

This flow rebuilds a **lost or retired** machine onto a replacement host. With
machine-based naming a server's name is its permanent identity (hostname +
NetBird peer + secrets), so the replacement is rebuilt as that **same** name —
there is no temporary identity to assign and later rename. Because it reclaims
the machine's name and NetBird peer, the original must be **offline** first, or
the two collide on the mesh.

> Moving a *live* service to a new box with no downtime is a different
> operation: provision a **new** machine (`<provider>-<region>-<date><letter>`,
> its own name), deploy the app, restore its service data, then flip the service
> CNAME to the new machine and retire the old one. That path never renames a
> machine. Use `task cloud:migrate-service` for that case; it switches the
> target to restore mode, pre-warms TLS, takes a final source backup, stops the
> source, restores the target, verifies the marker, and syncs NetBird DNS. See
> `docs/operations-runbooks.md#service-migration`.

First, capture a fresh backup from the source (or use the last good one if the
source is already lost):

```sh
mise exec -- task cloud:restore-rehearsal PHASE=prepare \
  SERVER=test-vps SOURCE_HOST=1.2.3.4
```

Then, with the source stopped, boot the replacement host into provider rescue
mode and run:

```sh
mise exec -- task cloud:restore-candidate \
  SERVER=test-vps SOURCE_HOST=1.2.3.4 RESTORE_HOST=5.6.7.8 \
  ROOT_IDENTITY=.local/ssh/cloud-admin_ed25519 \
  CONFIRM_DESTROY=5.6.7.8
```

The restore phase wipes the replacement disk, installs the restore profile as
`test-vps`'s own identity, restores from restic, switches the host to the normal
server profile, starts `apps.target`, and verifies the marker from the prepare
phase.

The older same-VPS destructive test remains available through
`cloud:restore-test` with `HOST=...`. Use it when only one disposable VPS is
available, not as the preferred production rehearsal.

Finalize the promoted replacement once the source is confirmed offline:

```sh
mise exec -- task cloud:promote-candidate SERVER=test-vps \
  CANDIDATE_HOST=5.6.7.8 SOURCE_OFFLINE=1 CONFIRM_PROMOTE=test-vps
```

The replacement already carries `test-vps`'s identity, so this finalizes it on
the normal `.#test-vps` profile and records it as the active host. It is the
manual cutover point for a host replacement.

The Hetzner + Scaleway path has been validated end to end with restic snapshot
`35deb21a` and PostgreSQL marker `recovery-20260624T083053Z-7810`.

## S3 Object Lock

Use a server-specific restic repository prefix inside an environment bucket:

```text
portablevps-backups-prod/servers/hetzner-primary/restic/
```

For direct restic-to-S3 backups, do not lock the entire live repository if that
also locks `lock/*`. Restic creates and deletes lock objects during normal
operation.

Recommended policy:

- Allow `ListBucket` on the bucket.
- Allow `GetObject` and `PutObject` on the server repository prefix.
- Allow `DeleteObject` only for `servers/SERVER/restic/lock/*`.
- Deny delete for `data/*`, `snapshots/*`, `index/*`, `keys/*`, and `config`
  below the server repository prefix.

A concrete starting policy for the server's backup credentials (adapt the
principal and ARN syntax to the provider's policy dialect; Scaleway accepts
S3-style bucket policies with its own principal format):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "AllowList",
      "Effect": "Allow",
      "Action": "s3:ListBucket",
      "Resource": "arn:aws:s3:::portablevps-backups-prod"
    },
    {
      "Sid": "AllowReadWrite",
      "Effect": "Allow",
      "Action": ["s3:GetObject", "s3:PutObject"],
      "Resource": "arn:aws:s3:::portablevps-backups-prod/servers/test-vps/restic/*"
    },
    {
      "Sid": "AllowLockDelete",
      "Effect": "Allow",
      "Action": "s3:DeleteObject",
      "Resource": "arn:aws:s3:::portablevps-backups-prod/servers/test-vps/restic/lock/*"
    },
    {
      "Sid": "DenyBackupDelete",
      "Effect": "Deny",
      "Action": "s3:DeleteObject",
      "Resource": [
        "arn:aws:s3:::portablevps-backups-prod/servers/test-vps/restic/data/*",
        "arn:aws:s3:::portablevps-backups-prod/servers/test-vps/restic/snapshots/*",
        "arn:aws:s3:::portablevps-backups-prod/servers/test-vps/restic/index/*",
        "arn:aws:s3:::portablevps-backups-prod/servers/test-vps/restic/keys/*",
        "arn:aws:s3:::portablevps-backups-prod/servers/test-vps/restic/config"
      ]
    }
  ]
}
```

This policy is what stops a compromised host from deleting its own backups:
the host necessarily holds working S3 credentials in
`/etc/portablevps/restic.env`. Without a deny-delete policy, host root can wipe
the repository.

Deny-delete conflicts with the weekly retention job: `restic forget --prune`
must delete `snapshots/*`, `data/*`, and rewrite `index/*`. Choose one mode
per repository:

- **Mutable repository** (default): no deny-delete policy, retention enabled
  (`portablevps.backups.retention.enable = true`). Protect against deletion with
  versioning or replication to a second locked bucket instead.
- **Append-only repository**: apply the deny-delete policy and set
  `portablevps.backups.retention.enable = false`. Rotate to a fresh repository
  periodically and delete the old one with elevated (non-host) credentials.

If bucket-wide object lock cannot exclude `lock/*`, use a live operational
bucket for restic and replicate immutable backup objects into a locked archive
bucket.

## Critical Rules

- Do not start `apps.target` before restore is complete.
- Do not restore while the configured PostgreSQL container is running.
- Keep backups outside the server being destroyed.
- Keep restic `lock/*` objects deletable, or backups and restores can block on
  stale locks.
- ZFS is optional local storage tooling, not the backup mechanism.
- VM snapshots may speed up testing, but they do not prove backup correctness.

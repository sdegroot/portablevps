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

## Cloud Provider Flow

For provider VPSes, prefer a separate-host restore rehearsal. The source host
keeps running, writes a backup, and a disposable restore host is rebuilt from
that backup.

```sh
mise exec -- task cloud:restore-rehearsal PHASE=prepare \
  SERVER=test-vps SOURCE_HOST=1.2.3.4
```

After the backup completes, boot the disposable restore host into provider
rescue mode and run:

```sh
mise exec -- task cloud:restore-candidate \
  SERVER=test-vps SOURCE_HOST=1.2.3.4 RESTORE_HOST=5.6.7.8 \
  ROOT_IDENTITY=.local/ssh/cloud-admin_ed25519 \
  CONFIRM_DESTROY=5.6.7.8
```

The restore phase wipes the restore host disk, installs the restore profile,
restores from restic, switches the host back to the normal server profile,
starts `apps.target`, and verifies the marker from the prepare phase.

Restore rehearsals use a unique hostname and Netbird peer name derived from the
restore host by default. Override them when needed:

```sh
mise exec -- task cloud:restore-rehearsal PHASE=restore \
  SERVER=test-vps SOURCE_HOST=1.2.3.4 RESTORE_HOST=5.6.7.8 \
  RESTORE_HOSTNAME=portablevps-restore-a \
  RESTORE_NETBIRD_NAME=portablevps-restore-a \
  ROOT_IDENTITY=.local/ssh/cloud-admin_ed25519 \
  CONFIRM_DESTROY=5.6.7.8
```

The older same-VPS destructive test remains available through
`cloud:restore-test` with `HOST=...`. Use it when only one disposable VPS is
available, not as the preferred production rehearsal.

Promote a validated candidate only after the source is stopped:

```sh
mise exec -- task cloud:promote-candidate SERVER=test-vps \
  CANDIDATE_HOST=5.6.7.8 SOURCE_OFFLINE=1 CONFIRM_PROMOTE=test-vps
```

This switches the candidate to the stable `.#test-vps` identity. It is the
manual cutover point for a provider migration.

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
- Do not restore while the `postgres-demo` container is running.
- Keep backups outside the server being destroyed.
- Keep restic `lock/*` objects deletable, or backups and restores can block on
  stale locks.
- ZFS is optional local storage tooling, not the backup mechanism.
- VM snapshots may speed up testing, but they do not prove backup correctness.

# portablevps

Create a single-instance application server (an app plus PostgreSQL) on any
VPS with NixOS, and move it to another host or provider at any time.
Reproducibility, online backups, tested restore, and monitoring are
first-class. Licensed under the EUPL-1.2 (see `LICENSE`).

## What it is

portablevps is a flake library, a set of reusable NixOS modules, and a CLI.
You describe *logical servers* in a small consumer repository; portablevps
turns each into a reproducible NixOS system, provisions or installs it onto a
provider VPS, backs PostgreSQL up online to S3-compatible storage, and restores
it onto a fresh host as a tested workflow rather than an emergency.

Key properties:

- **Disposable hosts.** The flake recreates the machine; the restic repository
  recreates the data. Moving hosts is a rehearsed restore, not a scramble.
- **Restore mode** keeps application services stopped until data is restored.
- **Mesh-VPN-first.** Application and admin traffic go over NetBird or
  Tailscale, so a host move does not touch public DNS.
- **Pluggable providers.** Provider metadata plus a lifecycle adapter; Hetzner
  is implemented, others are metadata-only until an adapter is added.

## Consuming portablevps

Scaffold a consumer repository from the template:

```sh
nix flake init -t github:OWNER/portablevps?dir=portablevps#server
```

A consumer flake is small — it points at portablevps and its own `servers/`:

```nix
{
  inputs.portablevps.url = "github:OWNER/portablevps?dir=portablevps";
  outputs = { self, portablevps }:
    portablevps.lib.mkFlake { inherit self; serverDir = ./servers; };
}
```

Each `servers/<name>.nix` selects a provider placement, a reusable profile
(e.g. `single-instance-app`), the mesh peer name, its backup repository, and
any proxy routes. See `templates/server/servers/example.nix`.

The first consumer, `epistola/`, lives beside this tool in the same repository
and is the reference for a real deployment.

## The tool's own test hosts

portablevps ships its own disaster-recovery proof: the `.#local-vm` and
`.#local-vm-restore` NixOS configurations plus a two-VM QEMU test that inserts
data, backs it up, restores onto a second VM, and verifies the marker. Run
`mise exec -- task validate:qemu`.

## Cloud VPS Setup

The first cloud target is an already-created VPS or rescue system that is
reachable over SSH. Provider API provisioning is intentionally not part of this
step.

Operational recovery procedures live in `docs/operations-runbooks.md`. Use that
runbook for Netbird registration checks, bad upgrade rollback, migration
rollback decisions, and backup failures. Use `docs/disaster-recovery.md` when a
host must be rebuilt or moved from backup.

Create the cloud admin keypair:

```sh
mise exec -- task cloud:keygen
```

This writes the private key to `.local/ssh/cloud-admin_ed25519` and commits the
matching public key at `keys/cloud-admin.pub`. The private key stays local
because `.local/` is ignored.

Cloud installs require encrypted sops secrets, decrypted on the host with an
age identity that the CLI ships to `/etc/sops/age/keys.txt` during install.

Prefer a **per-server age key** so a single host compromise does not expose
every server's secrets:

```sh
mise exec -- task cloud:secrets-init-server SERVER=<name>
```

This generates `.local/sops/servers/<name>/age-key.txt` and prints the age
recipient. Add that recipient (plus your operator recipient) to `.sops.yaml`
for `secrets/<name>.yaml`, point `portablevps.secrets.file` at that file, and
`sops updatekeys` it. Install then ships the per-server key automatically; the
CLI falls back to a shared `.local/sops/age-key.txt` only for repositories that
have not migrated.

Edit the encrypted values before a real cloud install:

```sh
SOPS_AGE_KEY_FILE=.local/sops/age-key.txt mise exec -- sops secrets/secrets.yaml
```

The required values are:

- `postgres.password`
- `restic.password`
- `restic.aws-access-key-id`
- `restic.aws-secret-access-key`
- `netbird.setup-key`
- `traefik/acme-env` when `portablevps.proxy.enable = true`

## Provider Lifecycle

Lifecycle management is the local control-plane layer that creates, rescues,
deletes, and discovers concrete VPS instances. Nix still describes the server
that gets installed; provider lifecycle state is intentionally kept outside git
under `.local/cloud-state/servers/<server>.json`.

Hetzner lifecycle management is implemented first. Netcup and Leaseweb have
provider metadata and explicit unsupported lifecycle adapters until their APIs
are added.

Create a local Hetzner API env file:

```sh
mkdir -p .local/providers
printf 'HCLOUD_TOKEN=...\n' > .local/providers/hetzner.env
chmod 600 .local/providers/hetzner.env
```

### Password-manager references

Any operator credential the CLI reads — provider tokens
(`.local/providers/<provider>.env`) and API tokens such as
`NETBIRD_API_TOKEN` — may be a **secret reference** instead of a plaintext
value, resolved at runtime:

```sh
# .local/providers/hetzner.env
HCLOUD_TOKEN=op://Private/Hetzner/token     # 1Password (needs the `op` CLI)
# or
HCLOUD_TOKEN=env://HCLOUD_TOKEN             # read from the environment
```

Plain values still work unchanged. 1Password is resolved with `op read`;
support for another manager (Bitwarden, pass, Vault, ...) is a scheme handler
registered in `scripts/portablevps_cloud/secrets.py`.

For the host secrets that are encrypted into `secrets/<server>.yaml`, keep them
in 1Password and fill the sops file with 1Password's own tooling — write a
template of `op://` references and run `op inject` (or `op run`) before
`sops --encrypt`. That keeps the password manager as the source of truth
without the host ever holding a service-account token.

Create a candidate VPS, enable rescue mode, and install onto it:

```sh
mise exec -- task cloud:lifecycle-preflight SERVER=test-vps ROLE=candidate
mise exec -- task cloud:create SERVER=test-vps ROLE=candidate
mise exec -- task cloud:rescue SERVER=test-vps ROLE=candidate
mise exec -- task cloud:install-created SERVER=test-vps ROLE=candidate
```

Inspect or delete local/provider state:

```sh
mise exec -- task cloud:status SERVER=test-vps ROLE=candidate
mise exec -- task cloud:delete SERVER=test-vps ROLE=candidate CONFIRM_DELETE=<provider-server-id>
```

`cloud:create` refuses to create a second server for an existing role in local
state. Use `ROLE=active` for the currently serving server and `ROLE=candidate`
for restore or migration rehearsal hosts.

Install NixOS onto an existing VPS with `nixos-anywhere`:

```sh
mise exec -- task cloud:preflight SERVER=test-vps HOST=1.2.3.4 \
  ROOT_IDENTITY=.local/ssh/cloud-admin_ed25519
```

```sh
mise exec -- task cloud:install SERVER=test-vps TARGET=root@1.2.3.4 DISK=/dev/sda
```

`cloud:preflight` checks the logical server config, active provider metadata,
local admin and sops keys, flake evaluation, and, when `HOST` or `TARGET` is
set, rescue SSH plus disk discovery. `TARGET` is the rescue or temporary Linux
SSH target. The server selects its active provider placement; `test-vps` uses
Hetzner. `DISK` is the disk that will be
partitioned and formatted by disko; for Hetzner it defaults to `/dev/sda`.
Installs verify that the chosen disk actually exists on the target before
anything is wiped. When the target has more than one disk, automatic
detection refuses to choose and the provider default is rejected; an explicit
`DISK=/dev/...` is required.

After the install and reboot, SSH as `admin`:

```sh
ssh -i .local/ssh/cloud-admin_ed25519 admin@1.2.3.4
```

Cloud VPS profiles use one GPT disk with an EFI boot partition and ext4 root.
`/data/postgres` and `/data/container-state` are ordinary directories on that
root filesystem for now. They include common virtualized storage and network
drivers, serial console output, a serial getty on `ttyS0`, and persistent
journald logs for rescue-mode diagnostics.

Cloud profiles enable key-only SSH for `admin`, disable root SSH login, and run
fail2ban for the default `sshd` jail. SSH is normally allowed on the Netbird
interface only. If Netbird is unhealthy, `portablevps-break-glass-ssh.timer`
temporarily opens public SSH for configured Dutch source ranges, then closes it
one hour after Netbird recovers. The Dutch source ranges come from a pinned
country list bundled in the repository
(`modules/networking/data/nl.zone`), refreshed from ipdeny.com at most daily,
so opening break-glass access never depends on an external download during an
incident. The NixOS firewall remains the portable host
policy. Provider firewalls should allow the same break-glass SSH sources as an
outer layer, or they will block this host-side fallback.

PostgreSQL binds only on `127.0.0.1` on cloud hosts. When Netbird joins
successfully, the service exposure module exposes PostgreSQL on the Netbird
interface only and opens port 5432 only for that interface.
Netbird registers the server with the logical server's explicit peer name; for
`test-vps` that name is `test-vps`.

## NetBird-First Proxy

Application traffic should not depend on the VPS public IP. Public DNS should
point at NetBird's reverse proxy/custom-domain ingress, and internal-only names
should resolve through NetBird DNS. The server-side proxy module uses Traefik
for hostname and SNI routing on the server itself.

The proxy module is disabled until a server declares routes:

```nix
portablevps.proxy = {
  enable = true;
  acme = {
    email = "ops@example.com";
    dnsProvider = "desec";
    # Use "staging" before first production issuance.
    environment = "production";
  };
  http.services.app = {
    domain = "app.example.com";
    upstream = "http://127.0.0.1:3000";
    visibility = "netbird-edge";
  };
  tcp.services.database = {
    domain = "db.example.com";
    targetHost = "127.0.0.1";
    targetPort = 5432;
    tlsPassthrough = true;
    visibility = "internal";
  };
  dns = {
    managedZones = [ "int.portablevps.io" ];
    acmeDelegatedZone = "acme.portablevps.io";
    publicTarget = "eu1.netbird.services.";
    netbirdCnameTarget = "test-vps.portablevps.int.";
  };
};
```

Traefik listens on port 443 and the host firewall opens that port only on the
NetBird interface by default. Use NetBird Reverse Proxy TLS passthrough for
public names and NetBird Custom Zones for internal names. Certificates are
issued per server/service through DNS-01 ACME using sops-managed provider
credentials at `/etc/portablevps/traefik-acme.env`; wildcard certificates are not
the default.

Keep normal DNS at the domain registrar, currently Mijn.host, but do not put an
unscoped Mijn.host API key on application servers. Instead, delegate an
ACME-only subzone such as `acme.example.com` to deSEC and give Traefik only a
deSEC token for that validation zone:

```sh
DESEC_TOKEN=...
```

For public hostnames that stay in the Mijn.host-managed parent zone, create a
one-time CNAME in Mijn.host that points the ACME challenge name into the
delegated zone:

```dns
_acme-challenge.app.example.com. CNAME _acme-challenge.app.example.com.acme.example.com.
```

For internal names, delegate a whole namespace such as `int.portablevps.io` to
deSEC once and add it to `portablevps.proxy.dns.managedZones`. Those names do not need
per-host ACME CNAMEs; Traefik writes the challenge TXT records directly in the
delegated deSEC zone. Keep NetBird API tokens on the deploy machine and sync
NetBird DNS records from the generated server plan:

```sh
NETBIRD_API_TOKEN=... mise exec -- task cloud:netbird-dns-sync \
  HOST=100.85.5.203 NETBIRD_DNS_ZONE=int.portablevps.io \
  NETBIRD_DNS_GROUP_IDS=<netbird-group-id>
```

When deSEC shows DNSSEC material for the delegated zone, add the child-zone
DNSSEC data at Mijn.host only for the delegated ACME zone. Prefer DS format when
Mijn.host offers it; DNSKEY format is only for provider UIs that compute DS from
DNSKEY. For `portablevps.io`, the parent-zone records are:

```dns
acme.portablevps.io. NS <desec-ns-1>
acme.portablevps.io. NS <desec-ns-2>
acme.portablevps.io. DS <key-tag> <algorithm> <digest-type> <digest>
```

Do not paste deSEC's `acme.portablevps.io` DNSKEY or DS material into a registrar
DNSSEC screen for the apex `portablevps.io` domain. It belongs to the delegated
child zone, not the parent zone itself.

Traefik then writes only TXT records in deSEC during DNS-01 validation. Direct
Mijn.host API use with `dnsProvider = "mijnhost"` is a temporary fallback only,
because the API key cannot currently be scoped narrowly enough for this server
template.

Use `portablevps.proxy.acme.environment = "staging"` for test issuance. Staging and
production use separate Traefik resolver names, `dns01-staging` and
`dns01-production`, so a staging certificate can stay in ACME storage without
blocking production issuance later.

To verify the server-side proxy path before real DNS and ACME are configured,
enable the temporary smoke-test backend on a running host. Use the host's
NetBird IP or NetBird-resolvable name as `HOST`:

```sh
mise exec -- task cloud:proxy-smoke-test SERVER=test-vps HOST=100.85.5.203
```

That command switches the host with a temporary Traefik route for
`proxy-test.portablevps.int` and verifies HTTPS over the NetBird path. The route
stays active as the current NixOS generation until the next normal switch. To
also verify the public path, first create a NetBird Reverse Proxy TLS
passthrough target to the same peer on TCP 443, then pass the public ingress
address:

```sh
mise exec -- task cloud:proxy-smoke-test SERVER=test-vps HOST=100.85.5.203 \
  PUBLIC_DOMAIN=proxy-test.example.com PUBLIC_HOST=203.0.113.10
```

For a destructive install-and-verify run against an existing VPS:

```sh
mise exec -- task cloud:smoke-test SERVER=test-vps HOST=1.2.3.4 \
  ROOT_IDENTITY=.local/ssh/cloud-admin_ed25519 \
  KEXEC_EXTRA_FLAGS=--kexec-syscall \
  CONFIRM_DESTROY=1.2.3.4
```

The cloud install and smoke test disable persistent SSH host-key recording
because the same IP is expected to change host keys between rescue, reinstall,
and the installed system.

Run a separate-host backup and restore rehearsal in two phases. First prepare
data and write a backup on the installed source host:

```sh
mise exec -- task cloud:restore-rehearsal PHASE=prepare \
  SERVER=test-vps SOURCE_HOST=1.2.3.4
```

Then boot a disposable restore host into provider rescue mode and reinstall it
in restore mode from the same S3-compatible restic repository:

```sh
mise exec -- task cloud:restore-candidate \
  SERVER=test-vps SOURCE_HOST=1.2.3.4 RESTORE_HOST=5.6.7.8 \
  ROOT_IDENTITY=.local/ssh/cloud-admin_ed25519 \
  CONFIRM_DESTROY=5.6.7.8
```

The restore phase waits for the rebuilt host, runs `restore.sh`, switches the
host back to the normal server profile, starts `apps.target`, and verifies the
marker created during prepare. The source host keeps running during this
rehearsal. Restore hosts get unique hostname and Netbird peer-name overrides by
default so they do not collide with the source. For a provider migration, keep
the restore candidate on temporary identity until validation, then change the
server's active placement and perform the final cutover deliberately.

After the restored candidate has been validated and the old source is stopped
or otherwise unable to serve traffic, promote the candidate to the stable server
identity:

```sh
mise exec -- task cloud:promote-candidate SERVER=test-vps \
  CANDIDATE_HOST=5.6.7.8 SOURCE_OFFLINE=1 CONFIRM_PROMOTE=test-vps
```

Promotion switches the candidate to `.#test-vps` without restore identity
overrides, starts `apps.target`, and verifies the prepared marker when `MARKER`
or `SOURCE_HOST` is available. It refuses to run until `SOURCE_OFFLINE=1` and
`CONFIRM_PROMOTE=test-vps` are both set, because two hosts with the same
hostname and NetBird identity would conflict.

Promotion also updates local lifecycle state: the promoted host becomes the
`active` record, and a previously recorded active host is re-recorded as
`candidate` so the retired VPS can still be deleted through `cloud:delete`
with its provider server id.

This flow has been validated end to end on Hetzner with Scaleway Object
Storage:

- Installed `.#test-vps` onto a rescue-mode VPS.
- Inserted PostgreSQL marker `recovery-20260624T083053Z-7810`.
- Wrote restic snapshot `35deb21a` to Scaleway S3.
- Rebooted the VPS into rescue mode.
- Reinstalled `.#test-vps-restore` onto the same disk.
- Restored snapshot `35deb21a`.
- Switched the restored host back to `.#test-vps`.
- Started PostgreSQL for validation.
- Verified marker `recovery-20260624T083053Z-7810`.

## QEMU VM Setup

Install QEMU on macOS:

```sh
brew install qemu
```

Install repo tools through mise:

```sh
mise install
```

Common workflows are coordinated through Task:

```sh
mise exec -- task --list
mise exec -- task check
mise exec -- task validate:qemu
mise exec -- task local-s3:start
mise exec -- task dr:test
```

Download a NixOS aarch64 minimal ISO, then create two local VMs:

```sh
scripts/qemu-create-vm.sh vm-a
scripts/qemu-create-vm.sh vm-b
```

Boot VM A into the installer:

```sh
scripts/qemu-boot-vm.sh vm-a \
  --iso /path/to/nixos-minimal-aarch64-linux.iso \
  --ssh-port 2222
```

Boot VM B on a different forwarded SSH port:

```sh
scripts/qemu-boot-vm.sh vm-b \
  --iso /path/to/nixos-minimal-aarch64-linux.iso \
  --ssh-port 2223
```

Each VM has:

- 2 CPUs.
- 2-4 GB RAM.
- Disk 1: 20-40 GB root filesystem.
- Disk 2: 10-20 GB dedicated ZFS data disk.
- SSH enabled.

Install NixOS on disk 1, clone this repository, and apply the config:

```sh
sudo nixos-rebuild switch --flake .#local-vm
```

The default admin user is `admin` with initial password `dev-password`. Change
that before using this outside a local prototype.

PostgreSQL and restic secrets are provided through
`/etc/portablevps/postgres.env` and `/etc/portablevps/restic.env`. Without
`secrets/secrets.yaml`, those files contain prototype defaults. Once an
encrypted sops file exists, `sops-nix` renders those same paths from decrypted
secret values during activation. Cloud profiles do not allow prototype defaults.

After installation, boot without `--iso`:

```sh
scripts/qemu-boot-vm.sh vm-a --ssh-port 2222
scripts/qemu-boot-vm.sh vm-b --ssh-port 2223
```

SSH from the host:

```sh
ssh -p 2222 admin@localhost
ssh -p 2223 admin@localhost
```

## ZFS Data Pool

Find the second disk and create the ZFS pool:

```sh
lsblk
sudo create-zpool.sh /dev/vdb
```

This creates:

- `tank/data` mounted at `/data`.
- `tank/data/postgres` mounted at `/data/postgres`.

The root filesystem is not ZFS in this prototype.

## PostgreSQL

PostgreSQL runs as a Podman container through Quadlet. Its only meaningful state
lives in `/data/postgres`. With the PostgreSQL 18 container image, the host
mount is the PostgreSQL parent directory and the live cluster is stored under
`/data/postgres/18/docker`. The container starts with `summarize_wal=on` because
PostgreSQL 18 incremental base backups require WAL summarization.

The PostgreSQL NixOS module owns its backup and restore behavior. It registers
the physical backup scratch path and ordered backup/restore hooks with the
shared backup orchestrator under `/etc/portablevps/backups/`.

Start apps manually after the first switch if needed:

```sh
sudo systemctl start apps.target
```

Insert and verify a marker:

```sh
sudo insert-test-data.sh
sudo verify-test-data.sh
```

## Monitoring

Every host runs node_exporter on port 9100, reachable only over the NetBird
interface. Monitoring itself is the job of a separate server that scrapes all
peers through NetBird; application servers only expose metrics. The scheduled
backup writes textfile metrics to
`/var/lib/portablevps-metrics/textfile/portablevps-backup.prom`:

- `portablevps_backup_last_run_timestamp_seconds`
- `portablevps_backup_last_success_timestamp_seconds`
- `portablevps_backup_last_run_failed`

Alert on `portablevps_backup_last_run_failed == 1` for immediate failures and on
`time() - portablevps_backup_last_success_timestamp_seconds` exceeding a few
timer intervals for silent staleness, which also catches a dead timer or an
unreachable host.

## Backups

The disaster recovery proof uses a shared S3-compatible restic repository. For
self-contained local testing, start MinIO on the host:

```sh
scripts/local-s3-start.sh
eval "$(scripts/local-s3-env.sh)"
```

With QEMU user networking, the VMs reach the host MinIO API at `10.0.2.2:9000`.
The MinIO container stores object data on the host under `.local/minio/data`.
The local VM profile writes the matching restic backend configuration to
`/etc/portablevps/restic.env` by default:

```sh
RESTIC_REPOSITORY=s3:http://10.0.2.2:9000/portablevps-dr
AWS_ACCESS_KEY_ID=portablevps
```

`backup.sh` runs one coordinated restic snapshot from module-registered backup
components. The PostgreSQL component runs `pg_basebackup` against the live
PostgreSQL 18 server before restic stores the physical backup chain. The first
run is a full base backup; later runs use PostgreSQL incremental base backups
when a previous `backup_manifest` is available. The container-state component
adds `/data/container-state`, which is the shared parent for file state that
containers need backed up.

Containers that need durable file storage should bind-mount their own
subdirectory, for example `/data/container-state/uploads`.

### S3 Repository Layout

Prefer one S3 bucket per environment and one restic repository prefix per
server:

```text
portablevps-backups-prod/
  servers/
    hetzner-primary/
      restic/
    netcup-primary/
      restic/
```

The restic repository URL for that layout is:

```text
s3:https://s3.nl-ams.scw.cloud/portablevps-backups-prod/servers/hetzner-primary/restic
```

A bucket per server is simpler to reason about and easier to delete wholesale,
but it creates more provider objects and policies. A shared bucket with one
prefix per server scales better and keeps policy management centralized. Keep
separate buckets for separate environments such as production, staging, and
local tests.

For Scaleway Object Storage in `nl-ams`, restic needs the endpoint and region:

```text
RESTIC_REPOSITORY=s3:https://s3.nl-ams.scw.cloud/BUCKET/PREFIX
AWS_DEFAULT_REGION=nl-ams
AWS_REGION=nl-ams
```

### Object Lock

Backups should be protected against deletion, but restic still needs to create
and delete operational lock objects under `lock/`. Do not apply default
retention to every object in the live restic repository unless `lock/` can be
excluded.

Recommended live repository policy for each server repository prefix:

- Allow `ListBucket` on the bucket.
- Allow `GetObject` on the repository prefix.
- Allow `PutObject` on the repository prefix.
- Allow `DeleteObject` only on `PREFIX/lock/*`.
- Deny delete for `PREFIX/data/*`, `PREFIX/snapshots/*`, `PREFIX/index/*`,
  `PREFIX/keys/*`, and `PREFIX/config`.

If the provider only supports bucket-wide default object lock, use a two-bucket
design: write restic to an operational bucket where `lock/` can be deleted,
then replicate or copy immutable backup objects to a locked archival bucket.

For a single production instance, start with one production bucket and one
server prefix. Move to one bucket per server only if provider policy boundaries
or operational isolation become easier that way.

### Retention and Verification

`backup.sh` runs hourly. Every backup after the first is a PostgreSQL
incremental base backup until the chain reaches
`portablevps.postgres.backup.maxChainLength` (default 24) entries; the next backup is
then a fresh full that starts a new chain and prunes the old chain from the
local scratch directory. Restore replays one chain with `pg_combinebackup`,
so this bounds both restore time and the damage a single corrupt chain link
can do.

A weekly `portablevps-backup-maintenance` timer applies the restic retention
policy (`portablevps.backups.retention`, default 48 hourly / 14 daily / 8 weekly / 12
monthly, with prune) and runs `restic check` with a small
`--read-data-subset` sample (`portablevps.backups.check.readDataSubset`, default 2%).

Retention conflicts with strict append-only bucket policies: `restic forget
--prune` must delete objects. If the repository bucket denies deletes outside
`lock/`, set `portablevps.backups.retention.enable = false` and rotate repositories
out of band instead.

Stop MinIO when finished:

```sh
scripts/local-s3-stop.sh
```

To start with an empty object store on the next run:

```sh
scripts/local-s3-stop.sh --remove-data
```

## Restore Flow

On the fresh VM, clone this repository and apply restore mode first:

```sh
sudo nixos-rebuild switch --flake .#local-vm-restore
```

Make sure `RESTIC_REPOSITORY` points at the shared S3-compatible repository,
then restore file state and module-owned service state:

```sh
sudo restore.sh
```

`restore.sh` refuses to run unless `portablevps.restoreMode=true` or `apps.target` is
inactive. Restore safety checks, path cleanup, and post-restore reconstruction
come from module-registered restore components. The PostgreSQL component refuses
to restore while the `postgres-demo` container is running, uses
`pg_combinebackup` to reconstruct a synthetic full backup from the
full/incremental chain, then writes that data directory into
`/data/postgres/18/docker` before normal app startup. Restic restores
`/data/container-state` directly from the same snapshot.

After restore, switch to normal mode and verify the database marker:

```sh
sudo nixos-rebuild switch --flake .#local-vm
sudo verify-test-data.sh "your-marker"
```

## Disaster Recovery Test

The automated prototype test is host-orchestrated and uses two manually created
VMs:

```sh
VM_A_SSH=admin@vm-a.local \
VM_B_SSH=admin@vm-b.local \
REMOTE_REPO=/home/admin/portablevps-nix-infra \
tests/test-disaster-recovery.sh
```

With the QEMU port forwards above, use:

```sh
scripts/local-s3-start.sh
eval "$(scripts/local-s3-env.sh)"
VM_A_SSH="-p 2222 admin@localhost" \
VM_B_SSH="-p 2223 admin@localhost" \
REMOTE_REPO=/home/admin/portablevps-nix-infra \
tests/test-disaster-recovery.sh
```

The same flow is available through Task once both VMs are booted and reachable:

```sh
mise exec -- task dr:test
```

For infrastructure changes, run the full local QEMU validation from the host:

```sh
mise exec -- task validate:qemu
```

This waits for both VMs, mounts the host repository at `/host` if needed,
resets local MinIO, creates a full backup plus an incremental backup from VM A,
restores into VM B, and verifies PostgreSQL plus `/data/container-state`.

It proves:

- VM A can insert and back up PostgreSQL state.
- VM B applies restore mode before restore.
- `apps.target` stays inactive during restore.
- `/data/postgres` is non-empty after restore.
- `/data/container-state` files are restored.
- PostgreSQL starts only after switching VM B to normal mode.
- The marker inserted on VM A is present on VM B.

## ZFS Snapshot Rollback

Manual rollback test:

```sh
marker_a="$(sudo insert-test-data.sh)"
snapshot="$(sudo snapshot.sh)"
marker_b="$(sudo insert-test-data.sh)"
sudo rollback-postgres.sh "$snapshot"
sudo verify-test-data.sh "$marker_a"
sudo verify-test-data.sh --absent "$marker_b"
```

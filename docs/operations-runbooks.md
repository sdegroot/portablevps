# Operations Runbooks

These runbooks separate system rollback from data restore. A broken NixOS
generation should normally be handled by booting or switching to an older
generation. A backup restore is for lost servers, lost disks, corrupted data, or
cases where no usable generation can be booted.

## Netbird Registration

Netbird uses the explicit `portablevps.netbird.name` value as the peer hostname. Cloud
servers set that value in `servers/<server>.nix`.

Server names are declared by each consumer flake in its `servers/*.nix` files
and are intentionally stable. A replacement host for the same machine reuses
the same name, NetBird peer, and secret set; a live move with overlap should use
a new machine name and cut service traffic over separately.

Check Netbird on a host:

```sh
systemctl is-active netbird.service netbird-join.service
ip -4 addr show wt0
sudo netbird status
sudo journalctl -u netbird.service -u netbird-join.service -n 200 --no-pager
```

If the host did not register:

1. Verify the encrypted secret `netbird/setup-key` is present in
   `secrets/secrets.yaml`.
2. Rebuild the normal server profile.
3. Restart the daemon and join unit:

   ```sh
   sudo systemctl restart netbird.service netbird-join.service
   ```

4. Confirm `wt0` exists and has an address.

The PostgreSQL Netbird proxy depends on `netbird-join.service`. If Netbird does
not join, PostgreSQL remains bound to localhost and should not be externally
reachable.

## SSH And Firewalls

Cloud hosts use key-only SSH for the `admin` user. Password login and root SSH
login are disabled. Fail2ban is enabled on cloud profiles and watches the
default `sshd` jail through the systemd journal.

SSH is normally open on the Netbird interface only. The
`portablevps-break-glass-ssh.timer` checks Netbird health every minute. If Netbird
is unhealthy, it opens public SSH through a dedicated host-firewall chain for
configured Dutch source ranges. After Netbird recovers, public SSH stays open
for one hour and then closes automatically.

Check SSH protection:

```sh
systemctl is-active sshd.service fail2ban.service
sudo fail2ban-client status sshd
systemctl status portablevps-break-glass-ssh.timer
sudo journalctl -u fail2ban.service -n 100 --no-pager
sudo journalctl -u portablevps-break-glass-ssh.service -n 100 --no-pager
```

The portable firewall policy lives in NixOS. Provider firewalls should be used
as an additional outer layer when available, not as the only policy.

Recommended provider firewall for SSH:

- Allow inbound TCP 22 from the same Dutch source ranges used by the host
  break-glass SSH policy. If the provider firewall blocks TCP 22 from those
  ranges, the VM cannot reopen it from inside the host.
- Allow inbound ICMP for basic reachability diagnostics.
- Do not expose PostgreSQL or application-private ports on the public interface.
- Do not restrict outbound HTTPS or UDP in a way that can break Netbird control
  plane access or peer connectivity.

The default country filter uses the public IPv4 Netherlands list from
`https://www.ipdeny.com/ipblocks/data/countries/nl.zone` and caches it on the
server. If the cache cannot be populated, public break-glass SSH fails closed.
IPv6 public SSH is closed unless explicit IPv6 CIDRs are configured.

## Install Preflight

Run preflight before any install or restore that may wipe a disk:

```sh
mise exec -- task cloud:preflight SERVER=test-vps HOST=1.2.3.4 \
  ROOT_IDENTITY=.local/ssh/cloud-admin_ed25519
```

The command validates the logical server definition, provider metadata, local
admin keypair, local sops age key, Nix flake evaluation, rescue SSH, and target
disk discovery. It does not modify the target host.

## Provider Lifecycle

Lifecycle management is local provider automation. It creates and manages the
concrete VPS; Nix still only defines the installed server. Concrete provider
state is stored in `.local/cloud-state/servers/<server>.json` and should not be
committed.

Hetzner is the first implemented lifecycle provider. Put the API token in a
local env file:

```sh
mkdir -p .local/providers
printf 'HCLOUD_TOKEN=...\n' > .local/providers/hetzner.env
chmod 600 .local/providers/hetzner.env
```

Create and prepare a candidate host:

```sh
mise exec -- task cloud:lifecycle-preflight SERVER=test-vps ROLE=candidate
mise exec -- task cloud:create SERVER=test-vps ROLE=candidate
mise exec -- task cloud:rescue SERVER=test-vps ROLE=candidate
mise exec -- task cloud:install-created SERVER=test-vps ROLE=candidate RESTORE_MODE=1
```

Check or delete a tracked provider server:

```sh
mise exec -- task cloud:status SERVER=test-vps ROLE=candidate
mise exec -- task cloud:delete SERVER=test-vps ROLE=candidate CONFIRM_DELETE=<provider-server-id>
```

Deletion confirmation uses the provider server ID, not the IP address, because
IPs can be reused after a server is destroyed.

## Bad NixOS Upgrade

Use this when a `nixos-rebuild switch`, flake input update, package upgrade, or
configuration change leaves the server unhealthy while the disk and application
data are expected to be intact.

Before a planned upgrade:

```sh
sudo backup.sh
sudo restic snapshots
```

If SSH still works, rollback the system generation:

```sh
sudo nixos-rebuild switch --rollback
sudo systemctl status postgres.service apps.target
```

Reboot if the failed upgrade affected the kernel, bootloader, drivers, or core
networking:

```sh
portablevps server reboot <server>
```

The command waits for a changed kernel boot ID, then requires the booted and
current NixOS generations to match and rejects a host that returns with failed
system services.

If SSH is unavailable but the provider console works, reboot and select the
previous NixOS generation in the bootloader menu. After the machine is reachable,
make that generation active:

```sh
sudo nixos-rebuild switch --rollback
```

If only provider rescue mode is available, first mount the disk and inspect it
instead of restoring data immediately:

```sh
lsblk -f
mount /dev/sda3 /mnt
mount /dev/sda2 /mnt/boot
ls -l /mnt/nix/var/nix/profiles/system-*-link
```

The supported automated path today is reinstalling the host from the flake and
restoring from backup. A pure bad system generation does not require a database
restore, but this repository does not yet provide a supported script for
switching NixOS generations from a generic provider rescue shell.

Use the disaster recovery flow if:

- The previous generation cannot be booted from the provider console.
- The disk layout or bootloader is damaged.
- The host must be moved to a fresh VPS.
- You cannot prove the local data is still trustworthy.

## Bad Application Or Database Migration

Use this when NixOS still boots but an application release or database migration
has corrupted state.

1. Stop application services:

   ```sh
   sudo systemctl stop apps.target
   ```

2. If the issue is only code or configuration, rollback the NixOS generation:

   ```sh
   sudo nixos-rebuild switch --rollback
   sudo systemctl start apps.target
   ```

3. If database contents or durable files are corrupted, use the restore runbook
   with a snapshot from before the bad migration.

Do not overwrite PostgreSQL state until you have identified the snapshot to
restore and accepted the data loss window.

## Zero-Downtime App Deploys (Blue-Green)

For an app with `blueGreen.enable = true` (see `docs/run-your-own-app.md`),
deploy with the CLI so the flip runs and its result gates the deploy:

```sh
portablevps server deploy <server>
```

The switch re-runs an on-box reconcile oneshot that warms the idle colour on the
new image, waits for health, flips, then drains the old colour. Notes:

- **A failed flip fails the deploy** (idle never healthy) and leaves the old
  colour serving. The CLI prints the reconcile log on failure; also check
  `journalctl -u '<app>-bluegreen.service'`. Fix the image and redeploy.
- **Which colour is live** is in `/var/lib/portablevps/<app>/active-color`;
  `sudo podman ps` shows the single running colour (`<app>-blue`/`<app>-green`).
- **To roll back a version**, deploy the previous image tag — the reconcile
  flips back the same way. `nixos-rebuild --rollback` also works but bypasses the
  flip (it restarts the colour in place; expect a brief blip).
- **First cutover** (turning blue-green on) has a one-time blip and leaves the
  old single container; stop it once with `sudo systemctl stop <app>`.

## Automatic Updates (Pull-Based Self-Upgrade)

Opt-in per box via `portablevps.autoUpgrade` (ADR 0003). An enabled box rebuilds
*itself* from the committed flake on `main` on a timer — no operator deploy, no
inbound access to the box, and no deploy key in CI. Currently enabled on the
**website** (`hetzner-fsn-20260710a`, 2-minute poll) and the **demo host**
(`hetzner-fsn-c4r8d160-20260710c`, 10-minute poll).

### How it works (end to end)

1. The app image is a git pin in the server def
   (`portablevps.apps.<app>.image = "ghcr.io/…:<tag>"`) — git is the source of
   truth for what runs, so `nixos-rebuild --rollback` covers infra *and* app.
2. On release, the app repo's CI publishes a new image and the pin on `main` is
   advanced (see "Bumping the pin").
3. Each enabled box runs the `nixos-upgrade` unit on its timer: it fetches the
   flake (`github:you/your-servers`), evaluates
   `#<hostname>`, and `nixos-rebuild switch`es. No change -> no-op; pin moved ->
   the podman restart-on-change step rolls the container. The website uses
   blue-green and health-gated flips; the demo apps currently use restart-based
   rollouts, so expect a brief blip during a demo image bump.
4. Outcome is stamped to `portablevps_deploy_last_{success,failure}_timestamp_
   seconds` (same as an operator deploy) → the `DeployFailing` alert.

Pull, not push: the box needs only *outbound* read access to GitHub (a read-only
PAT); nothing reaches into the box and no operator SSH/age key lands in CI. The
price is up-to-poll-interval latency (2 min for the website, 10 min for demos).

### Enabling it on a box

```nix
portablevps.autoUpgrade = {
  enable = true;
  flake = "github:you/your-servers";
  tokenSecret = "github/repo-token";   # sops key in this box's secrets file
  dates = "*:0/2";                     # poll interval (systemd OnCalendar)
  randomizedDelaySec = "20s";
};
```

Prerequisites:

- **`github/repo-token`** in the box's sops file — a GitHub fine-grained PAT,
  `Contents: Read-only`, scoped to your servers repo. It is handed
  to nix as `access-tokens = github.com=<token>` via a root-only (0400) sops
  EnvironmentFile on `nixos-upgrade`, so it never lands in the world-readable
  nix.conf. Rotate before it expires (≤1 yr); if it lapses, self-upgrade stops and
  the box keeps its last good generation.
- **`main` must hold the live config** — the box tracks `main`. Arm with one
  operator `portablevps server deploy <box>`; the timer self-drives after.

Auto-disabled in restore mode and in prototype/local-VM mode.

### Bumping the pin

portablevps only watches `main` — how the pin actually gets advanced is
entirely up to your own CI. Any mechanism that ends in a commit to `main`
works: a GitHub Actions workflow your app's CI dispatches on release, a
scheduled job that checks a registry for new tags, Renovate/Dependabot, or
just editing the pin by hand and pushing. There's no PR/eval gate needed
either way — a pin bump is a one-line string change that can't break
evaluation, and the real gate is on the box itself: a blue-green app (see
above) won't flip to an image that fails its health check, so a bad tag
leaves the old colour serving; a restart-based app is caught by monitoring
or a manual smoke test instead.

**Manual bump** (no CI needed): edit the relevant image pin, commit, and push
to `main` — the box applies it on its next poll.

### Observing a deploy

- **Metrics/alert:** `portablevps_deploy_last_{success,failure}_timestamp_seconds`
  (per `host_name`); `DeployFailing` fires when the last failure is newer than the
  last success.
- **Logs:** the `nixos-upgrade` journal ships to VictoriaLogs via the OTel
  collector — filter by unit for the rebuild output. (A "Fleet deploys" Grafana
  dashboard over these is a planned follow-up.)
- **On the box:** `systemctl status nixos-upgrade.timer` (next run),
  `journalctl -u nixos-upgrade -f` (live), `nixos-rebuild list-generations`.

### Operating

- **Force a tick now:** `sudo systemctl start nixos-upgrade.service`.
- **Pause** (e.g. during an incident): `sudo systemctl stop nixos-upgrade.timer`
  (transient), or set `autoUpgrade.enable = false` + deploy (durable).
- **Roll back a bad auto-update:** `git revert` the offending commit on `main` and
  push — the next tick converges. For an immediate local fix,
  `sudo nixos-rebuild switch --rollback` on the box, but also revert on `main` or
  the next tick re-pulls it. See "Bad NixOS Upgrade" / "Bad Application Or Database
  Migration".
- **Golden rule:** never `server deploy` **uncommitted** local changes to an
  autoUpgrade box — the next tick reverts them. Commit + push to `main` instead.

### Failure modes

- **Build/eval fails:** the switch never happens (safe); the box keeps running and
  self-corrects when a good commit lands. `DeployFailing` fires.
- **Clean switch, unhealthy app:** on blue-green apps, an unhealthy idle colour
  does not take traffic, but `nixos-upgrade` reports *success*, so watch the
  deploy dashboard/logs, not just `DeployFailing`. On restart-based demo apps, a
  builds-fine but unhealthy image can restart into a bad runtime state; revert the
  pin on `main` or roll back locally, then fix forward.
- **Blast radius:** any `main` commit touching the box's config or a shared
  `portablevps` module auto-rolls to every enabled box within a tick — keep `main`
  deployable.
- **Fetch path severed** (a commit breaks the box's own mesh/DNS, or the token
  lapses): the box can't self-upgrade; recover with an operator `server deploy` or
  break-glass SSH.

## Full Host Loss Or Host Move

Use `docs/disaster-recovery.md` when the host is destroyed, the provider is
being changed, or a fresh server must continue from backup.

The supported cloud flow is:

1. Install the server restore profile.
2. Run `restore.sh`.
3. Switch back to the normal server profile.
4. Start `apps.target`.
5. Verify PostgreSQL state.

The validated Hetzner path restored restic snapshot `35deb21a` and verified
marker `recovery-20260624T083053Z-7810`.

## NetBird-First Application Ingress

Public application DNS should not point at the VPS public IP. Use NetBird as the
stable ingress layer so moving a server only requires changing NetBird routing
targets, not public DNS.

Recommended routing model:

- `netbird-edge` services: public DNS CNAME points at the NetBird Reverse Proxy
  custom domain target. NetBird forwards TLS passthrough over the mesh to this
  server's NetBird peer on TCP 443.
- `internal` services: NetBird Custom Zones resolve service names to the active
  server's NetBird address. Clients connect over the NetBird network.
- `direct-public` services: public DNS points at the VPS public ingress target
  and the host firewall opens the proxy port. Avoid this unless bypassing
  NetBird's edge is intentional.
- The server-local Traefik proxy terminates HTTP TLS or routes TCP SNI
  passthrough to the correct local service.
- ACME uses DNS-01, so certificate issuance is independent from the server's
  public IP and works for internal names as long as the DNS zone is public.

Migration cutover:

1. Restore and validate the new server with a temporary NetBird name.
2. Freeze writes on the old server and take the final backup.
3. Restore the final backup onto the new server.
4. Stop the old source server or remove its network reachability.
5. Promote the candidate to stable identity:

   ```sh
   mise exec -- task cloud:promote-candidate SERVER=test-vps \
     CANDIDATE_HOST=5.6.7.8 SOURCE_OFFLINE=1 CONFIRM_PROMOTE=test-vps
   ```

6. Update NetBird Reverse Proxy targets and NetBird DNS records to the new
   peer/address.
7. Verify public and internal application hostnames.
8. Retire the old peer after validation.

Do not share wildcard certificates across servers by default. Each server should
request certificates only for the names it serves. DNS API credentials used by
Traefik should be scoped to ACME TXT record management.

DNS and ACME provider policy:

- Keep normal public DNS at Mijn.host, but do not place the Mijn.host API key on
  application servers because it cannot currently be scoped to only ACME TXT
  records.
- Delegate `acme.<domain>` from Mijn.host to deSEC with NS records. This is the
  fallback ACME validation zone for public hostnames that are still managed in
  the Mijn.host parent zone.
- Delegate the internal service namespace, such as `int.portablevps.io`, from
  Mijn.host to deSEC with NS records. Traefik can then create DNS-01 challenge
  TXT records directly in that managed zone and no per-internal-host ACME CNAME
  is needed.
- If deSEC shows DNSSEC delegation material, add it at Mijn.host for the child
  zone only. Prefer DS format; use DNSKEY format only if Mijn.host asks for
  DNSKEY and computes DS itself.
- Store the deSEC token in the sops-managed Traefik environment file:

  ```sh
  DESEC_TOKEN=...
  ```

- Configure Traefik with `portablevps.proxy.acme.dnsProvider = "desec";`.
- Declare service domains on the server through `portablevps.proxy.http.services`,
  `portablevps.proxy.tcp.services`, or the temporary `portablevps.proxy.testBackend`. Set
  `visibility = "netbird-edge"` for services reachable through the NetBird
  public reverse proxy, `visibility = "internal"` for services that should only
  resolve for connected NetBird clients, and `visibility = "direct-public"` for
  services that intentionally bypass the NetBird edge. Legacy values
  `visibility = "public"` and `visibility = "vpn"` are accepted as aliases for
  `netbird-edge` and `internal`.
- Configure DNS planning metadata on the server:

  ```nix
  portablevps.proxy.dns = {
    managedZones = [ "int.portablevps.io" ];
    acmeDelegatedZone = "acme.portablevps.io";
    publicTarget = "eu1.netbird.services.";
    publicRecordType = "CNAME";
    netbirdCnameTarget = "test-vps.portablevps.int.";
    netbirdEdgeOverrides = true;
  };
  ```

  `publicTarget` is used for `netbird-edge` and `direct-public` domains.
  For `netbird-edge`, set it to the NetBird Reverse Proxy edge target. For
  `direct-public`, set it to the host's public ingress target and choose the
  matching `publicRecordType`, such as `A` for an IPv4 address or `CNAME` for a
  hostname. `netbirdCnameTarget` is used for `internal` domains and, when
  `netbirdEdgeOverrides = true`, for NetBird split-horizon overrides of
  `netbird-edge` domains.
  `managedZones` lists public DNS zones that are delegated to the Traefik ACME
  provider, so the generated plan marks matching names as direct managed-zone
  ACME instead of CNAME-delegated ACME. `managedZones` is not required for
  NetBird DNS override zones.
- On a configured server, inspect the generated domain registration plan:

  ```sh
  portablevps-proxy-domain-plan
  ```

  The same JSON is available at `/etc/portablevps/proxy-domains.json`.
- For each hostname outside `managedZones`, add a one-time Mijn.host CNAME into
  the delegated ACME zone:

  ```dns
  _acme-challenge.app.example.com. CNAME _acme-challenge.app.example.com.acme.example.com.
  ```

  For names under `managedZones`, such as `test.int.portablevps.io`, do not add an
  ACME challenge CNAME. The delegated deSEC zone is authoritative and Traefik
  writes `_acme-challenge` TXT records there directly.

- For each `visibility = "netbird-edge"` hostname, add public DNS:

  ```dns
  app.example.com. CNAME eu1.netbird.services.
  ```

  If `netbirdEdgeOverrides = true`, also sync the public parent zone into
  NetBird DNS so connected clients resolve the same hostname directly to the
  peer:

  ```sh
  NETBIRD_API_TOKEN=... mise exec -- task cloud:netbird-dns-sync \
    HOST=100.85.5.203 NETBIRD_DNS_ZONE=example.com \
    NETBIRD_DNS_GROUP_IDS=<netbird-group-id>
  ```

- For each `visibility = "direct-public"` hostname, add public DNS to the host
  ingress target and set `portablevps.proxy.openPublicFirewall = true`. The NixOS module
  rejects this when any non-`direct-public` route exists on the same Traefik
  listener, because opening the public firewall exposes every configured
  hostname on that listener.

- For each `visibility = "internal"` hostname, add NetBird private DNS:

  ```dns
  app.example.com. CNAME test-vps.portablevps.int.
  ```

  For `internal`, do not add a public app DNS record. With the default firewall
  policy, Traefik's HTTPS port is allowed only on the NetBird interface.
  Keep NetBird API tokens off servers; sync these records from the deploy
  machine after the server has been switched:

  ```sh
  NETBIRD_API_TOKEN=... mise exec -- task cloud:netbird-dns-sync \
    HOST=100.85.5.203 NETBIRD_DNS_ZONE=int.portablevps.io \
    NETBIRD_DNS_GROUP_IDS=<netbird-group-id>
  ```

  `NETBIRD_DNS_GROUP_IDS` is only required when the NetBird DNS zone does not
  exist yet. After that, the task upserts internal CNAME records from the
  server's generated proxy domain plan.

The `test-vps` server enables a persistent internal proxy smoke route:

```text
test.int.portablevps.io
```

It uses `visibility = "internal"`, so deploy the profile, sync NetBird DNS, and
test it from a NetBird-connected client. While
`portablevps.proxy.acme.environment = "staging"`, the route should work for connectivity
testing but browsers will not trust the certificate. The Hetzner profile waits
before ACME DNS propagation checks so deSEC has time to publish challenge TXT
records on both authoritative nameservers.

- Test new domains against Let's Encrypt staging first:

  ```nix
  portablevps.proxy.acme.environment = "staging";
  ```

Switch to `portablevps.proxy.acme.environment = "production"` for production issuance.
The two environments use separate Traefik resolver names, `dns01-staging` and
`dns01-production`, so staging certificates can remain in ACME storage without
blocking production certificates. Direct `dnsProvider = "mijnhost"` is allowed
only as a short-lived fallback when delegated ACME is unavailable.

For `portablevps.io`, keep the broad public wildcard:

```dns
*.portablevps.io. CNAME eu1.netbird.services.
```

Use explicit public names such as `test.portablevps.io` with
`visibility = "netbird-edge"`. Their application DNS is covered by the wildcard,
but their ACME challenge still needs the fallback CNAME unless the whole
hostname is under a managed delegated zone:

```dns
_acme-challenge.test.portablevps.io. CNAME _acme-challenge.test.portablevps.io.acme.portablevps.io.
```

For internal-only names, use a delegated subzone such as `int.portablevps.io`:

```dns
int.portablevps.io. NS <desec-ns-1>
int.portablevps.io. NS <desec-ns-2>
int.portablevps.io. DS <key-tag> <algorithm> <digest-type> <digest>
```

Do not create public wildcard records below `int.portablevps.io`. Internal records
such as `test.int.portablevps.io` should be created in NetBird DNS by the deploy
task, and ACME TXT records are created in the delegated deSEC zone by Traefik.

For `portablevps.io`, configure the fallback delegated ACME zone in this order:

1. Create `acme.portablevps.io` in deSEC and note its nameservers.
2. In Mijn.host DNS for `portablevps.io`, add the child-zone NS records:

   ```dns
   acme.portablevps.io. NS <desec-ns-1>
   acme.portablevps.io. NS <desec-ns-2>
   ```

3. If Mijn.host supports DS records in the DNS zone editor, add the DS record
   deSEC shows with name `acme.portablevps.io`:

   ```dns
   acme.portablevps.io. DS <key-tag> <algorithm> <digest-type> <digest>
   ```

   If Mijn.host instead asks for DNSKEY for a delegated child zone, paste the
   DNSKEY block from deSEC there and let Mijn.host derive DS. Do not paste the
   deSEC DNSKEY or DS material into a registrar DNSSEC screen for the apex
   `portablevps.io` domain.

4. Add one-time challenge CNAMEs in Mijn.host for service hostnames:

   ```dns
   _acme-challenge.test.portablevps.io. CNAME _acme-challenge.test.portablevps.io.acme.portablevps.io.
   ```

5. Verify delegation and DNSSEC:

   ```sh
   dig NS acme.portablevps.io +short
   dig DS acme.portablevps.io +short
   dig CNAME _acme-challenge.test.portablevps.io +short
   dig +dnssec TXT _acme-challenge.test.portablevps.io
   ```

An empty `dig DS` result means DNSSEC trust is not delegated yet; NS delegation
can still be tested first. A broken DS record is worse than no DS record because
validating resolvers will reject the child zone.

Proxy smoke test:

1. Confirm the server is connected to NetBird and note its NetBird IP or name.
2. Enable and verify the temporary server-side route:

   ```sh
   mise exec -- task cloud:proxy-smoke-test SERVER=test-vps HOST=100.85.5.203
   ```

3. Create a NetBird Reverse Proxy TLS passthrough target to the same peer on
   TCP 443.
4. Verify the public ingress too:

   ```sh
   mise exec -- task cloud:proxy-smoke-test SERVER=test-vps HOST=100.85.5.203 \
     PUBLIC_DOMAIN=proxy-test.example.com PUBLIC_HOST=203.0.113.10
   ```

The first check proves that Traefik, the host firewall, and the NetBird path to
the server work. The second check proves that the external NetBird ingress is
also routing to this server. The smoke-test route remains active as the current
NixOS generation until the next normal switch.

## Backup Failure

Check the backup timer and last service run:

```sh
backup-status.sh
systemctl list-timers portablevps-backup.timer
systemctl status portablevps-backup.service
sudo journalctl -u portablevps-backup.service -n 200 --no-pager
```

Run a manual backup:

```sh
sudo init-backup-repo.sh
sudo backup.sh
```

For S3-compatible repositories:

- `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` must be exported from
  `/etc/portablevps/restic.env`.
- Scaleway requires `AWS_DEFAULT_REGION` and `AWS_REGION` to match the bucket
  region, for example `nl-ams`.
- `lock/*` must be deletable by restic.
- Backup data objects should be protected against deletion.

If the error mentions locked restic objects, fix permissions for the `lock/*`
prefix before retrying. Do not disable object protection for backup data
prefixes to work around lock failures.

## Restore Rehearsal

Use a separate disposable restore host for the real disaster recovery proof.
This catches install, boot, secret, Netbird, firewall, restore-mode, and
PostgreSQL startup failures that a same-host scratch restore cannot.

Prepare from the running source host:

```sh
mise exec -- task cloud:restore-rehearsal PHASE=prepare \
  SERVER=test-vps SOURCE_HOST=1.2.3.4
```

Boot a separate restore host into rescue mode, then restore onto it:

```sh
mise exec -- task cloud:restore-candidate \
  SERVER=test-vps SOURCE_HOST=1.2.3.4 RESTORE_HOST=5.6.7.8 \
  ROOT_IDENTITY=.local/ssh/cloud-admin_ed25519 \
  CONFIRM_DESTROY=5.6.7.8
```

`CONFIRM_DESTROY` must match the restore host. The candidate is installed as the
same stable server identity, so the old source must be offline before the
candidate is promoted or used as the replacement. For a live move with overlap,
provision a new machine identity and perform a service-level cutover instead of
reusing the old machine name.

```sh
mise exec -- task cloud:promote-candidate SERVER=test-vps \
  CANDIDATE_HOST=5.6.7.8 SOURCE_OFFLINE=1 CONFIRM_PROMOTE=test-vps
```

Promotion also updates local lifecycle state: the promoted host becomes the
`active` record and the retired host is re-recorded as `candidate` so it can
still be deleted with `task cloud:delete SERVER=... ROLE=candidate
CONFIRM_DELETE=<provider-server-id>` once traffic has been repointed.

## Service Migration

Use this to replace the host serving a live stateful service with a warm spare.
This is not machine-identity replacement: the target keeps its own hostname,
NetBird peer, sops age key, and secrets file. The service moves because both
server configs point at the same service-keyed backup repository and the service
DNS record is repointed after restore.

Keep a small pool of suitably sized idle machines. Before the maintenance
window, assign one spare the target workload in Git, add every required secret
to that machine's sops file, and make sure its `backupRepository` equals the
source's service-keyed repository. If no spare is suitable, create a new logical
machine, complete its sops flow, and install it with `portablevps server adopt
<target> --restore`; do not attempt that provider work after writes have been
frozen.

Prerequisites:

- The target server config is the prepared spare workload and declares the same
  non-empty `backupRepository` and `serviceKey` as the source service.
- The target server has all required sops secrets. Shared proxy credentials such
  as `traefik/acme-env` must match the working fleet credential when the same
  delegated DNS provider is used.
- The target is reachable over admin SSH and can build its normal and
  `-restore` profiles.
- NetBird DNS credentials are available if the command should repoint internal
  service DNS automatically.

Run:

```sh
portablevps#portablevps -- service migrate new-service-host \
  --source-server old-service-host \
  --source-host old-service-host.example.int \
  --target-host new-service-host.example.int
```

The command performs the cutover in this order:

1. Check the source and target machine IDs are distinct, then switch the target
   to `.#<target>-restore` and prove its applications are stopped.
2. Stop the source backup timer so no later snapshot can race the final one.
3. Run `portablevps-quiesce-writers` on the source. Each workload declares its
   API/worker writer units; PostgreSQL deliberately remains online.
4. Run the real `portablevps-backup.service` and wait for its verified final
   physical backup.
5. Stop the complete source `apps.target`, restore the target, activate its
   normal profile, start `apps.target`, and verify the requested marker.
6. Update the stable NetBird service DNS from the target plan:

   ```sh
   portablevps#portablevps -- network dns-sync new-service-host \
     --host new-service-host.example.int
   ```

   The temporary public edge uses these stable service names rather than a
   machine peer. Reload Traefik there after the DNS update if it has cached the
   prior target, then probe both a mesh client and the public hostname.

If the final backup, restore, or target verification fails, the command stops a
partially started target and restarts the source apps and backup timer. Do not
change DNS before the command succeeds. After the DNS and public probes pass,
commit the old machine back to the `idle` profile and replenish spare capacity.

Important details:

- Certificates are state. `portablevps.proxy` registers Traefik's
  `${services.traefik.dataDir}/acme.json` as a backup component so a restored
  service host can present the existing certificate immediately. DNS-01 issuance
  must still be healthy for future renewals.
- The command refuses to proceed when `SOURCE_SERVER` and `TARGET_SERVER` both
  declare backup repositories and they differ.
- The command is intentionally scoped to already-provisioned warm spares.
  Creating/adopting a machine, wiring sops recipients/secrets, and choosing
  which old machine becomes idle remain explicit operator steps completed
  before the maintenance window.

## Operator Control Plane

The deploy machine is part of the disaster recovery story. Everything under
`.local/` is untracked, and some of it cannot be regenerated:

- `.local/sops/age-key.txt` — decrypts every secret in `secrets/secrets.yaml`.
  Losing it means rotating all secrets and reinstalling hosts. Keep an
  encrypted off-machine copy (password manager or offline medium).
- `.local/ssh/cloud-admin_ed25519` — admin SSH onto all cloud hosts. Losing
  it locks you out of normal SSH; recovery requires provider console/rescue.
  Keep an encrypted off-machine copy.
- `.local/providers/*.env` — provider API tokens. Re-creatable from the
  provider console; store the tokens in a password manager anyway.
- `.local/cloud-state/servers/*.json` — lifecycle records. Mostly
  re-derivable: recreate a record by checking the provider console and
  re-running `task cloud:status`, or re-adopt manually. Losing it is an
  inconvenience, not a disaster.
- `.local/cloud-restore/*.marker` — rehearsal markers, disposable.

Treat the age key and admin SSH key as the control-plane equivalents of the
backup repository: a laptop loss without off-machine copies of those two
files turns a routine host move into a full secrets rotation.

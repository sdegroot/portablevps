# Operations Runbooks

These runbooks separate system rollback from data restore. A broken NixOS
generation should normally be handled by booting or switching to an older
generation. A backup restore is for lost servers, lost disks, corrupted data, or
cases where no usable generation can be booted.

## Netbird Registration

Netbird uses the explicit `my.netbird.name` value as the peer hostname. Cloud
servers set that value in `servers/<server>.nix`.

Current logical server names:

- `test-vps`: Hetzner-backed test server

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
`epistola-break-glass-ssh.timer` checks Netbird health every minute. If Netbird
is unhealthy, it opens public SSH through a dedicated host-firewall chain for
configured Dutch source ranges. After Netbird recovers, public SSH stays open
for one hour and then closes automatically.

Check SSH protection:

```sh
systemctl is-active sshd.service fail2ban.service
sudo fail2ban-client status sshd
systemctl status epistola-break-glass-ssh.timer
sudo journalctl -u fail2ban.service -n 100 --no-pager
sudo journalctl -u epistola-break-glass-ssh.service -n 100 --no-pager
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
sudo reboot
```

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
- Delegate the internal service namespace, such as `int.epistola.io`, from
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

- Configure Traefik with `my.proxy.acme.dnsProvider = "desec";`.
- Declare service domains on the server through `my.proxy.http.services`,
  `my.proxy.tcp.services`, or the temporary `my.proxy.testBackend`. Set
  `visibility = "netbird-edge"` for services reachable through the NetBird
  public reverse proxy, `visibility = "internal"` for services that should only
  resolve for connected NetBird clients, and `visibility = "direct-public"` for
  services that intentionally bypass the NetBird edge. Legacy values
  `visibility = "public"` and `visibility = "vpn"` are accepted as aliases for
  `netbird-edge` and `internal`.
- Configure DNS planning metadata on the server:

  ```nix
  my.proxy.dns = {
    managedZones = [ "int.epistola.io" ];
    acmeDelegatedZone = "acme.epistola.io";
    publicTarget = "eu1.netbird.services.";
    publicRecordType = "CNAME";
    netbirdCnameTarget = "test-vps.epistola.int.";
  };
  ```

  `publicTarget` is used for `netbird-edge` and `direct-public` domains.
  For `netbird-edge`, set it to the NetBird Reverse Proxy edge target. For
  `direct-public`, set it to the host's public ingress target and choose the
  matching `publicRecordType`, such as `A` for an IPv4 address or `CNAME` for a
  hostname. `netbirdCnameTarget` is used only for `internal` domains.
  `managedZones` lists public DNS zones that are delegated to the Traefik ACME
  provider, so the generated plan marks matching names as direct managed-zone
  ACME instead of CNAME-delegated ACME. Do not rely on split DNS by default; use
  explicit separate public and internal names when a service needs both paths.
- On a configured server, inspect the generated domain registration plan:

  ```sh
  epistola-proxy-domain-plan
  ```

  The same JSON is available at `/etc/epistola/proxy-domains.json`.
- For each hostname outside `managedZones`, add a one-time Mijn.host CNAME into
  the delegated ACME zone:

  ```dns
  _acme-challenge.app.example.com. CNAME _acme-challenge.app.example.com.acme.example.com.
  ```

  For names under `managedZones`, such as `test.int.epistola.io`, do not add an
  ACME challenge CNAME. The delegated deSEC zone is authoritative and Traefik
  writes `_acme-challenge` TXT records there directly.

- For each `visibility = "netbird-edge"` hostname, add public DNS:

  ```dns
  app.example.com. CNAME eu1.netbird.services.
  ```

- For each `visibility = "direct-public"` hostname, add public DNS to the host
  ingress target and set `my.proxy.openPublicFirewall = true`. The NixOS module
  rejects this when any non-`direct-public` route exists on the same Traefik
  listener, because opening the public firewall exposes every configured
  hostname on that listener.

- For each `visibility = "internal"` hostname, add NetBird private DNS:

  ```dns
  app.example.com. CNAME test-vps.epistola.int.
  ```

  For `internal`, do not add a public app DNS record. With the default firewall
  policy, Traefik's HTTPS port is allowed only on the NetBird interface.
  Keep NetBird API tokens off servers; sync these records from the deploy
  machine after the server has been switched:

  ```sh
  NETBIRD_API_TOKEN=... mise exec -- task cloud:netbird-dns-sync \
    HOST=100.85.5.203 NETBIRD_DNS_ZONE=int.epistola.io \
    NETBIRD_DNS_GROUP_IDS=<netbird-group-id>
  ```

  `NETBIRD_DNS_GROUP_IDS` is only required when the NetBird DNS zone does not
  exist yet. After that, the task upserts internal CNAME records from the
  server's generated proxy domain plan.

The `test-vps` server enables a persistent internal proxy smoke route:

```text
test.int.epistola.io
```

It uses `visibility = "internal"`, so deploy the profile, sync NetBird DNS, and
test it from a NetBird-connected client. While
`my.proxy.acme.environment = "staging"`, the route should work for connectivity
testing but browsers will not trust the certificate. The Hetzner profile waits
before ACME DNS propagation checks so deSEC has time to publish challenge TXT
records on both authoritative nameservers.

- Test new domains against Let's Encrypt staging first:

  ```nix
  my.proxy.acme.environment = "staging";
  ```

Switch to `my.proxy.acme.environment = "production"` for production issuance.
The two environments use separate Traefik resolver names, `dns01-staging` and
`dns01-production`, so staging certificates can remain in ACME storage without
blocking production certificates. Direct `dnsProvider = "mijnhost"` is allowed
only as a short-lived fallback when delegated ACME is unavailable.

For `epistola.io`, keep the broad public wildcard:

```dns
*.epistola.io. CNAME eu1.netbird.services.
```

Use explicit public names such as `test.epistola.io` with
`visibility = "netbird-edge"`. Their application DNS is covered by the wildcard,
but their ACME challenge still needs the fallback CNAME unless the whole
hostname is under a managed delegated zone:

```dns
_acme-challenge.test.epistola.io. CNAME _acme-challenge.test.epistola.io.acme.epistola.io.
```

For internal-only names, use a delegated subzone such as `int.epistola.io`:

```dns
int.epistola.io. NS <desec-ns-1>
int.epistola.io. NS <desec-ns-2>
int.epistola.io. DS <key-tag> <algorithm> <digest-type> <digest>
```

Do not create public wildcard records below `int.epistola.io`. Internal records
such as `test.int.epistola.io` should be created in NetBird DNS by the deploy
task, and ACME TXT records are created in the delegated deSEC zone by Traefik.

For `epistola.io`, configure the fallback delegated ACME zone in this order:

1. Create `acme.epistola.io` in deSEC and note its nameservers.
2. In Mijn.host DNS for `epistola.io`, add the child-zone NS records:

   ```dns
   acme.epistola.io. NS <desec-ns-1>
   acme.epistola.io. NS <desec-ns-2>
   ```

3. If Mijn.host supports DS records in the DNS zone editor, add the DS record
   deSEC shows with name `acme.epistola.io`:

   ```dns
   acme.epistola.io. DS <key-tag> <algorithm> <digest-type> <digest>
   ```

   If Mijn.host instead asks for DNSKEY for a delegated child zone, paste the
   DNSKEY block from deSEC there and let Mijn.host derive DS. Do not paste the
   deSEC DNSKEY or DS material into a registrar DNSSEC screen for the apex
   `epistola.io` domain.

4. Add one-time challenge CNAMEs in Mijn.host for service hostnames:

   ```dns
   _acme-challenge.test.epistola.io. CNAME _acme-challenge.test.epistola.io.acme.epistola.io.
   ```

5. Verify delegation and DNSSEC:

   ```sh
   dig NS acme.epistola.io +short
   dig DS acme.epistola.io +short
   dig CNAME _acme-challenge.test.epistola.io +short
   dig +dnssec TXT _acme-challenge.test.epistola.io
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
systemctl list-timers epistola-backup.timer
systemctl status epistola-backup.service
sudo journalctl -u epistola-backup.service -n 200 --no-pager
```

Run a manual backup:

```sh
sudo init-backup-repo.sh
sudo backup.sh
```

For S3-compatible repositories:

- `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` must be exported from
  `/etc/epistola/restic.env`.
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

`CONFIRM_DESTROY` must match the restore host. The source host should remain
running during rehearsal. The restore host uses temporary identity until it is
promoted.

```sh
mise exec -- task cloud:promote-candidate SERVER=test-vps \
  CANDIDATE_HOST=5.6.7.8 SOURCE_OFFLINE=1 CONFIRM_PROMOTE=test-vps
```

Promotion also updates local lifecycle state: the promoted host becomes the
`active` record and the retired host is re-recorded as `candidate` so it can
still be deleted with `task cloud:delete SERVER=... ROLE=candidate
CONFIRM_DELETE=<provider-server-id>` once traffic has been repointed.

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

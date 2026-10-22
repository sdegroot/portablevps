# ADR 0006: runtime security: prevent, detect, and maintenance windows (edge pilot)

- Status: Proposed
- Date: 2026-10-22
- Deciders: portablevps maintainers

Facts are marked **[V]** when they were checked on 2026-10-22 against the
running fleet, the fleet's nixpkgs (`nixos-26.05`, `da6dc0e`) or a local test.
**[A]** marks an assumption the pilot has to confirm before anything depends on
it.

## Context

Every host has a narrow job. The public edge (`hetzner-fsn-c2r4d80-20260710b`)
is a Traefik SNI-passthrough proxy. The others run one app behind Traefik, or a
CI runner. Anything outside that job should be impossible, or at least raise an
alert, unless an operator has said up front that they are doing maintenance.

Today:

- **No exec auditing.** `auditd` is not running, and there are no kernel audit
  records in any journal [V]. VictoriaLogs has no `_TRANSPORT=audit` lines in the
  last 24 h; the transports present are `journal`, `stdout`, `syslog` and
  `kernel` [V].
- **Light sandboxing.** `systemd-analyze security` on the edge [V]:

  | Unit | Score | Rating |
  | --- | --- | --- |
  | traefik.service | 5.7 | MEDIUM |
  | netbird.service | 9.6 | UNSAFE |
  | sshd.service | 9.6 | UNSAFE |
  | opentelemetry-collector.service | 8.9 | EXPOSED |

  Traefik already runs as its own user with only `cap_net_bind_service`,
  `NoNewPrivileges`, `ProtectSystem=full`, `PrivateTmp`, `PrivateDevices`,
  `ProtectHome` and `LimitNPROC=64`. That is the stock nixpkgs unit, which
  portablevps does not override [V]. It has no syscall filter and no exec
  restriction.
- **Detection plumbing already exists:**
  - every host's `otelcol-contrib` ships journald to VictoriaLogs on the
    monitoring box;
  - `vmalert-logs` evaluates `rules-vlogs/*.yml` and can remote-write
    recording rules into VictoriaMetrics (`-remoteWrite.url` is set) [V];
  - Alertmanager 0.32.1 sends email [V].
- **Alertmanager has no silences today** [V], and its API is loopback-only by
  design. It is published on `127.0.0.1:9093`, the proxy routes only Grafana
  and the OTLP CNAME, and the server comment says the raw UIs have no login of
  their own [V].

## Threat model (this fleet)

In scope:

1. **T1: remote code execution in an internet-facing daemon.** Traefik on the
   edge, Traefik on the ACME hosts, the app containers. The attacker's next step
   is to exec something: a shell, curl, a dropped binary.
2. **T2: a stolen operator credential.** A per-server admin key (1Password), or
   the operator laptop itself. That gives a valid `admin` SSH session on the
   mesh at a time nobody planned one. Today these logins are logged
   (`sshd-session`, `LogLevel VERBOSE`, with the key fingerprint [V]) but nobody
   is alerted.
3. **T3: persistence.**
   - A line in `~admin/.ssh/authorized_keys`: sshd reads
     `%h/.ssh/authorized_keys` as well as `/etc/ssh/authorized_keys.d/%u` [V],
     and the first is writable by the user.
   - A new system generation outside a deploy.
   - A binary in a writable directory.
4. **T4: blinding the detection.** Stopping otelcol, flushing the audit rules,
   or writing logs under another host's name. `host.name` is stamped by the
   sender's own collector [V], so it is not authenticated.

Out of scope here:

- **Kernel exploits and rootkits.** Root on the host can blind any host-side
  sensor. The counter is shipping off-host promptly plus alerting on silence.
- **Supply chain** (flake inputs, images).
- **Compromise of the NetBird control plane or of the monitoring box.**
- **Authenticating OTLP senders**: per-host credentials or mTLS at the gateway.
  That belongs in a separate ADR.

## Decision (proposed)

Three layers plus maintenance windows, piloted on the edge.

### Layer 1: make exec impossible where a service never needs it

**Do not use `SystemCallFilter=~execve`.** systemd installs the seccomp filter
before it execs the service binary itself, so the unit dies at start with
`status=31/SYS`. This was reproduced with `systemd-run -p SystemCallFilter=~execve
/bin/true` (systemd 252, local container) [V]. No nixpkgs module denies execve
this way [V].

**Use mount-level exec restriction instead:** `NoExecPaths=/` plus
`ExecPaths=<only what the unit runs>`. Tested locally with the same harness [V]:

- the service binary runs and can re-exec itself;
- any other binary fails with `Permission denied` (exit 126);
- a dynamically linked binary also needs its loader and library directories in
  `ExecPaths`.

Traefik is a static binary ("not a dynamic executable") [V], so its package
directory is enough.

Sketch for `modules/networking/proxy.nix`, behind a new
`portablevps.proxy.hardening.enable` (default `false` until the pilot is done):

```nix
systemd.services.traefik.serviceConfig = {
  NoExecPaths = [ "/" ];
  ExecPaths = [ "${config.services.traefik.package}" ];  # static binary
  MemoryDenyWriteExecute = true;        # Go does not JIT [A: verify start + load]
  SystemCallFilter = [ "@system-service" "~memfd_create" ];  # no fileless exec via memfd [A]
  SystemCallArchitectures = "native";
  RestrictAddressFamilies = [ "AF_INET" "AF_INET6" "AF_UNIX" "AF_NETLINK" ];  # [A: drop AF_NETLINK if unused]
  ProtectSystem = lib.mkForce "strict"; # ReadWritePaths = dataDir already set
  ProtectKernelTunables = true; ProtectKernelModules = true; ProtectKernelLogs = true;
  ProtectControlGroups = true; ProtectClock = true; ProtectHostname = true;
  RestrictNamespaces = true; RestrictRealtime = true; RestrictSUIDSGID = true;
  LockPersonality = true; PrivateUsers = false;  # needs the real traefik uid for :443 [A]
};
```

What the hardening touches:

- **The edge** has no ACME and no plugins. Its static config is one entrypoint,
  the file provider and a log section [V], so nothing in it execs.
- **Every other proxy host** runs ACME DNS-01 through lego's `desec` provider.
  It is an HTTP API client [A: confirm there is no `os/exec` on that path; lego
  also ships an `exec` provider that we don't configure]. These hosts also get
  an `ExecStartPre` shell script that runs `envsubst` to render the ACME secret
  into the config [V: nixpkgs `traefik.nix`, set when `environmentFiles` is
  non-empty]. `NoExecPaths` would block it. Either prefix it with `+` (run
  outside the sandbox) or move it into its own oneshot unit.

**Blast radius:** `proxy.nix` is imported by the `web-app`, `single-instance-app`,
`monitoring-server` and `discourse-app` profiles [V]. That covers every host
with a public or internal HTTP service. Hence the opt-in flag, edge first.

Other services:

- **otel collector.** Runs as root by design: portablevps forces `User=root`,
  `DynamicUser=false` for the podman socket and the journal [V]. It execs
  `journalctl --utc --output=json --follow --lines=0 --priority info` as a child
  [V]. Phase 2: `ExecPaths=/nix/store` + `NoExecPaths=/`, the nixpkgs pattern
  (jackett, meme-bingo-web) [V]. That blocks binaries dropped in writable
  directories, but not reuse of binaries already in the store. Also measure
  whether it can drop to a non-root user with `SupplementaryGroups`.
- **NetBird, sshd, fail2ban** exec by nature:
  - sshd starts sessions [V];
  - fail2ban's ban actions call nft or iptables [A];
  - NetBird is root with `NET_ADMIN`. Its own SSH server is disabled [V].

  No exec sandbox for these. Layer 2 covers them.

### Layer 2: record every exec that matters (kernel audit)

Use the NixOS modules from the fleet's nixpkgs [V]:

- `security.audit.enable`: true or `"lock"`. It adds `audit=1` and
  `audit_backlog_limit` to the kernel command line and loads `rules` through
  `audit-rules-nixos.service`.
- `security.auditd.enable`, which defaults `security.audit.enable` to true.

It ships audit plugins for `syslog`, `filter` (audisp-filter → audisp-syslog)
and `af_unix`, all inactive until `active = true`.

**Pipeline: auditd's syslog plugin → journald → the existing otel receiver.**
The receiver runs `journalctl … --priority info` [V]. `audisp-syslog` logs at
`LOG_INFO` [V: module args], so audit lines pass the filter with no collector
change. `systemd-journald-audit.socket` is also active on every host [V], so
journald will receive the raw kernel records too.

- [A] Whether those raw records carry a PRIORITY. If they do, mask the socket so
  every line doesn't ship twice. If they don't, the receiver drops them.

Rules (sketch, `portablevps.security.audit.*`; validate the syntax with
`auditctl -R` on the pilot host before relying on it [A]):

```text
# T2: every program run in a login session. pam_loginuid gives an SSH session
# auid=1000 (admin) [V: /proc/self/loginuid]. Services, timers and autoUpgrade
# have auid unset, so they don't match.
-a always,exit -F arch=b64 -S execve,execveat -F auid>=1000 -F auid!=unset -k pvps-session
-a always,exit -F arch=b32 -S execve,execveat -F auid>=1000 -F auid!=unset -k pvps-session
# T1 backstop: service accounts that must never exec (edge: traefik, uid 995 [V])
-a always,exit -F arch=b64 -S execve,execveat -F uid=traefik -k pvps-noexec   # name→uid [A]
# T3: persistence by a session user
-a always,exit -F dir=/home/admin/.ssh -F perm=wa -F auid!=unset -k pvps-persist
-a always,exit -F dir=/root/.ssh -F perm=wa -F auid!=unset -k pvps-persist
-a always,exit -F dir=/nix/var/nix/profiles -F perm=wa -F auid!=unset -k pvps-generation
# Edge pilot only, observe mode: every exec, to learn the real allowlist
-a always,exit -F arch=b64 -S execve,execveat -k pvps-exec
```

A seccomp or `NoExecPaths` denial in a Layer-1 unit then shows up too: audit
`SECCOMP` records, or an `EXECVE` that failed with `exit=-13`. The kernel logs
the seccomp actions `kill_process kill_thread trap errno user_notif trace log`
[V: `/proc/sys/kernel/seccomp/actions_logged`].

**Expected volume:**

- **Edge today:** about 14k log lines a day [V]. The website ships 83k, the
  busiest host [V].
- **Session rule:** SSH logins run at 2 to 55 a week per host [V], so the rule
  is negligible.
- **Edge full-exec observe rule:** dominated by
  `portablevps-break-glass-ssh.timer`, which fires every minute [V]. Its script
  runs a handful of store binaries per tick (iptables, ip6tables, ipset, ip,
  systemctl, date, find, grep, curl) [V], and each exec produces several audit
  records. Estimate +50k to +90k lines a day on the edge [A: measure in week 1].
  Fine for the pilot, not for the fleet.
- **Podman hosts** (healthcheck execs every 30 s per container) and **runners**
  (CI) never get the full-exec rule. They get the session, service-account and
  persistence rules only.

**Allowlist expression.** In the logs the exe is a store path such as
`/nix/store/<32 chars>-iptables-1.8.13/bin/xtables-nft-multi`. Match the package
name, not the hash, so deploys don't break the allowlist:

```text
exe:~"^/nix/store/[a-z0-9]{32}-(bash|coreutils|iptables|ipset|iproute2|systemd|findutils|gnugrep|curl|logrotate|fstrim|util-linux)-"
```

Only root, through nix-daemon, can create store paths. That case is covered by
the `pvps-generation` watch, plus "new exe not seen before" during review.

Phase 2 replaces the name match with a host-side check: is this exe inside the
closure of `/run/current-system`? It has to run on the host, because the
monitoring box doesn't know each host's closure.

**Tamper resistance:**

- An audit heartbeat timer, `auditctl -m "pvps-audit-heartbeat"` every 5 min,
  proves the whole kernel → auditd → syslog → journald → otel → VictoriaLogs
  path [A: USER messages reach the syslog plugin].
- After the pilot, consider `security.audit.enable = "lock"` (`-e 2`). Rules can
  then only change at a reboot. A rule change on deploy makes
  `audit-rules-nixos` fail, which shows up as a failed switch. See the open
  questions.

### Alerts (vmalert-logs, `apps/monitoring/rules-vlogs/runtime-security.yml`)

These follow the existing format [V: `authentik.yml`, `app-logs.yml`]:
`type: vlogs`, LogsQL with `stats by (host.name)`, and `rename host.name as
host_name`. Each alert carries `maintenance: silenceable|never`. Only
`silenceable` ones are muted by a maintenance window.

```yaml
groups:
  - name: runtime-security
    type: vlogs
    interval: 1m
    rules:
      - alert: SSHLogin                      # T2; identifier verified: sshd-session
        expr: |
          _time:5m SYSLOG_IDENTIFIER:sshd-session "Accepted publickey"
          | stats by (host.name) count() as logins
          | rename host.name as host_name
        labels: { severity: warning, maintenance: silenceable }
      - alert: ServiceAccountExec            # T1; never legitimate
        expr: |
          _time:5m "key=\"pvps-noexec\""
          | stats by (host.name) count() as execs | rename host.name as host_name
        labels: { severity: critical, maintenance: never }
      - alert: PersistenceChange             # T3
        expr: |
          _time:5m ("key=\"pvps-persist\"" OR "key=\"pvps-generation\"")
          | stats by (host.name) count() as changes | rename host.name as host_name
        labels: { severity: critical, maintenance: silenceable }
      - alert: EdgeUnexpectedExec            # pilot; regex from the observe week
        expr: |
          _time:5m host.name:"<edge>" "key=\"pvps-exec\"" -exe:~"<allowlist>"
          | stats by (host.name, exe) count() as n | rename host.name as host_name
        labels: { severity: critical, maintenance: silenceable }
```

**Log silence (T4).** No log-presence alert exists today; only metrics have one
(`NodeMetricsMissing`, absent() per host) [V]. Generate one rule per host from
`monitoredHosts`, the same way `fleetPresenceFile` is generated. LogsQL
`stats count()` over an empty selection returns a row with `0` [V: queried
`host.name:"no-such-host"` → `{"n":"0"}`], so a plain vlogs rule works:

```text
_time:10m host.name:"<host>" | stats count() as n | filter n:=0          # HostLogsMissing
_time:15m host.name:"<host>" "pvps-audit-heartbeat" | stats count() as n | filter n:=0  # AuditHeartbeatMissing
```

### Maintenance mode: off-host silences, opened by pvps

A flag on the host is worthless against T1 to T3, because root can set it. A
silence in Alertmanager lives on the monitoring box and records who, why and
until when.

```text
pvps maintenance start  <server> --for 30m --reason "kernel reboot"   # prints silence id + endsAt
pvps maintenance extend <server> --for 15m
pvps maintenance stop   <server>                                      # expires pvps-created silences for it
pvps maintenance status [<server>]                                    # active windows: who, why, until
```

**The silence:**

```json
{
  "matchers": [
    {"name": "host_name",   "value": "<server>",    "isRegex": false, "isEqual": true},
    {"name": "maintenance", "value": "silenceable", "isRegex": false, "isEqual": true}
  ],
  "startsAt": "<now>",
  "endsAt": "<now + for>",
  "createdBy": "pvps/<os-user>@<laptop>",
  "comment": "<reason>"
}
```

API calls (Alertmanager v2):

- `POST /api/v2/silences` creates one.
- `DELETE /api/v2/silence/{id}` expires it.
- `GET /api/v2/silences?filter=host_name="<server>"` lists them. GET answered
  `[]` [V]; POST and DELETE were not exercised, in keeping with read-only
  verification.

**Transport and auth.** Alertmanager stays loopback-only. `pvps maintenance`
reaches it over SSH to the monitoring box with a dedicated restricted key, in
`authorized_keys` as `restrict,command="portablevps-silence"`, escrowed at
`op://<vault>/<monitoring>-maintenance`. That key can only create, extend,
expire and list silences. It never gets a shell.

Its logins are recognisable by fingerprint (VERBOSE logs it [V]), so `SSHLogin`
on the monitoring box excludes exactly that fingerprint. Otherwise opening a
window would itself page.

**Automatic windows.** These commands open a window before their first SSH to
the target:

| Command | Window |
| --- | --- |
| `pvps server deploy` | 20 min |
| `pvps server reboot` | 10 min |
| `pvps backup run` | 10 min |
| `pvps ssh` | 30 min, interactive |

They expire the window on success. On a crash, `endsAt` caps it. Ordering
matters: alerts fired before the silence existed are still delivered, so the
silence comes first.

**Unattended autoUpgrade** runs as a systemd service with auid unset. It matches
neither the session rules nor the `auid!=unset` persistence rules, so it needs
no window.

- **Residual risk:** a root RCE (auid unset) that writes a generation is missed
  by Layer 2. It shows up through the existing deploy-outcome metrics
  (`DeployFailing`) only if it goes through `nixos-rebuild`.

### Optional, recommended later: gating port 22 per window

`pvps maintenance` could also flip NetBird's `operators-ssh` policy, so port 22
is closed to everyone outside a window. The building blocks exist:

- the NetBird client has `SetPolicyEnabled` [V];
- **but** `policy-sync` treats a disabled policy as drift and would re-enable it
  [V: `cli/internal/netbird/mesh.go`, the `!p.Enabled` check]. A gated policy
  needs a marker so `policy-sync` leaves its `enabled` flag alone.

Lockout analysis:

- **Break-glass doesn't cover it.** Break-glass opens a public, country-filtered
  port 22 only while the mesh itself is unhealthy [V: the script's
  unhealthy-since/open state]. A closed gate on a healthy mesh plus a broken
  toggle means a lockout until someone re-enables the policy in the NetBird
  console.
- **The monitoring box must stay ungated**, because opening a window goes
  through it.
- **Deploys and reboots must open the gate first.** autoUpgrade is unaffected.

**Recommendation:** defer until detection has run for a month.

### Later: runtime enforcement

Tetragon 1.6.0 is in nixpkgs, but there is no NixOS module [V]. Its eBPF
policies can kill a process on a policy match [A: upstream docs], so it would
turn Layer 2 into enforcement. Falco is not in nixpkgs; only `falcoctl` is [V].

Revisit Tetragon after Layers 1 to 3 have run for a while. Before that, the
cost is a module we maintain plus a second policy language.

## Rollout

0. **Approve this design.**
1. **Edge pilot (1 to 2 weeks):**
   - `proxy.hardening.enable` on the edge;
   - audit in observe mode with all rules, including full exec;
   - `HostLogsMissing` and `AuditHeartbeatMissing` fleet-wide (cheap and
     independent);
   - `pvps maintenance` with automatic windows;
   - alerts by email.

   At the end, derive the edge allowlist and confirm every [A] above.
2. **All proxy hosts:**
   - Traefik hardening, with the ACME `ExecStartPre` handled;
   - the session, service-account and persistence rules fleet-wide;
   - `SSHLogin` fleet-wide.
3. **Podman hosts:** measure the healthcheck exec volume. Harden containers
   with `no-new-privileges` and a read-only rootfs where the app allows. The
   otel collector goes to `ExecPaths=/nix/store`.
4. **Runners last:** session and persistence rules only. CI execs anything.
5. **Lock the audit rules; reassess Tetragon.**

## Noise and false positives

- **Services, timers, healthchecks, autoUpgrade:** auid unset, so they never
  match session or persistence rules.
- **Operator work:** covered by the automatic windows. Operators reach hosts
  only through `pvps ssh`, which opens a window.
- **Deploys change store hashes:** the allowlists match package names.
- **One host, many alerts:** Alertmanager groups by host. The existing
  `alertRepeatInterval` (4h) applies.
- **Work outlasting its window:** `pvps maintenance extend`. Never an
  open-ended silence; `--for` is required and capped (proposed: 4 h).

## Open questions

1. **Email only?** Is email (the only receiver today [V]) enough for
   `critical` intrusion alerts, or should they go to a push channel?
2. **Who may open windows?** Only holders of the restricted monitoring key,
   i.e. operators. Is that right?
3. **Lock the audit rules (`-e 2`)?** Rule changes would then need a reboot.
4. **Port-22 gating:** wanted at all, given the lockout analysis?
5. **Audit-line retention:** same as other logs, or longer? VictoriaLogs has
   one `retention.logs` today.
6. **Pilot host:** the edge is marked TEMP (NetBird edge replacement). Pilot
   there anyway, or on authentik?
7. **OTLP sender authentication** (forged `host.name`): a separate ADR?

## Consequences

- **What it stops and what it shows:** a Traefik RCE can no longer start a
  shell. A stolen key or an unplanned session alerts within about a minute, and
  every program run in a session is on record off-host.
- **New parts to maintain:** an audit module and rules, a vlogs rules file, a
  restricted key on the monitoring box, and a new CLI command group.
- **More log volume:** small fleet-wide (session rules), noticeable on the edge
  during the pilot (full exec).
- **What it doesn't stop:** kernel-level compromise, and root-level activity
  that never execs through the audited paths. These stay residual risks. The
  mitigations are prompt shipping, the silence alerts and reinstallable hosts.

# Run your own app

You do not need to write a NixOS module to host your own container. Declare it
under `portablevps.apps.custom.<name>` in your server definition and portablevps
builds the Quadlet unit, wires restore-mode gating and secrets, and — for a
stateful app — registers a backup component so its data is part of the same
tested restic snapshot as everything else.

## Stateless app

```nix
portablevps.apps.custom.web = {
  enable = true;
  image = "docker.io/library/nginx:1.27.3"; # pin an immutable tag or digest
  port = 80;                                 # loopback port the app listens on
};

portablevps.proxy = {
  enable = true;
  acme = { email = "ops@example.com"; dnsProvider = "desec"; };
  http.services.web = {
    domain = "app.example.com";
    upstream = "http://127.0.0.1:80";
    visibility = "internal";                 # or "netbird-edge" for public
  };
};
```

## Stateful app (backed up automatically)

Add `volumes`. Keep host paths under `/data` (the machine's state). Every volume
with `backup = true` (the default) is created on the host and included in the
coordinated backup, cleared and restored in order during a restore — the same
machinery the built-in apps use, so your data survives a host move.

```nix
portablevps.apps.custom.ghost = {
  enable = true;
  image = "docker.io/library/ghost:5.109.5";
  port = 2368;
  uid = 1000;                                # the uid the image runs as
  volumes = [
    { hostPath = "/data/ghost"; containerPath = "/var/lib/ghost/content"; }
  ];
  env = { url = "https://blog.example.com"; };
  secretEnv = {                              # env var <- sops key
    database__connection__password = "ghost/db-password";
  };
};
```

`secretEnv` maps an environment variable to a key in your server's sops file;
portablevps declares the secret and renders a `0400` env file the container
reads. Under prototype/local-VM secrets (for disaster-recovery testing) the
values are placeholders so the container still boots.

## Give the app its own database role

`portablevps.postgres.user` names the role the container image creates on first
boot, and that role is the cluster **superuser** — it can read every database,
create roles, bypass row-level security and open a replication stream. Backups
need it (`pg_basebackup` requires `REPLICATION`); applications do not. Declare an
app role and point the application at that instead:

```nix
portablevps.postgres = {
  database = "website";
  user = "website_admin";                          # superuser: admin + backups
  appRoles.website.passwordSecret = "website/db-password";
};

portablevps.apps.custom.website.secretEnv.PGPASSWORD = "website/db-password";
```

The role is held to `NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION
NOBYPASSRLS`, gets `CONNECT`/`TEMPORARY` on its one database with `PUBLIC`
revoked, and owns its schema (`public` by default, or set `schema` to keep it in
its own namespace). Owning the schema is what lets a migration tool create and
alter its own tables with no cluster-wide privilege.

Roles are reconciled on every activation rather than seeded once through
`/docker-entrypoint-initdb.d`: an init script only runs on an empty cluster, so a
rotated password — or a host rebuilt with `restore.sh` — would never converge.
The password is read from its secret file at activation and handed to `psql`
through the environment, so it stays out of both the process list and the
world-readable store.

One consequence: a non-superuser cannot `CREATE EXTENSION`. Declare what the app
needs in `portablevps.postgres.extensions` instead of in a migration.

### Database metrics

```nix
portablevps.postgres.metrics.enable = true;   # needs portablevps.telemetry.endpoint
```

The host's OpenTelemetry collector then scrapes the cluster's statistics views
and ships them with the rest of the host's telemetry. The series are
`postgresql_*`, e.g. `postgresql_backends_ratio`, `postgresql_commits_total`,
`postgresql_blks_hit_total`, `postgresql_db_size_bytes`,
`postgresql_table_size_bytes`, `postgresql_rows_ratio{state="dead"}` and
`postgresql_deadlocks_total`. They carry the host's `service_name`, so a
dashboard can select them by application.
The built-in **Role — PostgreSQL** dashboard (`/d/pvps-postgres?var-service=<name>`)
shows them, with the postgres container's log.

It connects as `portablevps_monitor`, a role holding only `pg_monitor` and
`CONNECT` on the cluster's databases, capped at 10 connections. It can read
statistics and sizes, not a single row of application data. Its password is
generated on the box into `/var/lib/portablevps/postgres-monitor.env` (root
only) and re-applied on every activation, so a restored or rebuilt host
converges without anything in sops.

### Application metrics

An application that serves Prometheus metrics on loopback is read the same way:

```nix
portablevps.telemetry.prometheusTargets.my-app = {
  target = "127.0.0.1:9100";            # loopback only; anything else fails the build
  metricsPath = "/actuator/prometheus"; # default /metrics
};
```

The host's collector reads it every `portablevps.telemetry.scrapeInterval` and
pushes the series with the rest of the host's telemetry, so they carry the host's
`host_name` and `service_name` and the endpoint needs no port on the mesh. The
name is the scrape job; `traefik` is the proxy's own. Names travel Prometheus →
OTLP → VictoriaMetrics, so check the series after the first push
(`{__name__=~"my_app_.*"}`).

A Spring Boot application's Micrometer endpoint (`/actuator/prometheus`) fills
the built-in **Role — JVM / Spring Boot** dashboard
(`/d/pvps-jvm?var-service=<name>`) with no further work. Its HTTP latency
percentiles need histogram buckets, which Spring publishes only with
`management.metrics.distribution.percentiles-histogram.http.server.requests=true`;
without them the dashboard shows the mean and maximum. Traffic through the
host's proxy is on **Role — Traefik** (`/d/pvps-traefik`).

### Your own alerts

The monitoring box loads consumer rule files next to the built-in ones:

```nix
portablevps.apps.monitoring.extraRules.my-app = ./alerts/my-app.yml;
```

Each is a vmalert rule file (MetricsQL over VictoriaMetrics) and is mounted as
`extra-<name>.yml`; alerts go through the same Alertmanager and e-mail. Every
rule file is parsed with `vmalert -dryRun` when the configuration is built, so a
broken rule fails the build instead of stopping vmalert, and with it every alert.
The collector pushes an `up` series per target, but it stops with the
collector, so alert on an application going quiet with `absent_over_time(...)`
over one of its own series. `vmalert-tool unittest`
tests a rule file against series you write down.

### Your own dashboards

The monitoring box provisions consumer dashboards next to the built-in fleet
and role ones:

```nix
portablevps.apps.monitoring.grafana.extraDashboards.website = ./dashboards/website.json;
```

The name becomes the file name and must not collide with a built-in dashboard.
Each file needs its own stable `uid`; the build fails on invalid JSON or a
missing uid. Like the built-ins they are read-only in the UI: change the file.

## Private registry

```nix
portablevps.apps.custom.internal = {
  enable = true;
  image = "ghcr.io/you/internal-app:1.2.3";
  pullAuthUser = "your-bot-account";         # an identifier, not a secret
  pullAuthSecret = "internal/registry-token"; # sops key: ONLY the token
};
```

For ghcr.io the token must be a **classic** PAT with `read:packages` (fine-grained
tokens have no packages permission).

## Zero-downtime deploys (blue-green)

A normal deploy of a single-container app stops the container, pulls the new
image (which can take minutes), then starts it — so the proxy returns `502` the
whole time. Blue-green removes that: it runs **two colour slots** (`port+1` and
`port+2`) but keeps **only one running** in steady state, and on an image change
it warms the idle colour on the new image, waits until it is healthy, flips
traffic, and drains the old colour — no dropped request.

Turn it on with `blueGreen.enable = true` — on a `custom` app or the first-party
`website` app:

```nix
portablevps.apps.custom.web = {
  enable = true;
  image = "ghcr.io/you/web:1.4.0";
  port  = 8080;                      # colours run on 8081 / 8082
  blueGreen.enable = true;           # ← zero-downtime deploys
};

portablevps.proxy.http.services.web = {
  domain = "app.example.com";        # upstreams + healthCheck come from the app
  visibility = "internal";
};
```

Under the hood this uses the reusable helper `lib/blue-green.nix`, which emits
the two colour quadlets, the reconcile oneshot, the proxy backends (`upstreams` +
`healthCheck`), and the restart-exclude entry. A first-party app module can call
it directly:

```nix
(lib.mkIf cfg.blueGreen.enable (import ../../lib/blue-green.nix { inherit lib pkgs; } {
  inherit config; name = cfg.containerName; image = cfg.image; port = cfg.port;
  pullAuthFile = if useAuth then authFile else null;
  mkContainerText = { color, containerName, port }: '' ...one colour's quadlet... '';
}))
```

The proxy side is generic on its own too: give any service **`upstreams`** (a
list) + a **`healthCheck`** instead of a single `upstream`, and it also gets a
Traefik retry middleware so a request that lands on a just-drained backend is
retried against the healthy one.

Requirements and behaviour:

- **The app must listen on `$PORT`.** The colour slots inject `PORT=port+1` /
  `PORT=port+2` so both can run at once (12-factor apps already do this). The
  image's own HEALTHCHECK and any `healthCmd` are dropped — health is probed over
  HTTP on the colour port instead.
- **Stateless by default.** The two colours run simultaneously during a flip, so
  a single-writer store (Postgres, SQLite) would corrupt. `custom` therefore
  **refuses `volumes` with blue-green** unless you set
  `blueGreen.sharedVolumesOk = true`, asserting the app tolerates concurrent
  access (safe for read-only content or multi-replica-safe apps).
- **Deploy via the CLI.** The flip is an on-box reconcile oneshot that
  `nixos-rebuild switch` re-runs (`restartTriggers = [ image ]`); a normal
  `portablevps server deploy` triggers it and the switch waits for its exit
  status, so a failed flip (idle never healthy) keeps the old colour serving and
  fails the deploy. A colour set left un-flipped by a raw `nixos-rebuild` reveals
  no new version until a deploy runs the reconcile.
- **One-time cutover.** Turning blue-green on removes the single container and
  moves the proxy to the colour ports, so the *first* switch has a brief blip and
  leaves the old single container to be stopped once. Every subsequent deploy is
  zero-downtime.

## Options reference

| Option | Purpose |
| --- | --- |
| `image` | Pinned container image (immutable tag/digest). |
| `port` | Loopback port for the proxy (`null` if the app has no HTTP surface). |
| `network` | Podman network; defaults to `host`. |
| `env` | Plain environment variables. |
| `secretEnv` | `ENV_VAR -> sops key`, rendered to a `0400` EnvironmentFile. |
| `volumes` | Bind mounts `{ hostPath; containerPath; backup ? true; }`. |
| `uid` / `gid` | Owner for the created volume host directories. |
| `healthCmd` | Optional podman `HealthCmd` (skip if the image has its own HEALTHCHECK). |
| `pullAuthUser` / `pullAuthSecret` | Private-registry pull auth. |
| `backup.enable` / `backup.order` | Backup component toggle and restore ordering. |
| `extraPodmanArgs` | Extra `PodmanArgs=` lines. |
| `blueGreen.enable` | Zero-downtime deploys (needs `port`; app must honour `$PORT`). |
| `blueGreen.sharedVolumesOk` | Allow `volumes` with blue-green (only if the app tolerates two concurrent instances). |

The proxy route lives in the server definition, not the app: the app owns the
container and its state; the server owns the edge.

## Built-in Discourse role

Discourse has a first-party portablevps module because its supported runtime is
the `discourse_docker` launcher rather than a normal single image. A future
server should use the dedicated profile:

```nix
profile = "discourse-app";

module = { ... }: {
  portablevps.server.backupRepository = (import ../backup.nix).repo "discourse";
  portablevps.backups.restic.region = (import ../backup.nix).region;

  portablevps.apps.discourse = {
    enable = true;
    domain = "community.example.com";
    developerEmails = [ "admin@example.com" ];
    # Defaults to "latest"; set a Discourse tag or commit when you want
    # upgrade rollouts to be exact and repeatable.
    discourseVersion = "latest";
    oidc = {
      enable = true;
      issuerBaseUrl = "https://auth.example.com";
      clientId = "discourse";
      clientSecretSecret = "discourse/oidc-client-secret";
    };
    smtp = {
      enable = true;
      host = "smtp.example.com";
      notificationEmail = "noreply@example.com";
      usernameSecret = "discourse/smtp-username";
      passwordSecret = "discourse/smtp-password";
    };
  };

  portablevps.proxy.http.services.discourse = {
    domain = "community.example.com";
    upstream = "http://127.0.0.1:3080";
    visibility = "netbird-edge";
  };
};
```

The role enables Docker for this host and renders a standalone Discourse
`app.yml` under `/data/discourse/docker`, with Discourse publishing HTTP only on
loopback port `3080`. PostgreSQL, Redis, uploads, and plugin state are owned by
Discourse inside that launcher-managed appliance.

During the portablevps backup window, the module asks Discourse to create a
normal local backup archive and stores that archive in the coordinated restic
snapshot. Restore mode restores the archive first; the next normal activation
bootstraps Discourse and runs Discourse's own restore command before serving
traffic. The local DR profile verifies the restored Discourse database marker,
an uploads-tree file marker, and the `/srv/status` HTTP endpoint.

Discourse backup archives include the bundled PostgreSQL database, uploads,
themes, and other Discourse-managed state under `/shared`. The launcher source,
Docker images, and declared plugin checkout commands are regenerated from Nix
and `app.yml`; do not treat manual edits under `/data/discourse/docker` as
backed-up configuration. The module keeps only the newest local Discourse backup
tarballs by default (`backup.localArchivesToKeep = 2`) so hourly restic
snapshots do not keep re-uploading every historical Discourse archive.

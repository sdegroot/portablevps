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

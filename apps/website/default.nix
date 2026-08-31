# portablevps app: a stateless containerised web app (e.g. an Astro SSR site).
#
# Runs a single podman Quadlet container from a pinned image, on the host network
# bound to loopback, fronted by the portablevps proxy (the consumer's server def
# adds the proxy route). No volumes — stateless by design; content/assets belong
# in object storage / a CDN, not on the box. The image is pinned (an immutable
# tag) in the consumer's server def so git is the source of truth for what runs.
#
# Private registries: set `pullAuthUser` (the registry username — not a secret)
# and `pullAuthSecret` (a sops key holding ONLY the pull token/password). The
# module base64-encodes `user:token` into a podman authfile at start and pulls
# with it. Keeping the username out of the secret avoids the easy mistake of
# storing a bare token with no `user:` prefix. In prototype/local-VM mode (no
# sops) the authfile is skipped.
#
# ghcr.io note: the Container registry only accepts a *classic* PAT with the
# `read:packages` scope — fine-grained tokens have no packages permission and are
# rejected. The username is not validated by ghcr (any non-empty value works).
{ config, lib, pkgs, ... }:

let
  cfg = config.portablevps.apps.website;
  prototype = config.portablevps.secrets.allowPrototypeDefaults;

  registry = builtins.head (lib.splitString "/" cfg.image); # e.g. ghcr.io
  authFile = "/etc/portablevps/website-registry-auth.json";
  useAuth = cfg.pullAuthSecret != null && !prototype;

  restoreGate = "ConditionPathExists=!/run/portablevps/restore-mode";

  # Secret-backed env (e.g. OIDC client secret): rendered from sops into an
  # EnvironmentFile. In prototype/local-VM mode there is no sops, so demo values
  # are written directly so the config still evaluates/boots.
  secretEnvFile = "/etc/portablevps/${cfg.containerName}.env";
  hasSecretEnv = cfg.extraSecretEnv != { };
  renderSecret = key: if prototype then "demo" else config.sops.placeholder.${key};
  secretEnvContent =
    lib.concatStringsSep "\n"
      (lib.mapAttrsToList (k: secret: "${k}=${renderSecret secret}") cfg.extraSecretEnv) + "\n";

  # ---- blue-green ----------------------------------------------------------
  # The reusable orchestration (colour quadlets, reconcile oneshot, proxy
  # backends, restart-exclude) lives in lib/blue-green.nix; this module only
  # supplies the app-specific colour container body via mkColorText.
  bg = cfg.blueGreen.enable;

  # Units the registry-auth oneshot must precede: the single container, or (in
  # blue-green) both colour containers.
  authTargets =
    if bg
    then [ "${cfg.containerName}-blue.service" "${cfg.containerName}-green.service" ]
    else [ "${cfg.containerName}.service" ];

  # One colour's quadlet. Identical to the single-container [Container] block
  # except for name + PORT, and it deliberately OMITS [Install] so apps.target
  # does not auto-start it — the reconcile oneshot starts only the active colour.
  mkColorText = { color, containerName, port }: ''
    [Unit]
    Description=${containerName} (stateless web app, blue-green) for portablevps
    After=network-online.target
    Wants=network-online.target
    PartOf=apps.target
    ${restoreGate}

    [Container]
    Image=${cfg.image}
    ContainerName=${containerName}
    # Host network, bound to loopback — the proxy health-checks 127.0.0.1:${toString port}.
    Network=host
    Environment=HOST=127.0.0.1
    Environment=PORT=${toString port}
    Environment=NODE_ENV=production
    ${lib.concatStringsSep "\n" (lib.mapAttrsToList (k: v: "Environment=${k}=${v}") cfg.extraEnv)}
    ${lib.optionalString hasSecretEnv "EnvironmentFile=${secretEnvFile}"}
    ${lib.optionalString useAuth "PodmanArgs=--authfile=${authFile}"}
    # Disable the image's built-in HEALTHCHECK: it commonly hardcodes the app's
    # default port (e.g. an Astro image probes 4321), so on a colour port it
    # always reports unhealthy — which both fails the switch (podman healthcheck
    # transient) and breaks the reconcile gate. Health is enforced by HTTP on the
    # REAL port instead: Traefik's loadBalancer healthCheck (routing) and the
    # reconcile's curl probe (flip gate).
    PodmanArgs=--no-healthcheck

    [Service]
    Restart=always
    TimeoutStartSec=180
  '';

  bgFragment = import ../../lib/blue-green.nix { inherit lib pkgs; } {
    inherit config;
    name = cfg.containerName;
    image = cfg.image;
    port = cfg.port;
    pullAuthFile = if useAuth then authFile else null;
    mkContainerText = mkColorText;
  };
in
{
  options.portablevps.apps.website = {
    enable = lib.mkEnableOption "the portablevps stateless web-app container";

    image = lib.mkOption {
      type = lib.types.str;
      example = "ghcr.io/you/website:1.4.0";
      description = "Pinned container image (use an immutable release tag, not :latest).";
    };

    port = lib.mkOption {
      type = lib.types.port;
      default = 4321;
      description = "Port the app listens on inside the container; bound to 127.0.0.1 on the host network and fronted by the proxy.";
    };

    containerName = lib.mkOption {
      type = lib.types.str;
      default = "website";
      description = "Podman container/unit name.";
    };

    pullAuthUser = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "ci-bot";
      description = ''
        Registry username paired with `pullAuthSecret` (NOT a secret — it is an
        identifier, so it lives in config, not sops). For ghcr.io any non-empty
        value works (ghcr authenticates by the token); use the bot account name
        for clarity. Required when `pullAuthSecret` is set.
      '';
    };

    pullAuthSecret = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "website/ghcr-pull-auth";
      description = ''
        Optional sops key holding ONLY the pull token/password for the image's
        registry (for ghcr.io: a classic PAT with the `read:packages` scope — no
        username, no `user:` prefix). The module pairs it with `pullAuthUser`,
        base64-encodes `user:token`, and renders a podman authfile so a private
        image can be pulled. Leave null for a public image.
      '';
    };

    extraEnv = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = { };
      description = "Extra plain environment variables passed to the container.";
    };

    extraSecretEnv = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = { };
      example = { OIDC_CLIENT_SECRET = "website/oidc-client-secret"; };
      description = ''
        Env backed by sops secrets, as ENV_VAR = "<sops key>". Each key is
        declared as a sops secret and rendered into an EnvironmentFile as
        ENV_VAR=<placeholder>. Use for the OIDC client secret, AUTH_SECRET, etc.
      '';
    };

    blueGreen.enable = lib.mkEnableOption ''
      zero-downtime blue-green deploys. Instead of one container, run two
      colour slots (blue on port+1, green on port+2); only the ACTIVE colour
      runs in steady state. On an image change a reconcile oneshot (re-run by
      `nixos-rebuild switch` via restartTriggers) warms the idle colour on the
      new image, waits for it to be healthy, flips, then drains the old colour.
      The proxy fronts both ports with a health check so traffic only ever hits
      a healthy backend. Requires the app to be fronted by the portablevps proxy
    '';
  };

  config = lib.mkIf cfg.enable (lib.mkMerge [
    # Single-container mode (blue-green disabled): unchanged.
    (lib.mkIf (!bg) {
      environment.etc."containers/systemd/${cfg.containerName}.container".text = ''
        [Unit]
        Description=${cfg.containerName} (stateless web app) for portablevps
        After=network-online.target
        Wants=network-online.target
        PartOf=apps.target
        ${restoreGate}

        [Container]
        Image=${cfg.image}
        ContainerName=${cfg.containerName}
        # Host network, bound to loopback — the proxy fronts it on 127.0.0.1:${toString cfg.port}.
        Network=host
        Environment=HOST=127.0.0.1
        Environment=PORT=${toString cfg.port}
        Environment=NODE_ENV=production
        ${lib.concatStringsSep "\n" (lib.mapAttrsToList (k: v: "Environment=${k}=${v}") cfg.extraEnv)}
        ${lib.optionalString hasSecretEnv "EnvironmentFile=${secretEnvFile}"}
        ${lib.optionalString useAuth "PodmanArgs=--authfile=${authFile}"}
        # No HealthCmd here: when the image ships its own HEALTHCHECK, podman uses
        # it automatically. Restart=always covers a crashed container meanwhile.

        [Service]
        Restart=always
        TimeoutStartSec=180

        [Install]
        WantedBy=apps.target
      '';
    })

    # Blue-green mode: two colour quadlets + a reconcile oneshot (selector at
    # boot, flipper on image change) + the proxy backends + the restart-exclude
    # list so restartChangedQuadlets leaves the colour units to the reconcile.
    (lib.mkIf bg bgFragment)

    # Private-registry pull auth: the sops secret holds ONLY the token; a oneshot
    # combines it with the (non-secret) username and base64-encodes `user:token`
    # into a podman authfile before the container. (podman's auth.json wants
    # base64(user:token); encoding here means the operator never pre-encodes and
    # can't store a bare token with no `user:` prefix.)
    (lib.mkIf useAuth {
      assertions = [{
        assertion = cfg.pullAuthUser != null;
        message = "portablevps.apps.website: pullAuthUser must be set when pullAuthSecret is set (the registry username, e.g. the bot account name).";
      }];
      sops.secrets.${cfg.pullAuthSecret} = { };
      systemd.services."${cfg.containerName}-registry-auth" = {
        description = "Render the ${cfg.containerName} podman registry authfile from sops";
        before = authTargets;
        requiredBy = authTargets;
        serviceConfig = {
          Type = "oneshot";
          RemainAfterExit = true;
        };
        script = ''
          set -eu
          token="$(${pkgs.coreutils}/bin/tr -d '\n' < ${config.sops.secrets.${cfg.pullAuthSecret}.path})"
          auth="$(${pkgs.coreutils}/bin/printf '%s:%s' ${lib.escapeShellArg cfg.pullAuthUser} "$token" \
            | ${pkgs.coreutils}/bin/base64 -w0)"
          umask 077
          ${pkgs.coreutils}/bin/install -Dm0400 /dev/null ${authFile}
          printf '{"auths":{"${registry}":{"auth":"%s"}}}' "$auth" > ${authFile}
        '';
      };
    })

    # Secret-backed env → EnvironmentFile. Normal mode renders from sops;
    # prototype/local-VM writes demo values directly (no sops).
    (lib.mkIf (hasSecretEnv && !prototype) {
      sops.secrets = lib.genAttrs (lib.unique (lib.attrValues cfg.extraSecretEnv)) (_: { });
      sops.templates."portablevps/${cfg.containerName}.env" = {
        path = secretEnvFile;
        mode = "0400";
        content = secretEnvContent;
      };
    })
    (lib.mkIf (hasSecretEnv && prototype) {
      environment.etc."portablevps/${cfg.containerName}.env" = {
        mode = "0400";
        text = secretEnvContent;
      };
    })
  ]);
}

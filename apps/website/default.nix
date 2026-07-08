# portablevps app: a stateless containerised web app (e.g. an Astro SSR site).
#
# Runs a single podman Quadlet container from a pinned image, on the host network
# bound to loopback, fronted by the portablevps proxy (the consumer's server def
# adds the proxy route). No volumes — stateless by design; content/assets belong
# in object storage / a CDN, not on the box. The image is pinned (an immutable
# tag) in the consumer's server def so git is the source of truth for what runs.
#
# Private registries: set `pullAuthSecret` to a sops key holding the base64
# `username:token` for the registry; the module renders a podman authfile and
# pulls with it. In prototype/local-VM mode (no sops) the authfile is skipped.
{ config, lib, ... }:

let
  cfg = config.portablevps.apps.website;
  prototype = config.portablevps.secrets.allowPrototypeDefaults;

  registry = builtins.head (lib.splitString "/" cfg.image); # e.g. ghcr.io
  authFile = "/etc/portablevps/website-registry-auth.json";
  useAuth = cfg.pullAuthSecret != null && !prototype;

  restoreGate = "ConditionPathExists=!/run/portablevps/restore-mode";

  healthCmd =
    "node -e \"require('http').get('http://127.0.0.1:${toString cfg.port}/',r=>process.exit(r.statusCode<500?0:1)).on('error',()=>process.exit(1))\"";
in
{
  options.portablevps.apps.website = {
    enable = lib.mkEnableOption "the portablevps stateless web-app container";

    image = lib.mkOption {
      type = lib.types.str;
      example = "ghcr.io/epistola-app/website:1.4.0";
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

    pullAuthSecret = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "website/ghcr-pull-auth";
      description = ''
        Optional sops key holding the base64 of `username:token` for the image's
        registry (compute once: `printf '%s' 'USER:TOKEN' | base64`). Rendered
        into a podman authfile so a private image can be pulled. Leave null for a
        public image.
      '';
    };

    extraEnv = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = { };
      description = "Extra plain environment variables passed to the container.";
    };
  };

  config = lib.mkIf cfg.enable (lib.mkMerge [
    {
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
        ${lib.optionalString useAuth "PodmanArgs=--authfile=${authFile}"}
        HealthCmd=${healthCmd}
        HealthStartPeriod=30s
        HealthInterval=30s
        HealthTimeout=10s
        HealthRetries=3
        HealthOnFailure=kill

        [Service]
        Restart=always
        TimeoutStartSec=180

        [Install]
        WantedBy=apps.target
      '';
    }

    # Private-registry pull auth: render a podman authfile from the sops secret.
    (lib.mkIf useAuth {
      sops.secrets.${cfg.pullAuthSecret} = { };
      sops.templates."portablevps/website-registry-auth.json" = {
        content = builtins.toJSON {
          auths.${registry}.auth = config.sops.placeholder.${cfg.pullAuthSecret};
        };
        path = authFile;
        owner = "root";
        mode = "0400";
      };
    })
  ]);
}

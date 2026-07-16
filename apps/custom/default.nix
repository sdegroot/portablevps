# portablevps app: bring-your-own container.
#
# A declarative way to run an arbitrary container image as a portablevps app —
# without writing a NixOS module. Define one or more apps under
# `portablevps.apps.custom.<name>` (image, an optional loopback HTTP port for the
# proxy, bind-mount volumes, plain and sops-backed env, private-registry pull
# auth, an optional healthcheck) and portablevps materialises the Quadlet unit,
# the restore-mode gate, the secret env file, and — for a stateful app — a backup
# component so its data is included in the same restic snapshot and restored in
# order, exactly like the first-party apps.
#
# The consumer's server definition still adds the proxy route (pointing at
# 127.0.0.1:<port>); this module owns the container and its state, not the edge.
{ config, lib, pkgs, ... }:

let
  apps = config.portablevps.apps.custom;
  prototype = config.portablevps.secrets.allowPrototypeDefaults;
  restoreGate = "ConditionPathExists=!/run/portablevps/restore-mode";

  volumeType = lib.types.submodule {
    options = {
      hostPath = lib.mkOption {
        type = lib.types.str;
        example = "/data/myapp";
        description = "Host directory to bind-mount (keep it under /data so it is part of the machine's state).";
      };
      containerPath = lib.mkOption {
        type = lib.types.str;
        example = "/var/lib/myapp";
        description = "Mount point inside the container.";
      };
      backup = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Include this volume's hostPath in the app's backup component.";
      };
    };
  };

  appType = lib.types.submodule ({ name, ... }: {
    options = {
      enable = lib.mkEnableOption "this custom container app";

      image = lib.mkOption {
        type = lib.types.str;
        example = "docker.io/library/nginx:1.27.3";
        description = "Pinned container image (use an immutable tag/digest, not :latest).";
      };

      containerName = lib.mkOption {
        type = lib.types.str;
        default = name;
        description = "Podman container/unit name.";
      };

      port = lib.mkOption {
        type = lib.types.nullOr lib.types.port;
        default = null;
        example = 8080;
        description = ''
          Port the app listens on inside the container. With Network=host it is
          bound on the host; add a proxy route to 127.0.0.1:<port> in the server
          definition. Null for an app with no HTTP surface.
        '';
      };

      network = lib.mkOption {
        type = lib.types.str;
        default = "host";
        description = "Podman network. Defaults to host so the proxy can reach it on loopback, matching the other portablevps apps.";
      };

      env = lib.mkOption {
        type = lib.types.attrsOf lib.types.str;
        default = { };
        description = "Plain (non-secret) environment variables.";
      };

      secretEnv = lib.mkOption {
        type = lib.types.attrsOf lib.types.str;
        default = { };
        example = { DB_PASSWORD = "myapp/db-password"; };
        description = ''
          Environment variables sourced from sops: maps ENV_VAR -> sops key. The
          keys are declared as sops secrets and rendered into a 0400 EnvironmentFile.
          Under prototype/local-VM secrets the values are placeholders so the app
          still boots for disaster-recovery testing.
        '';
      };

      volumes = lib.mkOption {
        type = lib.types.listOf volumeType;
        default = [ ];
        description = "Bind-mount volumes. Volumes with backup=true are added to the app's backup component.";
      };

      uid = lib.mkOption {
        type = lib.types.nullOr lib.types.int;
        default = null;
        example = 1000;
        description = "If set, the app's volume host directories are created owned by this uid (the uid the image runs as).";
      };

      gid = lib.mkOption {
        type = lib.types.nullOr lib.types.int;
        default = null;
        description = "Group owner for the volume host directories; defaults to uid when uid is set.";
      };

      healthCmd = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = "curl -fsS http://localhost:8080/health || exit 1";
        description = "Optional podman HealthCmd. If the image ships its own HEALTHCHECK, leave this null.";
      };

      pullAuthUser = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Registry username paired with pullAuthSecret (an identifier, not a secret). Required when pullAuthSecret is set.";
      };

      pullAuthSecret = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = "myapp/registry-token";
        description = "sops key holding ONLY the pull token/password for a private registry. The module renders a podman authfile from <user>:<token>.";
      };

      extraPodmanArgs = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = "Extra arguments appended verbatim as PodmanArgs= in the Quadlet unit.";
      };

      backup = {
        enable = lib.mkOption {
          type = lib.types.bool;
          default = true;
          description = ''
            Register a backup component for this app's backed-up volume host
            paths. Has no effect for a stateless app (no volumes with
            backup=true); set false to keep a stateful app's volumes out of
            backups deliberately.
          '';
        };
        order = lib.mkOption {
          type = lib.types.ints.positive;
          default = 40;
          description = "Restore ordering for this app's component (postgres=10, acme=15, container-state=20).";
        };
      };

      blueGreen = {
        enable = lib.mkEnableOption ''
          zero-downtime blue-green deploys for this app. Runs two colour slots
          (on port+1/port+2) but keeps only one running; on an image change a
          reconcile oneshot warms the idle colour on the new image, waits for
          health, flips, and drains the old colour (see lib/blue-green.nix and
          docs/run-your-own-app.md). Requirements: the app must have an HTTP
          surface (`port` set) AND listen on the `$PORT` env var (the colour
          slots inject PORT=port+1 / PORT=port+2 so both can run at once). The
          image's own HEALTHCHECK and any `healthCmd` are dropped; health is
          probed over HTTP on the colour port. Stateless by default — see
          `blueGreen.sharedVolumesOk` before combining with volumes
        '';
        sharedVolumesOk = lib.mkOption {
          type = lib.types.bool;
          default = false;
          description = ''
            Acknowledge that this app tolerates TWO instances accessing its
            volumes concurrently — during a flip both colours run for a few
            seconds against the same host paths. Required to combine
            `blueGreen.enable` with any `volumes`. Leave false for single-writer
            stores (Postgres, SQLite, most embedded DBs): a second instance on
            the same data directory will fail to start or corrupt it. Safe to set
            for read-only content volumes or apps built for concurrent shared
            storage.
          '';
        };
      };
    };
  });

  # Per-app configuration fragment.
  # Only enabled apps contribute anything. Referenced only inside option VALUES
  # below (never to shape the top-level `config`), so evaluating the
  # portablevps.apps.custom option does not force this module's config —
  # avoiding the declares-and-iterates-the-same-option recursion.
  enabledApps = lib.filterAttrs (_: a: a.enable) apps;

  # Per-app derived values, computed once.
  derive = name: app: rec {
    inherit name app;
    registry = builtins.head (lib.splitString "/" app.image);
    authFile = "/etc/portablevps/custom-${name}-registry-auth.json";
    useAuth = app.pullAuthSecret != null && !prototype;
    envFile = "/etc/portablevps/custom-${name}.env";
    hasSecretEnv = app.secretEnv != { };
    useSecretEnv = hasSecretEnv && !prototype;
    backupPaths = map (v: v.hostPath) (lib.filter (v: v.backup) app.volumes);
    ownerStr =
      if app.uid == null then "root root"
      else "${toString app.uid} ${toString (if app.gid == null then app.uid else app.gid)}";
    containerText = ''
      [Unit]
      Description=${app.containerName} (portablevps custom app)
      After=network-online.target
      Wants=network-online.target
      PartOf=apps.target
      ${restoreGate}

      [Container]
      Image=${app.image}
      ContainerName=${app.containerName}
      Network=${app.network}
      ${lib.concatStringsSep "\n" (lib.mapAttrsToList (k: v: "Environment=${k}=${v}") app.env)}
      ${lib.optionalString hasSecretEnv "EnvironmentFile=${envFile}"}
      ${lib.concatMapStringsSep "\n" (v: "Volume=${v.hostPath}:${v.containerPath}") app.volumes}
      ${lib.optionalString (app.healthCmd != null) "HealthCmd=${app.healthCmd}"}
      ${lib.optionalString useAuth "PodmanArgs=--authfile=${authFile}"}
      ${lib.concatMapStringsSep "\n" (a: "PodmanArgs=${a}") app.extraPodmanArgs}

      [Service]
      Restart=always
      TimeoutStartSec=180

      [Install]
      WantedBy=apps.target
    '';

    bg = app.blueGreen.enable;
    # Only render the blue-green machinery once the config is valid (port set);
    # otherwise `port + 1` in the helper would crash before the assertion below
    # can report the real problem cleanly.
    bgReady = app.blueGreen.enable && app.port != null;

    # One colour's quadlet for blue-green: the container body with a per-colour
    # name + PORT (injected LAST so it wins over any env.PORT), no [Install] (the
    # reconcile starts only the active colour), and --no-healthcheck (health is
    # HTTP on the colour port via Traefik + the reconcile probe).
    mkColorText = { color, containerName, port }: ''
      [Unit]
      Description=${containerName} (portablevps custom app, blue-green)
      After=network-online.target
      Wants=network-online.target
      PartOf=apps.target
      ${restoreGate}

      [Container]
      Image=${app.image}
      ContainerName=${containerName}
      Network=${app.network}
      ${lib.concatStringsSep "\n" (lib.mapAttrsToList (k: v: "Environment=${k}=${v}") app.env)}
      Environment=PORT=${toString port}
      ${lib.optionalString hasSecretEnv "EnvironmentFile=${envFile}"}
      ${lib.concatMapStringsSep "\n" (v: "Volume=${v.hostPath}:${v.containerPath}") app.volumes}
      ${lib.optionalString useAuth "PodmanArgs=--authfile=${authFile}"}
      ${lib.concatMapStringsSep "\n" (a: "PodmanArgs=${a}") app.extraPodmanArgs}
      PodmanArgs=--no-healthcheck

      [Service]
      Restart=always
      TimeoutStartSec=180
    '';

    # Config fragment from the shared blue-green helper (colour quadlets,
    # reconcile oneshot, proxy backends, restart-exclude), for blue-green apps.
    bgFragment = import ../../lib/blue-green.nix { inherit lib pkgs; } {
      inherit config;
      name = app.containerName;
      image = app.image;
      port = app.port;
      pullAuthFile = if useAuth then authFile else null;
      mkContainerText = mkColorText;
    };

    # Units the registry-auth oneshot must precede.
    authTargets =
      if bg
      then [ "${app.containerName}-blue.service" "${app.containerName}-green.service" ]
      else [ "${app.containerName}.service" ];
  };

  derived = lib.mapAttrsToList derive enabledApps;
in
{
  options.portablevps.apps.custom = lib.mkOption {
    type = lib.types.attrsOf appType;
    default = { };
    description = ''
      Bring-your-own container apps. Each entry runs an image as a portablevps
      app (Quadlet unit, restore-mode gate, secret env, and — for stateful apps —
      an ordered backup component). Add the proxy route in the server definition.
    '';
  };

  # NOTE: every key of `config` here is a literal (environment, systemd,
  # portablevps.backups, sops, assertions); the per-app iteration lives inside
  # each value. This lets the module system see what options this module sets
  # without evaluating `enabledApps`, which is what breaks the recursion.
  config = {
    environment.etc = lib.mkMerge (
      # Single container — blue-green apps emit colour quadlets via bgFragment instead.
      (map (d: lib.optionalAttrs (!d.bg) { "containers/systemd/${d.app.containerName}.container".text = d.containerText; }) derived)
      # Blue-green colour quadlets (from the shared helper).
      ++ (map (d: lib.optionalAttrs d.bgReady d.bgFragment.environment.etc) derived)
      # Secret env under prototype/local-VM: placeholders so the container boots
      # for DR testing without a sops file.
      ++ (map
        (d: lib.optionalAttrs (d.hasSecretEnv && prototype) {
          ${lib.removePrefix "/etc/" d.envFile} = {
            mode = "0400";
            text = lib.concatStringsSep "\n"
              (lib.mapAttrsToList (env: _key: "${env}=prototype-${lib.toLower env}") d.app.secretEnv);
          };
        })
        derived)
    );

    systemd.tmpfiles.rules = lib.concatMap
      (d: map (v: "d ${v.hostPath} 0750 ${d.ownerStr} -") d.app.volumes)
      derived;

    # Private-registry pull auth (mirrors the website app): sops holds only the
    # token; a oneshot base64-encodes user:token into a podman authfile.
    systemd.services = lib.mkMerge (
      (map
        (d: lib.optionalAttrs d.useAuth {
          "${d.app.containerName}-registry-auth" = {
            description = "Render the ${d.app.containerName} podman registry authfile from sops";
            before = d.authTargets;
            requiredBy = d.authTargets;
            serviceConfig = {
              Type = "oneshot";
              RemainAfterExit = true;
            };
            script = ''
              set -eu
              token="$(${pkgs.coreutils}/bin/tr -d '\n' < ${config.sops.secrets.${d.app.pullAuthSecret}.path})"
              auth="$(${pkgs.coreutils}/bin/printf '%s:%s' ${lib.escapeShellArg d.app.pullAuthUser} "$token" \
                | ${pkgs.coreutils}/bin/base64 -w0)"
              umask 077
              ${pkgs.coreutils}/bin/install -Dm0400 /dev/null ${d.authFile}
              printf '{"auths":{"${d.registry}":{"auth":"%s"}}}' "$auth" > ${d.authFile}
            '';
          };
        })
        derived)
      # Blue-green reconcile oneshot (from the shared helper).
      ++ (map (d: lib.optionalAttrs d.bgReady d.bgFragment.systemd.services) derived));

    # Blue-green proxy backends (both colour ports + health check) and the
    # restart-exclude, contributed per app by the shared helper.
    portablevps.proxy.http.services = lib.mkMerge
      (map (d: lib.optionalAttrs d.bgReady d.bgFragment.portablevps.proxy.http.services) derived);
    portablevps.podman.bluegreenExcludedUnits = lib.concatMap
      (d: lib.optionals d.bgReady d.bgFragment.portablevps.podman.bluegreenExcludedUnits) derived;

    # Backup component: the app's stateful volumes ride in the shared restic
    # snapshot and are cleared+restored in order during a restore.
    portablevps.backups.components = lib.mkMerge (map
      (d: lib.optionalAttrs (d.app.backup.enable && d.backupPaths != [ ]) {
        ${d.name} = {
          order = d.app.backup.order;
          paths = d.backupPaths;
          clearBeforeRestore = d.backupPaths;
        };
      })
      derived);

    sops.secrets = lib.mkMerge (map
      (d:
        (lib.optionalAttrs d.useSecretEnv
          (lib.listToAttrs (lib.mapAttrsToList (_env: key: lib.nameValuePair key { }) d.app.secretEnv)))
        // (lib.optionalAttrs d.useAuth { ${d.app.pullAuthSecret} = { }; }))
      derived);

    # Secret env from sops (real hosts).
    sops.templates = lib.mkMerge (map
      (d: lib.optionalAttrs d.useSecretEnv {
        "custom-${d.name}.env" = {
          path = d.envFile;
          mode = "0400";
          content = lib.concatStringsSep "\n"
            (lib.mapAttrsToList (env: key: "${env}=${config.sops.placeholder.${key}}") d.app.secretEnv);
        };
      })
      derived);

    assertions = lib.concatMap
      (d:
        (lib.optional d.useAuth {
          assertion = d.app.pullAuthUser != null;
          message = "portablevps.apps.custom.${d.name}: pullAuthUser must be set when pullAuthSecret is set.";
        })
        ++ (lib.optionals d.bg [
          {
            assertion = d.app.port != null;
            message = "portablevps.apps.custom.${d.name}: blueGreen.enable requires `port` (the app must expose an HTTP port and listen on $PORT).";
          }
          {
            assertion = d.app.volumes == [ ] || d.app.blueGreen.sharedVolumesOk;
            message = "portablevps.apps.custom.${d.name}: blueGreen.enable runs two instances during a flip, so it refuses `volumes` unless blueGreen.sharedVolumesOk = true (only safe if the app tolerates concurrent access — NOT single-writer databases).";
          }
        ]))
      derived;
  };
}

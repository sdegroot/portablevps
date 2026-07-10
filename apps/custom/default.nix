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
      (map (d: { "containers/systemd/${d.app.containerName}.container".text = d.containerText; }) derived)
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
    systemd.services = lib.mkMerge (map
      (d: lib.optionalAttrs d.useAuth {
        "${d.app.containerName}-registry-auth" = {
          description = "Render the ${d.app.containerName} podman registry authfile from sops";
          before = [ "${d.app.containerName}.service" ];
          requiredBy = [ "${d.app.containerName}.service" ];
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
      derived);

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
      (d: lib.optional d.useAuth {
        assertion = d.app.pullAuthUser != null;
        message = "portablevps.apps.custom.${d.name}: pullAuthUser must be set when pullAuthSecret is set.";
      })
      derived;
  };
}

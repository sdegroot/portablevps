# portablevps app: Forgejo Actions runner.
#
# This is intentionally separate from the Forgejo service app. Runners execute
# repository-controlled code and should live on disposable compute hosts, not on
# the stateful Forgejo/PostgreSQL host.
{ config, lib, pkgs, ... }:

let
  cfg = config.portablevps.apps.forgejoRunner;
  prototype = config.portablevps.secrets.allowPrototypeDefaults;
  restoreGate = "ConditionPathExists=!/run/portablevps/restore-mode";
  dataRoot = cfg.dataRoot;
  tokenPath =
    if prototype
    then pkgs.writeText "forgejo-runner-demo-token" "demo"
    else config.sops.secrets.${cfg.registrationTokenSecret}.path;

  configFile = pkgs.writeText "forgejo-runner-config.yaml" (builtins.toJSON {
    log.level = cfg.logLevel;
    runner = {
      file = "${dataRoot}/runner/.runner";
      capacity = cfg.capacity;
      timeout = cfg.timeout;
    };
    container = {
      network = cfg.container.network;
      privileged = cfg.container.privileged;
      options = cfg.container.options;
      valid_volumes = cfg.container.validVolumes;
      docker_host = cfg.container.dockerHost;
    };
    host.workdir_parent = "${dataRoot}/work";
  });
in
{
  options.portablevps.apps.forgejoRunner = {
    enable = lib.mkEnableOption "Forgejo Actions runner";

    image = lib.mkOption {
      type = lib.types.str;
      default = "data.forgejo.org/forgejo/runner:12";
      description = "Forgejo runner container image.";
    };

    dindImage = lib.mkOption {
      type = lib.types.str;
      default = "docker.io/library/docker:28-dind";
      description = "Docker-in-Docker daemon image used by job containers.";
    };

    dataRoot = lib.mkOption {
      type = lib.types.str;
      default = "/data/forgejo-runner";
      description = "Runner state and work directory root.";
    };

    instanceUrl = lib.mkOption {
      type = lib.types.str;
      example = "https://code.int.epistola.app";
      description = "Forgejo instance URL the runner registers with.";
    };

    name = lib.mkOption {
      type = lib.types.str;
      default = config.networking.hostName;
      description = "Runner name shown in Forgejo.";
    };

    labels = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ "docker:docker://node:22-bookworm" ];
      description = "Forgejo runner labels advertised during registration.";
    };

    registrationTokenSecret = lib.mkOption {
      type = lib.types.str;
      default = "forgejo-runner/registration-token";
      description = "sops key containing a repository/org-scoped runner registration token.";
    };

    capacity = lib.mkOption {
      type = lib.types.ints.positive;
      default = 1;
      description = "Maximum concurrent jobs for this runner.";
    };

    timeout = lib.mkOption {
      type = lib.types.str;
      default = "3h";
      description = "Maximum job runtime.";
    };

    logLevel = lib.mkOption {
      type = lib.types.enum [ "trace" "debug" "info" "warn" "error" ];
      default = "info";
      description = "Runner log level.";
    };

    container = {
      dockerHost = lib.mkOption {
        type = lib.types.str;
        default = "tcp://127.0.0.1:2375";
        description = "Docker daemon endpoint used for job containers.";
      };
      network = lib.mkOption {
        type = lib.types.str;
        default = "";
        description = "Network assigned to job containers. Empty uses the runner default.";
      };
      privileged = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = "Whether job containers may run privileged.";
      };
      options = lib.mkOption {
        type = lib.types.str;
        default = "--cpus=2 --memory=4g --pids-limit=512";
        description = "Additional docker options applied to job containers.";
      };
      validVolumes = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = "Host volumes workflows are allowed to mount.";
      };
    };
  };

  config = lib.mkIf cfg.enable (lib.mkMerge [
    {
      assertions = [
        {
          assertion = cfg.container.privileged == false;
          message = "portablevps.apps.forgejoRunner.container.privileged must remain false; use the isolated DIND sidecar for Docker builds.";
        }
      ];

      systemd.tmpfiles.rules = [
        "d ${dataRoot} 0750 root root -"
        "d ${dataRoot}/runner 0750 1000 1000 -"
        "d ${dataRoot}/work 0750 1000 1000 -"
        "d ${dataRoot}/dind 0700 root root -"
      ];

      environment.etc."containers/systemd/forgejo-runner-dind.container".text = ''
        [Unit]
        Description=Forgejo runner Docker-in-Docker daemon
        After=network-online.target
        Wants=network-online.target
        PartOf=apps.target
        ${restoreGate}

        [Container]
        Image=${cfg.dindImage}
        ContainerName=forgejo-runner-dind
        Network=host
        PodmanArgs=--privileged
        Environment=DOCKER_TLS_CERTDIR=
        Volume=${dataRoot}/dind:/var/lib/docker:Z
        Exec=dockerd --host=tcp://127.0.0.1:2375 --host=unix:///var/run/docker.sock --tls=false

        [Service]
        Restart=always
        TimeoutStartSec=180

        [Install]
        WantedBy=apps.target
      '';

      systemd.services.forgejo-runner-register = {
        description = "Register Forgejo Actions runner";
        wantedBy = lib.optional (!config.portablevps.restoreMode) "apps.target";
        before = [ "forgejo-runner.service" ];
        after = [ "network-online.target" "forgejo-runner-dind.service" ];
        wants = [ "network-online.target" "forgejo-runner-dind.service" ];
        partOf = [ "apps.target" ];
        path = [ pkgs.coreutils pkgs.podman ];
        unitConfig.ConditionPathExists = "!/run/portablevps/restore-mode";
        serviceConfig = {
          Type = "oneshot";
          RemainAfterExit = true;
        };
        script = ''
          set -eu
          if [ -s ${dataRoot}/runner/.runner ]; then
            exit 0
          fi
          token="$(tr -d '\n' < ${tokenPath})"
          podman run --rm \
            --network host \
            -v ${dataRoot}/runner:${dataRoot}/runner:Z \
            -v ${dataRoot}/work:${dataRoot}/work:Z \
            -v ${configFile}:/etc/forgejo-runner/config.yaml:ro \
            ${lib.escapeShellArg cfg.image} \
            forgejo-runner register \
              --no-interactive \
              --config /etc/forgejo-runner/config.yaml \
              --instance ${lib.escapeShellArg cfg.instanceUrl} \
              --token "$token" \
              --name ${lib.escapeShellArg cfg.name} \
              --labels ${lib.escapeShellArg (lib.concatStringsSep "," cfg.labels)}
        '';
      };

      environment.etc."containers/systemd/forgejo-runner.container".text = ''
        [Unit]
        Description=Forgejo Actions runner
        After=network-online.target forgejo-runner-dind.service forgejo-runner-register.service
        Wants=network-online.target forgejo-runner-dind.service
        Requires=forgejo-runner-register.service
        PartOf=apps.target
        ${restoreGate}

        [Container]
        Image=${cfg.image}
        ContainerName=forgejo-runner
        Network=host
        Environment=DOCKER_HOST=${cfg.container.dockerHost}
        Volume=${dataRoot}/runner:${dataRoot}/runner:Z
        Volume=${dataRoot}/work:${dataRoot}/work:Z
        Volume=${configFile}:/etc/forgejo-runner/config.yaml:ro
        Exec=forgejo-runner daemon --config /etc/forgejo-runner/config.yaml

        [Service]
        Restart=always
        TimeoutStartSec=180

        [Install]
        WantedBy=apps.target
      '';
    }

    (lib.mkIf (!prototype) {
      sops.secrets.${cfg.registrationTokenSecret} = { };
    })
  ]);
}

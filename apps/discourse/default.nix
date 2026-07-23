# portablevps app: Discourse forum.
#
# Discourse is operated as its supported Docker/launcher appliance. The
# authoritative recovery artifact is a Discourse backup tarball; portablevps
# owns scheduling, restic storage, restore-mode gating, and DR verification.
{ config, lib, pkgs, ... }:

let
  cfg = config.portablevps.apps.discourse;
  prototype = config.portablevps.secrets.allowPrototypeDefaults;

  appName = cfg.containerName;
  dataRoot = cfg.dataRoot;
  dockerRoot = "${dataRoot}/docker";
  sharedRoot = "${dataRoot}/shared/standalone";
  backupDir = "${sharedRoot}/backups/default";
  pendingRestore = "${dataRoot}/pending-restore-backup";
  appYml = "${dockerRoot}/containers/${appName}.yml";

  discourseDockerSrc = pkgs.fetchzip {
    url = "https://github.com/discourse/discourse_docker/archive/${cfg.discourseDocker.rev}.tar.gz";
    hash = cfg.discourseDocker.hash;
  };

  envPath =
    if prototype
    then "/etc/portablevps/discourse.env"
    else config.sops.templates."portablevps/discourse.env".path;

  secretEnv =
    lib.optionalAttrs cfg.smtp.enable {
      DISCOURSE_SMTP_PASSWORD = cfg.smtp.passwordSecret;
    }
    // lib.optionalAttrs (cfg.smtp.enable && cfg.smtp.usernameSecret != null) {
      DISCOURSE_SMTP_USER_NAME = cfg.smtp.usernameSecret;
    }
    // lib.optionalAttrs cfg.oidc.enable {
      DISCOURSE_OPENID_CONNECT_CLIENT_SECRET = cfg.oidc.clientSecretSecret;
    }
    // cfg.extraSecretEnv;

  demoSecret = _key: "demo";

  renderSecret = key:
    if prototype then demoSecret key else config.sops.placeholder.${key};

  plainEnv =
    {
      DISCOURSE_HOSTNAME = cfg.domain;
      DISCOURSE_DEVELOPER_EMAILS = lib.concatStringsSep "," cfg.developerEmails;
      DISCOURSE_SERVE_STATIC_ASSETS = "true";
      RAILS_ENV = "production";
    }
    // lib.optionalAttrs (!cfg.smtp.enable) {
      DISCOURSE_SKIP_EMAIL_SETUP = "1";
    }
    // lib.optionalAttrs cfg.smtp.enable {
      DISCOURSE_SMTP_ADDRESS = cfg.smtp.host;
      DISCOURSE_SMTP_PORT = toString cfg.smtp.port;
      DISCOURSE_SMTP_ENABLE_START_TLS = lib.boolToString cfg.smtp.startTls;
      DISCOURSE_NOTIFICATION_EMAIL = cfg.smtp.notificationEmail;
    }
    // lib.optionalAttrs cfg.oidc.enable {
      DISCOURSE_OPENID_CONNECT_ENABLED = "true";
      DISCOURSE_OPENID_CONNECT_DISCOVERY_DOCUMENT =
        "${cfg.oidc.issuerBaseUrl}/application/o/${cfg.oidc.clientId}/.well-known/openid-configuration";
      DISCOURSE_OPENID_CONNECT_CLIENT_ID = cfg.oidc.clientId;
      DISCOURSE_OPENID_CONNECT_AUTHORIZE_SCOPE = lib.concatStringsSep " " cfg.oidc.scopes;
      DISCOURSE_OPENID_CONNECT_BUTTON_LABEL = cfg.oidc.buttonLabel;
      DISCOURSE_OPENID_CONNECT_FULL_SCREEN_LOGIN = lib.boolToString cfg.oidc.fullScreenLogin;
    }
    // cfg.extraEnv;

  envContent =
    lib.concatStringsSep "\n" (
      (lib.mapAttrsToList (k: v: "${k}=${v}") plainEnv)
      ++ (lib.mapAttrsToList (k: secret: "${k}=${renderSecret secret}") secretEnv)
    ) + "\n";

  pruneBackupArchives = pkgs.writeShellScript "portablevps-discourse-prune-backup-archives" ''
    set -euo pipefail

    keep=${toString cfg.backup.localArchivesToKeep}
    if [ "$keep" -le 0 ] || [ ! -d ${backupDir} ]; then
      exit 0
    fi

    mapfile -t archives < <(find ${backupDir} -maxdepth 1 -type f -name '*.tar.gz' -printf '%T@ %p\n' 2>/dev/null | sort -nr | cut -d' ' -f2-)
    if [ "''${#archives[@]}" -le "$keep" ]; then
      exit 0
    fi

    for archive in "''${archives[@]:$keep}"; do
      echo "pruning old Discourse backup archive: $archive"
      rm -f -- "$archive"
    done
  '';

  waitForHttp = pkgs.writeShellScript "portablevps-discourse-wait-for-http" ''
    set -euo pipefail

    for _ in $(seq 1 ${toString cfg.startupHealthTimeoutSec}); do
      if ${pkgs.curl}/bin/curl --fail --silent --show-error \
        --header ${lib.escapeShellArg "Host: ${cfg.domain}"} \
        http://127.0.0.1:${toString cfg.listenHttp.port}/srv/status >/dev/null; then
        echo "Discourse HTTP status verified"
        exit 0
      fi
      sleep 1
    done

    echo "error: Discourse did not become healthy within ${toString cfg.startupHealthTimeoutSec}s" >&2
    docker logs --tail 120 ${appName} >&2 || true
    exit 1
  '';

  renderAppYml = pkgs.writeShellScript "portablevps-discourse-render-app-yml" ''
    set -euo pipefail

    yaml_quote() {
      printf '"'
      printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
      printf '"'
    }

    emit_env() {
      local key="$1" value="$2"
      printf '  %s: ' "$key"
      yaml_quote "$value"
      printf '\n'
    }

    # shellcheck disable=SC1091
    source ${envPath}

    : "''${DISCOURSE_HOSTNAME:?missing DISCOURSE_HOSTNAME}"
    : "''${DISCOURSE_DEVELOPER_EMAILS:?missing DISCOURSE_DEVELOPER_EMAILS}"

    install -d -m 0750 ${dockerRoot} ${dockerRoot}/containers
    install -d -m 0755 ${sharedRoot} ${backupDir}

    source_hash="$(find ${discourseDockerSrc} -type f -print0 | sort -z | xargs -0 sha256sum | sha256sum | cut -d' ' -f1)"
    old_hash="$(cat ${dockerRoot}/.source-hash 2>/dev/null || true)"
    if [ "$source_hash" != "$old_hash" ] || [ ! -x ${dockerRoot}/launcher ]; then
      tmp="${dockerRoot}.new"
      rm -rf "$tmp"
      mkdir -p "$tmp"
      cp -a ${discourseDockerSrc}/. "$tmp/"
      chmod -R u+w "$tmp"
      rm -rf ${dockerRoot}.old
      if [ -d ${dockerRoot} ]; then mv ${dockerRoot} ${dockerRoot}.old; fi
      mv "$tmp" ${dockerRoot}
      printf '%s\n' "$source_hash" > ${dockerRoot}/.source-hash
    fi

    {
      cat <<YAML
    templates:
      - templates/postgres.template.yml
      - templates/redis.template.yml
      - templates/web.template.yml
      - templates/web.ratelimited.template.yml

    expose:
      - "127.0.0.1:${toString cfg.listenHttp.port}:80"

    params:
      db_default_text_search_config: "pg_catalog.english"
      db_shared_buffers: "${cfg.postgresSharedBuffers}"
      version: "${cfg.discourseVersion}"

    env:
    YAML
      emit_env LC_ALL en_US.UTF-8
      emit_env LANG en_US.UTF-8
      emit_env LANGUAGE en_US.UTF-8
      emit_env UNICORN_WORKERS ${toString cfg.unicornWorkers}
      emit_env DISCOURSE_HOSTNAME "$DISCOURSE_HOSTNAME"
      emit_env DISCOURSE_DEVELOPER_EMAILS "$DISCOURSE_DEVELOPER_EMAILS"
      emit_env DISCOURSE_SERVE_STATIC_ASSETS "true"
      if [ "''${DISCOURSE_SKIP_EMAIL_SETUP:-}" = "1" ]; then
        emit_env DISCOURSE_SKIP_EMAIL_SETUP "1"
      else
        emit_env DISCOURSE_SMTP_ADDRESS "''${DISCOURSE_SMTP_ADDRESS:-}"
        emit_env DISCOURSE_SMTP_PORT "''${DISCOURSE_SMTP_PORT:-}"
        emit_env DISCOURSE_SMTP_ENABLE_START_TLS "''${DISCOURSE_SMTP_ENABLE_START_TLS:-true}"
        emit_env DISCOURSE_NOTIFICATION_EMAIL "''${DISCOURSE_NOTIFICATION_EMAIL:-}"
        emit_env DISCOURSE_SMTP_USER_NAME "''${DISCOURSE_SMTP_USER_NAME:-}"
        emit_env DISCOURSE_SMTP_PASSWORD "''${DISCOURSE_SMTP_PASSWORD:-}"
      fi
      if [ "''${DISCOURSE_OPENID_CONNECT_ENABLED:-}" = "true" ]; then
        emit_env DISCOURSE_OPENID_CONNECT_ENABLED "$DISCOURSE_OPENID_CONNECT_ENABLED"
        emit_env DISCOURSE_OPENID_CONNECT_DISCOVERY_DOCUMENT "$DISCOURSE_OPENID_CONNECT_DISCOVERY_DOCUMENT"
        emit_env DISCOURSE_OPENID_CONNECT_CLIENT_ID "$DISCOURSE_OPENID_CONNECT_CLIENT_ID"
        emit_env DISCOURSE_OPENID_CONNECT_CLIENT_SECRET "$DISCOURSE_OPENID_CONNECT_CLIENT_SECRET"
        emit_env DISCOURSE_OPENID_CONNECT_AUTHORIZE_SCOPE "$DISCOURSE_OPENID_CONNECT_AUTHORIZE_SCOPE"
        emit_env DISCOURSE_OPENID_CONNECT_BUTTON_LABEL "$DISCOURSE_OPENID_CONNECT_BUTTON_LABEL"
        emit_env DISCOURSE_OPENID_CONNECT_FULL_SCREEN_LOGIN "$DISCOURSE_OPENID_CONNECT_FULL_SCREEN_LOGIN"
      fi
      while IFS='=' read -r key value; do
        case "$key" in
          DISCOURSE_*|RAILS_ENV)
            case "$key" in
              DISCOURSE_HOSTNAME|DISCOURSE_DEVELOPER_EMAILS|DISCOURSE_SERVE_STATIC_ASSETS|DISCOURSE_SKIP_EMAIL_SETUP|DISCOURSE_SMTP_*|DISCOURSE_NOTIFICATION_EMAIL|DISCOURSE_OPENID_CONNECT_*|RAILS_ENV) ;;
              *) emit_env "$key" "$value" ;;
            esac
            ;;
        esac
      done < ${envPath}
      cat <<YAML

    volumes:
      - volume:
          host: ${sharedRoot}
          guest: /shared
      - volume:
          host: ${sharedRoot}/log/var-log
          guest: /var/log

    hooks:
      after_code:
        - exec:
            cd: \$home/plugins
            cmd:
              - git clone https://github.com/discourse/docker_manager.git
    YAML
    } > ${appYml}.tmp
    mv ${appYml}.tmp ${appYml}
    chmod 0600 ${appYml}

    ln -sfn ${dataRoot} /var/discourse
  '';

  startScript = pkgs.writeShellScript "portablevps-discourse-start" ''
    set -euo pipefail

    ${renderAppYml}

    cd ${dockerRoot}
    config_hash="$(sha256sum ${appYml} ${dockerRoot}/.source-hash | sha256sum | cut -d' ' -f1)"
    old_config_hash="$(cat ${dockerRoot}/.config-hash 2>/dev/null || true)"

    if ! docker container inspect ${appName} >/dev/null 2>&1 || [ "$config_hash" != "$old_config_hash" ]; then
      ./launcher rebuild ${appName}
      printf '%s\n' "$config_hash" > ${dockerRoot}/.config-hash
    else
      ./launcher start ${appName}
    fi

    if [ -s ${pendingRestore} ]; then
      archive="$(cat ${pendingRestore})"
      echo "restoring Discourse backup archive: $archive"
      docker exec ${appName} discourse enable_restore
      docker exec ${appName} discourse restore "$archive"
      rm -f ${pendingRestore}
      ./launcher rebuild ${appName}
      ${pruneBackupArchives}
    fi

    if ! docker inspect --format '{{.State.Running}}' ${appName} | grep -qx true; then
      docker logs --tail 120 ${appName} >&2 || true
      exit 1
    fi

    ${waitForHttp}
  '';

  stopScript = pkgs.writeShellScript "portablevps-discourse-stop" ''
    set -euo pipefail

    if [ -x ${dockerRoot}/launcher ]; then
      cd ${dockerRoot}
      ./launcher stop ${appName} || true
    else
      docker stop ${appName} || true
    fi
  '';

  drSeed = pkgs.writeShellScript "portablevps-discourse-dr-seed" ''
    set -euo pipefail
    marker="''${1:?marker required}"

    ready=0
    for _ in $(seq 1 180); do
      if docker exec ${appName} rails runner "puts 'ready'" >/dev/null 2>&1; then
        ready=1
        break
      fi
      sleep 1
    done
    if [ "$ready" != 1 ]; then
      echo "error: Discourse Rails runner did not become ready within 180s" >&2
      docker logs --tail 120 ${appName} >&2 || true
      exit 1
    fi

    docker exec --env "PORTABLEVPS_DR_MARKER=$marker" ${appName} rails runner ${lib.escapeShellArg ''
      PluginStore.set("portablevps_dr", "marker", ENV.fetch("PORTABLEVPS_DR_MARKER"))
    ''}
    docker exec ${appName} sh -c 'mkdir -p /shared/uploads/default/original/portablevps-dr && printf "%s\n" "$1" >/shared/uploads/default/original/portablevps-dr/marker.txt' sh "$marker"
  '';

  drVerify = pkgs.writeShellScript "portablevps-discourse-dr-verify" ''
    set -euo pipefail
    marker="''${1:?marker required}"

    healthy=0
    for _ in $(seq 1 180); do
      if ${pkgs.curl}/bin/curl --fail --silent --show-error \
        --header ${lib.escapeShellArg "Host: ${cfg.domain}"} \
        http://127.0.0.1:${toString cfg.listenHttp.port}/srv/status >/dev/null; then
        echo "Discourse HTTP status verified"
        healthy=1
        break
      fi
      sleep 1
    done
    if [ "$healthy" != 1 ]; then
      echo "error: Discourse HTTP status did not become healthy within 180s" >&2
      docker logs --tail 120 ${appName} >&2 || true
      exit 1
    fi

    got_db="$(docker exec ${appName} rails runner 'puts PluginStore.get("portablevps_dr", "marker")' | tail -1)"
    if [ "$got_db" != "$marker" ]; then
      echo "error: Discourse database marker is '$got_db', expected '$marker'" >&2
      exit 1
    fi

    got_file="$(docker exec ${appName} cat /shared/uploads/default/original/portablevps-dr/marker.txt 2>/dev/null || true)"
    if [ "$got_file" != "$marker" ]; then
      echo "error: Discourse shared marker is '$got_file', expected '$marker'" >&2
      exit 1
    fi

    echo "Discourse marker verified: $marker"
  '';
in
{
  options.portablevps.apps.discourse = {
    enable = lib.mkEnableOption "Discourse forum app";

    containerName = lib.mkOption {
      type = lib.types.str;
      default = "app";
      description = "Discourse launcher container name.";
    };

    dataRoot = lib.mkOption {
      type = lib.types.str;
      default = "/data/discourse";
      description = "Host directory for Discourse launcher state, shared files, and backups.";
    };

    discourseDocker = {
      rev = lib.mkOption {
        type = lib.types.str;
        default = "8ee40baf0aae999c07e7b7d0cee2a0bc3309db9a";
        description = "Pinned discourse_docker commit used for launcher operations.";
      };

      hash = lib.mkOption {
        type = lib.types.str;
        default = "sha256-Pp0ZnigwhVW+hCSS1LABuUMKLGcxTKkVYHf5dDgRS9s=";
        description = "Nix hash for the pinned discourse_docker source archive.";
      };
    };

    discourseVersion = lib.mkOption {
      type = lib.types.str;
      default = "latest";
      description = "Discourse git ref rendered as app.yml params.version. Use a tag or commit for repeatable upgrades.";
    };

    startupHealthTimeoutSec = lib.mkOption {
      type = lib.types.ints.positive;
      default = 600;
      description = "Seconds to wait for the Discourse HTTP status endpoint after start, rebuild, or restore.";
    };

    domain = lib.mkOption {
      type = lib.types.str;
      example = "community.example.com";
      description = "Canonical Discourse hostname.";
    };

    developerEmails = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ "admin@example.com" ];
      description = "Initial Discourse developer/admin email list.";
    };

    listenHttp.port = lib.mkOption {
      type = lib.types.port;
      default = 3080;
      description = "Loopback HTTP port published by the launcher-managed Discourse container.";
    };

    unicornWorkers = lib.mkOption {
      type = lib.types.ints.positive;
      default = 2;
      description = "Unicorn worker count rendered into app.yml.";
    };

    postgresSharedBuffers = lib.mkOption {
      type = lib.types.str;
      default = "128MB";
      description = "Bundled PostgreSQL shared_buffers value rendered into app.yml.";
    };

    backup.localArchivesToKeep = lib.mkOption {
      type = lib.types.ints.positive;
      default = 2;
      description = "Number of local Discourse backup tarballs to retain after coordinated backups and restores.";
    };

    smtp = {
      enable = lib.mkEnableOption "SMTP delivery";
      host = lib.mkOption { type = lib.types.str; default = ""; description = "SMTP host."; };
      port = lib.mkOption { type = lib.types.port; default = 587; description = "SMTP port."; };
      startTls = lib.mkOption { type = lib.types.bool; default = true; description = "Enable STARTTLS."; };
      notificationEmail = lib.mkOption { type = lib.types.str; default = "noreply@example.com"; description = "Forum notification From address."; };
      usernameSecret = lib.mkOption { type = lib.types.nullOr lib.types.str; default = null; description = "sops key for SMTP username."; };
      passwordSecret = lib.mkOption { type = lib.types.str; default = "discourse/smtp-password"; description = "sops key for SMTP password."; };
    };

    oidc = {
      enable = lib.mkEnableOption "Discourse OpenID Connect login";
      issuerBaseUrl = lib.mkOption { type = lib.types.str; default = ""; description = "Authentik base URL, e.g. https://auth.example.com."; };
      clientId = lib.mkOption { type = lib.types.str; default = "discourse"; description = "OIDC client id."; };
      clientSecretSecret = lib.mkOption { type = lib.types.str; default = "discourse/oidc-client-secret"; description = "sops key for OIDC client secret."; };
      scopes = lib.mkOption { type = lib.types.listOf lib.types.str; default = [ "openid" "email" "profile" ]; description = "OIDC scopes requested by Discourse."; };
      buttonLabel = lib.mkOption { type = lib.types.str; default = "Log in with Epistola"; description = "Discourse login button label."; };
      fullScreenLogin = lib.mkOption { type = lib.types.bool; default = true; description = "Send anonymous users directly through OIDC."; };
    };

    extraEnv = lib.mkOption { type = lib.types.attrsOf lib.types.str; default = { }; description = "Extra plain environment variables."; };
    extraSecretEnv = lib.mkOption { type = lib.types.attrsOf lib.types.str; default = { }; description = "Extra sops-backed environment variables."; };
  };

  config = lib.mkIf cfg.enable (lib.mkMerge [
    {
      assertions = [
        {
          assertion = !cfg.oidc.enable || cfg.oidc.issuerBaseUrl != "";
          message = "portablevps.apps.discourse.oidc.issuerBaseUrl must be set when OIDC is enabled.";
        }
        {
          assertion = !cfg.smtp.enable || cfg.smtp.host != "";
          message = "portablevps.apps.discourse.smtp.host must be set when SMTP is enabled.";
        }
      ];

      systemd.tmpfiles.rules = [
        "d ${dataRoot} 0750 root root -"
        # Discourse's bundled redis/postgres users need to traverse /shared.
        "d ${sharedRoot} 0755 root root -"
        "d ${backupDir} 0755 root root -"
        "d ${dockerRoot} 0750 root root -"
      ];

      boot.kernel.sysctl."vm.overcommit_memory" = 1;

      virtualisation.docker = {
        enable = true;
        autoPrune = {
          enable = lib.mkDefault true;
          dates = lib.mkDefault "weekly";
        };
      };

      systemd.services.discourse = {
        description = "Discourse forum";
        after = [ "network-online.target" "docker.service" "systemd-tmpfiles-setup.service" ];
        wants = [ "network-online.target" ];
        requires = [ "docker.service" ];
        partOf = [ "apps.target" ];
        wantedBy = lib.optional (!config.portablevps.restoreMode) "apps.target";
        unitConfig.ConditionPathExists = "!/run/portablevps/restore-mode";
        path = [
          pkgs.bash
          pkgs.docker
          pkgs.git
          pkgs.coreutils
          pkgs.findutils
          pkgs.gawk
          pkgs.gnugrep
          pkgs.gnused
          pkgs.nettools
          pkgs.which
        ];
        serviceConfig = {
          Type = "oneshot";
          RemainAfterExit = true;
          TimeoutStartSec = 3600;
          ExecStartPre = "${pkgs.systemd}/bin/systemd-tmpfiles --create";
          ExecStart = "${startScript}";
          ExecStop = "${stopScript}";
        };
      };

      environment.etc."portablevps/dr/seed.d/30-discourse" = {
        source = drSeed;
        mode = "0555";
      };
      environment.etc."portablevps/dr/verify.d/30-discourse" = {
        source = drVerify;
        mode = "0555";
      };
      environment.etc."portablevps/dr/discourse-local-archives-to-keep".text =
        "${toString cfg.backup.localArchivesToKeep}\n";

      portablevps.backups.components.discourse = {
        order = 5;
        paths = [ backupDir ];
        clearBeforeRestore = [ dataRoot ];
        packages = [ pkgs.bash pkgs.docker pkgs.coreutils pkgs.findutils pkgs.gnugrep ];
        afterServices = [ "discourse.service" "docker.service" ];
        wantsServices = [ "discourse.service" "docker.service" ];
        preBackup = ''
          if docker container inspect ${appName} >/dev/null 2>&1 &&
            [ "$(docker inspect -f '{{.State.Running}}' ${appName})" = "true" ]; then
            before="$(find ${backupDir} -maxdepth 1 -type f -name '*.tar.gz' -printf '%T@ %p\n' 2>/dev/null | sort -n | tail -1 | cut -d' ' -f2- || true)"
            docker exec ${appName} discourse backup
            after="$(find ${backupDir} -maxdepth 1 -type f -name '*.tar.gz' -printf '%T@ %p\n' 2>/dev/null | sort -n | tail -1 | cut -d' ' -f2- || true)"
            if [ -z "$after" ] || [ "$after" = "$before" ]; then
              echo "error: Discourse backup did not create a new archive in ${backupDir}" >&2
              exit 70
            fi
            ${pruneBackupArchives}
          else
            echo "error: Discourse container ${appName} is not running; refusing backup" >&2
            exit 70
          fi
        '';
        postBackup = ''
          ${pruneBackupArchives}
        '';
        postRestore = ''
          latest="$(find ${backupDir} -maxdepth 1 -type f -name '*.tar.gz' -printf '%T@ %f\n' 2>/dev/null | sort -n | tail -1 | cut -d' ' -f2- || true)"
          if [ -z "$latest" ]; then
            echo "error: restored Discourse backup archive not found in ${backupDir}" >&2
            exit 70
          fi
          install -d -m 0750 ${dataRoot}
          printf '%s\n' "$latest" > ${pendingRestore}
          ${pruneBackupArchives}
        '';
      };
    }

    (lib.mkIf (!prototype) {
      sops.secrets = lib.genAttrs (lib.unique (lib.attrValues secretEnv)) (_: { });
      sops.templates."portablevps/discourse.env" = {
        path = "/etc/portablevps/discourse.env";
        mode = "0400";
        content = envContent;
      };
    })

    (lib.mkIf prototype {
      environment.etc."portablevps/discourse.env" = {
        mode = "0400";
        text = envContent;
      };
    })
  ]);
}

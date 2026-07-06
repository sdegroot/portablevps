# portablevps app: authentik identity provider.
#
# The GENERIC runtime for authentik (2026.5, Postgres-backed, no Redis): the
# server + worker Quadlet containers on the host network, a dedicated database
# on the platform PostgreSQL, the sops-rendered env file, the media/templates/
# blueprints directories, backup wiring, and a provision oneshot that syncs a
# consumer-provided config-as-code tree (blueprints) and brand assets from the
# Nix store onto the host before the containers start.
#
# This module is deployment-agnostic. A consumer enables it and supplies the
# parts that are theirs: the blueprints + brand assets (as store paths), the
# public domain, SMTP relay, and any extra secret-backed env (e.g. per-app
# OAuth client secrets referenced by their blueprints). Secrets are named by
# their sops key; the module declares them and renders `key=<placeholder>`.
{ config, lib, pkgs, ... }:

let
  cfg = config.portablevps.apps.authentik;

  dataRoot = "/data/authentik";
  mediaDir = "${dataRoot}/media";
  # The file backend resolves MEDIA/public to {base}/media/{schema}; base=/media
  # (env) + schema=public -> /media/media/public inside the container.
  publicMediaDir = "${mediaDir}/media/public";
  templatesDir = "${dataRoot}/custom-templates";
  blueprintsDir = "${dataRoot}/blueprints";

  # authentik runs as uid/gid 1000 (the "ak" user) inside the image.
  akUid = 1000;

  restoreGate = "ConditionPathExists=!/run/portablevps/restore-mode";

  commonContainer = execArg: extraLines: ''
    [Unit]
    Description=authentik ${execArg}
    After=network-online.target postgres.service authentik-provision.service
    Wants=network-online.target
    Requires=postgres.service
    PartOf=apps.target
    ${restoreGate}

    [Container]
    Image=${cfg.image}
    ContainerName=authentik-${execArg}
    Exec=${execArg}
    Network=host
    EnvironmentFile=/etc/portablevps/authentik.env
    Volume=${mediaDir}:/media:Z
    Volume=${templatesDir}:/templates:Z
    ${extraLines}

    [Service]
    Restart=always
    TimeoutStartSec=300

    [Install]
    WantedBy=apps.target
  '';

  # Non-secret env (rendered literally) and secret env (rendered as sops
  # placeholders). The consumer extends the secret set via extraSecretEnv.
  smtpEnabled = cfg.smtp.host != "";

  plainEnv = {
    AUTHENTIK_LISTEN__HTTP = cfg.listenHttp;
    AUTHENTIK_LISTEN__TRUSTED_PROXY_CIDRS = lib.concatStringsSep "," cfg.trustedProxyCidrs;
    AUTHENTIK_POSTGRESQL__HOST = "127.0.0.1";
    AUTHENTIK_POSTGRESQL__PORT = "5432";
    AUTHENTIK_POSTGRESQL__NAME = cfg.postgres.database;
    AUTHENTIK_POSTGRESQL__USER = cfg.postgres.user;
    AUTHENTIK_STORAGE__MEDIA__BACKEND = "file";
    AUTHENTIK_STORAGE__MEDIA__FILE__PATH = "/media";
    AUTHENTIK_BOOTSTRAP_EMAIL = cfg.bootstrapEmail;
  } // lib.optionalAttrs smtpEnabled {
    AUTHENTIK_EMAIL__HOST = cfg.smtp.host;
    AUTHENTIK_EMAIL__PORT = toString cfg.smtp.port;
    AUTHENTIK_EMAIL__USE_TLS = lib.boolToString cfg.smtp.useTls;
    AUTHENTIK_EMAIL__USE_SSL = lib.boolToString cfg.smtp.useSsl;
    AUTHENTIK_EMAIL__TIMEOUT = toString cfg.smtp.timeout;
    AUTHENTIK_EMAIL__FROM = cfg.smtp.from;
  };

  secretEnv = {
    AUTHENTIK_SECRET_KEY = cfg.secretKeySecret;
    AUTHENTIK_POSTGRESQL__PASSWORD = cfg.postgres.passwordSecret;
    AUTHENTIK_BOOTSTRAP_PASSWORD = cfg.bootstrapPasswordSecret;
    AUTHENTIK_BOOTSTRAP_TOKEN = cfg.bootstrapTokenSecret;
  }
  // lib.optionalAttrs (smtpEnabled && cfg.smtp.usernameSecret != null) {
    AUTHENTIK_EMAIL__USERNAME = cfg.smtp.usernameSecret;
  }
  // lib.optionalAttrs (smtpEnabled && cfg.smtp.passwordSecret != null) {
    AUTHENTIK_EMAIL__PASSWORD = cfg.smtp.passwordSecret;
  }
  // cfg.extraSecretEnv;

  envContent =
    lib.concatStringsSep "\n" (
      (lib.mapAttrsToList (k: v: "${k}=${v}") plainEnv)
      ++ (lib.mapAttrsToList (k: secret: "${k}=${config.sops.placeholder.${secret}}") secretEnv)
    ) + "\n";
in
{
  options.portablevps.apps.authentik = {
    enable = lib.mkEnableOption "authentik identity provider app";

    image = lib.mkOption {
      type = lib.types.str;
      # Bump tag@digest together (Renovate-managed in the reference).
      default = "ghcr.io/goauthentik/server:2026.5.2";
      description = "authentik container image (pin tag@digest in production).";
    };

    listenHttp = lib.mkOption {
      type = lib.types.str;
      default = "127.0.0.1:9000";
      description = "Address authentik's HTTP server binds (fronted by the proxy).";
    };

    trustedProxyCidrs = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ "127.0.0.0/8" "10.0.0.0/8" "172.16.0.0/12" "192.168.0.0/16" "100.64.0.0/10" ];
      description = ''
        CIDRs authentik trusts for X-Forwarded-* headers. Includes the NetBird
        CGNAT range (100.64.0.0/10) so a mesh-fronting proxy's forwarded
        X-Forwarded-Proto is honoured (otherwise logins loop http<->https).
      '';
    };

    bootstrapEmail = lib.mkOption {
      type = lib.types.str;
      default = "admin@example.com";
      description = "Email for the bootstrap akadmin user (first run only).";
    };

    postgres = {
      database = lib.mkOption {
        type = lib.types.str;
        default = "authentik";
        description = "Dedicated PostgreSQL database for authentik.";
      };
      user = lib.mkOption {
        type = lib.types.str;
        default = "authentik";
        description = "PostgreSQL role authentik connects as (the container superuser).";
      };
      maxConnections = lib.mkOption {
        type = lib.types.ints.positive;
        default = 200;
        description = "max_connections (authentik pools heavily without Redis).";
      };
      passwordSecret = lib.mkOption {
        type = lib.types.str;
        default = "postgres/password";
        description = "sops key holding the PostgreSQL password.";
      };
    };

    secretKeySecret = lib.mkOption {
      type = lib.types.str;
      default = "authentik/secret-key";
      description = "sops key for AUTHENTIK_SECRET_KEY.";
    };
    bootstrapPasswordSecret = lib.mkOption {
      type = lib.types.str;
      default = "authentik/bootstrap-password";
      description = "sops key for the bootstrap akadmin password.";
    };
    bootstrapTokenSecret = lib.mkOption {
      type = lib.types.str;
      default = "authentik/bootstrap-token";
      description = "sops key for the bootstrap akadmin API token.";
    };

    smtp = {
      host = lib.mkOption {
        type = lib.types.str;
        default = "";
        description = "SMTP relay host. Empty leaves email inert.";
      };
      port = lib.mkOption {
        type = lib.types.port;
        default = 587;
        description = "SMTP port (587 = STARTTLS, 465 = implicit TLS).";
      };
      useTls = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Use STARTTLS.";
      };
      useSsl = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = "Use implicit TLS (with port 465).";
      };
      timeout = lib.mkOption {
        type = lib.types.ints.positive;
        default = 10;
        description = "SMTP timeout in seconds.";
      };
      from = lib.mkOption {
        type = lib.types.str;
        default = "";
        description = "From address on outgoing mail.";
      };
      usernameSecret = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "sops key for the SMTP username (null omits SMTP auth).";
      };
      passwordSecret = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "sops key for the SMTP password (null omits SMTP auth).";
      };
    };

    blueprintsDir = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      description = ''
        A directory of authentik config-as-code blueprints (*.yaml). Synced onto
        the host and mounted read-only into the worker at /blueprints/custom.
        Consumer-owned; null runs authentik with only its bundled blueprints.
      '';
    };

    assetsDir = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      description = ''
        A directory of brand assets (logo/favicon/flow backgrounds) placed into
        the managed file store and referenced by name from a branding blueprint.
      '';
    };

    extraSecretEnv = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = { };
      example = { AUTHENTIK_BP_GRAFANA_CLIENT_SECRET = "authentik/bp-grafana-client-secret"; };
      description = ''
        Extra environment variables backed by sops secrets, as
        ENV_VAR = "<sops key>". Each key is declared as a sops secret and
        rendered as ENV_VAR=<placeholder>. Use for OAuth client secrets the
        consumer's blueprints reference via the !Env tag.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    # authentik owns the platform postgres on this box: a dedicated database
    # with a raised connection ceiling.
    portablevps.postgres = {
      database = lib.mkDefault cfg.postgres.database;
      user = lib.mkDefault cfg.postgres.user;
      maxConnections = lib.mkDefault cfg.postgres.maxConnections;
      containerName = lib.mkDefault "postgres-authentik";
    };

    systemd.tmpfiles.rules = [
      "d ${dataRoot} 0750 ${toString akUid} ${toString akUid} -"
      "d ${mediaDir} 0750 ${toString akUid} ${toString akUid} -"
      "d ${mediaDir}/media 0750 ${toString akUid} ${toString akUid} -"
      "d ${publicMediaDir} 0750 ${toString akUid} ${toString akUid} -"
      "d ${templatesDir} 0750 ${toString akUid} ${toString akUid} -"
      "d ${blueprintsDir} 0750 ${toString akUid} ${toString akUid} -"
    ];

    # Sync consumer-provided blueprints + brand assets from the Nix store onto
    # the host before the containers start. Store-path changes (new closure)
    # re-sync; owned by the container uid.
    systemd.services.authentik-provision = {
      description = "Sync authentik blueprints and brand assets from the Nix store";
      wantedBy = [ "multi-user.target" ];
      before = [ "authentik-server.service" "authentik-worker.service" ];
      after = [ "systemd-tmpfiles-setup.service" ];
      unitConfig.ConditionPathExists = "!/run/portablevps/restore-mode";
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
      };
      path = [ pkgs.rsync pkgs.coreutils ];
      script = ''
        install -d -o ${toString akUid} -g ${toString akUid} -m 0750 ${blueprintsDir} ${publicMediaDir}
      '' + lib.optionalString (cfg.blueprintsDir != null) ''
        rsync -a --delete --chown=${toString akUid}:${toString akUid} ${cfg.blueprintsDir}/ ${blueprintsDir}/
      '' + lib.optionalString (cfg.assetsDir != null) ''
        rsync -a --chown=${toString akUid}:${toString akUid} ${cfg.assetsDir}/ ${publicMediaDir}/
      '';
    };

    # Declare every referenced sops secret (core + smtp + consumer extras).
    sops.secrets = lib.genAttrs (lib.unique (lib.attrValues secretEnv)) (_: { });

    sops.templates."portablevps/authentik.env" = {
      path = "/etc/portablevps/authentik.env";
      mode = "0400";
      content = envContent;
    };

    environment.etc."containers/systemd/authentik-server.container".text =
      commonContainer "server" "";

    # The worker shares host networking + the env file with the server, so it
    # would otherwise bind the server's AUTHENTIK_LISTEN__HTTP/METRICS ports
    # (:9000/:9300) first and leave the server unable to listen — the proxy then
    # hits the worker's minimal HTTP server and every page is a blank 200.
    # Override the worker's listen ports so only the server owns :9000/:9300.
    environment.etc."containers/systemd/authentik-worker.container".text =
      commonContainer "worker" (lib.concatStringsSep "\n    " [
        "Volume=${blueprintsDir}:/blueprints/custom:ro,Z"
        "Environment=AUTHENTIK_LISTEN__HTTP=127.0.0.1:9001"
        "Environment=AUTHENTIK_LISTEN__METRICS=127.0.0.1:9301"
      ]);

    # Back up authentik's file state (uploaded media, custom templates). The
    # database is covered by the platform PostgreSQL online backup. Blueprints
    # are regenerated from the Nix store, so they are not backed up.
    portablevps.backups.components.authentik = {
      order = 30;
      paths = [ mediaDir templatesDir ];
      clearBeforeRestore = [ mediaDir templatesDir ];
    };
  };
}

# portablevps app: Forgejo code hosting.
#
# Runs Forgejo as a stateful Quadlet container with PostgreSQL for relational
# state and /data/forgejo for repositories, attachments, avatars, LFS, and
# rendered app state. Login is expected to be OIDC-first, with a local admin kept
# as break-glass.
{ config, lib, pkgs, ... }:

let
  cfg = config.portablevps.apps.forgejo;
  prototype = config.portablevps.secrets.allowPrototypeDefaults;

  dataRoot = cfg.dataRoot;
  forgejoUid = 1000;
  restoreGate = "ConditionPathExists=!/run/portablevps/restore-mode";
  envPath =
    if prototype
    then "/etc/portablevps/forgejo.env"
    else config.sops.templates."portablevps/forgejo.env".path;

  plainEnv = {
    USER_UID = toString forgejoUid;
    USER_GID = toString forgejoUid;
    FORGEJO__database__DB_TYPE = "postgres";
    FORGEJO__database__HOST = "127.0.0.1:5432";
    FORGEJO__database__NAME = cfg.postgres.database;
    FORGEJO__database__USER = cfg.postgres.user;
    FORGEJO__server__APP_DATA_PATH = "/data/gitea";
    FORGEJO__server__DOMAIN = cfg.domain;
    FORGEJO__server__ROOT_URL = cfg.rootUrl;
    FORGEJO__server__HTTP_ADDR = cfg.listenHttp.host;
    FORGEJO__server__HTTP_PORT = toString cfg.listenHttp.port;
    FORGEJO__server__SSH_DOMAIN = cfg.ssh.domain;
    FORGEJO__server__SSH_PORT = toString cfg.ssh.port;
    FORGEJO__server__SSH_LISTEN_HOST = cfg.ssh.listenHost;
    FORGEJO__server__SSH_LISTEN_PORT = toString cfg.ssh.listenPort;
    FORGEJO__server__START_SSH_SERVER = lib.boolToString cfg.ssh.enable;
    FORGEJO__server__DISABLE_SSH = lib.boolToString (!cfg.ssh.enable);
    FORGEJO__service__DISABLE_REGISTRATION = lib.boolToString cfg.disableRegistration;
    FORGEJO__service__ALLOW_ONLY_EXTERNAL_REGISTRATION = "false";
    FORGEJO__service__ENABLE_NOTIFY_MAIL = "false";
    FORGEJO__openid__ENABLE_OPENID_SIGNIN = "false";
    FORGEJO__openid__ENABLE_OPENID_SIGNUP = "false";
    FORGEJO__security__INSTALL_LOCK = "true";
    FORGEJO__api__ENABLE_SWAGGER = "false";
    FORGEJO__actions__ENABLED = lib.boolToString cfg.actions.enable;
  } // cfg.extraEnv;

  secretEnv = {
    FORGEJO__database__PASSWD = cfg.postgres.passwordSecret;
    FORGEJO__security__SECRET_KEY = cfg.secretKeySecret;
    FORGEJO__security__INTERNAL_TOKEN = cfg.internalTokenSecret;
  };

  demoSecret = key:
    if key == cfg.postgres.passwordSecret then "demo-password"
    else if key == cfg.secretKeySecret then lib.concatStrings (lib.genList (_: "0") 64)
    else if key == cfg.internalTokenSecret then lib.concatStrings (lib.genList (_: "1") 64)
    else if key == cfg.admin.passwordSecret then "demo-admin-password"
    else "demo";

  renderSecret = key:
    if prototype then demoSecret key else config.sops.placeholder.${key};

  envContent =
    lib.concatStringsSep "\n" (
      (lib.mapAttrsToList (k: v: "${k}=${v}") plainEnv)
      ++ (lib.mapAttrsToList (k: secret: "${k}=${renderSecret secret}") secretEnv)
    ) + "\n";

  oauthEnabled = cfg.oidc.enable && cfg.oidc.issuerBaseUrl != "";
  forgejoCli = "podman exec --user ${toString forgejoUid}:${toString forgejoUid} forgejo forgejo";
in
{
  options.portablevps.apps.forgejo = {
    enable = lib.mkEnableOption "Forgejo code hosting app";

    image = lib.mkOption {
      type = lib.types.str;
      default = "codeberg.org/forgejo/forgejo:15.0.4";
      description = "Forgejo container image.";
    };

    dataRoot = lib.mkOption {
      type = lib.types.str;
      default = "/data/forgejo";
      description = "Host directory mounted as /data in the Forgejo container.";
    };

    domain = lib.mkOption {
      type = lib.types.str;
      example = "code.epistola.app";
      description = "Canonical Forgejo web domain.";
    };

    rootUrl = lib.mkOption {
      type = lib.types.str;
      example = "https://code.epistola.app/";
      description = "Canonical Forgejo ROOT_URL.";
    };

    listenHttp = {
      host = lib.mkOption {
        type = lib.types.str;
        default = "127.0.0.1";
        description = "Loopback address Forgejo HTTP binds on the host network.";
      };
      port = lib.mkOption {
        type = lib.types.port;
        default = 3000;
        description = "Loopback HTTP port fronted by the proxy.";
      };
    };

    ssh = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Enable Forgejo's built-in SSH server.";
      };
      domain = lib.mkOption {
        type = lib.types.str;
        example = "code.int.epistola.app";
        description = "SSH clone domain advertised by Forgejo.";
      };
      port = lib.mkOption {
        type = lib.types.port;
        default = 2222;
        description = "SSH clone port advertised by Forgejo.";
      };
      listenHost = lib.mkOption {
        type = lib.types.str;
        default = "127.0.0.1";
        description = "Host address the built-in SSH server listens on.";
      };
      listenPort = lib.mkOption {
        type = lib.types.port;
        default = 2222;
        description = "Host port the built-in SSH server listens on.";
      };
    };

    disableRegistration = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Disable open local registration.";
    };

    postgres = {
      database = lib.mkOption {
        type = lib.types.str;
        default = "forgejo";
        description = "Dedicated PostgreSQL database for Forgejo.";
      };
      user = lib.mkOption {
        type = lib.types.str;
        default = "forgejo";
        description = "PostgreSQL role Forgejo connects as.";
      };
      passwordSecret = lib.mkOption {
        type = lib.types.str;
        default = "postgres/password";
        description = "sops key holding the PostgreSQL password.";
      };
    };

    secretKeySecret = lib.mkOption {
      type = lib.types.str;
      default = "forgejo/secret-key";
      description = "sops key for Forgejo's SECRET_KEY.";
    };

    internalTokenSecret = lib.mkOption {
      type = lib.types.str;
      default = "forgejo/internal-token";
      description = "sops key for Forgejo's INTERNAL_TOKEN.";
    };

    admin = {
      username = lib.mkOption {
        type = lib.types.str;
        default = "admin";
        description = "Break-glass local admin username.";
      };
      email = lib.mkOption {
        type = lib.types.str;
        default = "admin@example.com";
        description = "Break-glass local admin email.";
      };
      passwordSecret = lib.mkOption {
        type = lib.types.str;
        default = "forgejo/admin-password";
        description = "sops key holding the break-glass local admin password.";
      };
    };

    oidc = {
      enable = lib.mkEnableOption "OIDC login source";
      name = lib.mkOption {
        type = lib.types.str;
        default = "authentik";
        description = "Forgejo authentication source name.";
      };
      provider = lib.mkOption {
        type = lib.types.str;
        default = "openidConnect";
        description = "Forgejo OAuth provider type.";
      };
      clientId = lib.mkOption {
        type = lib.types.str;
        default = "forgejo";
        description = "OIDC client id.";
      };
      clientSecretSecret = lib.mkOption {
        type = lib.types.str;
        default = "forgejo/oauth-client-secret";
        description = "sops key holding the OIDC client secret.";
      };
      issuerBaseUrl = lib.mkOption {
        type = lib.types.str;
        default = "";
        description = "OIDC issuer base URL used for discovery.";
      };
      scopes = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ "openid" "email" "profile" "groups" ];
        description = "OIDC scopes requested by Forgejo.";
      };
    };

    actions.enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Enable Forgejo Actions scheduling. Jobs still require separate runners.";
    };

    extraEnv = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = { };
      description = "Extra plain FORGEJO__/Gitea environment overrides.";
    };
  };

  config = lib.mkIf cfg.enable (lib.mkMerge [
    {
      portablevps.postgres = {
        database = lib.mkDefault cfg.postgres.database;
        user = lib.mkDefault cfg.postgres.user;
        containerName = lib.mkDefault "postgres-forgejo";
      };

      systemd.tmpfiles.rules = [
        "d ${dataRoot} 0750 ${toString forgejoUid} ${toString forgejoUid} -"
      ];

      environment.etc."containers/systemd/forgejo.container".text = ''
        [Unit]
        Description=Forgejo code hosting
        After=network-online.target postgres.service
        Wants=network-online.target
        Requires=postgres.service
        PartOf=apps.target
        ${restoreGate}

        [Container]
        Image=${cfg.image}
        ContainerName=forgejo
        Network=host
        EnvironmentFile=${envPath}
        Volume=${dataRoot}:/data:Z
        HealthCmd=wget -q --spider http://${cfg.listenHttp.host}:${toString cfg.listenHttp.port}/api/healthz
        HealthStartPeriod=60s
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

      systemd.services.forgejo-provision = {
        description = "Provision Forgejo break-glass admin and OIDC login source";
        wantedBy = lib.optional (!config.portablevps.restoreMode) "apps.target";
        after = [ "forgejo.service" ];
        wants = [ "forgejo.service" ];
        partOf = [ "apps.target" ];
        path = [ pkgs.coreutils pkgs.gnugrep pkgs.podman ];
        unitConfig.ConditionPathExists = "!/run/portablevps/restore-mode";
        serviceConfig = {
          Type = "oneshot";
          RemainAfterExit = true;
          TimeoutStartSec = 180;
        };
        script = ''
          set -eu
          for _ in $(seq 1 120); do
            if ${forgejoCli} admin user list >/dev/null 2>&1; then
              break
            fi
            sleep 1
          done

          admin_password="$(${pkgs.coreutils}/bin/tr -d '\n' < ${if prototype then pkgs.writeText "forgejo-demo-admin-password" (demoSecret cfg.admin.passwordSecret) else config.sops.secrets.${cfg.admin.passwordSecret}.path})"
          if ! ${forgejoCli} admin user list | grep -Fq -- ${lib.escapeShellArg cfg.admin.username}; then
            ${forgejoCli} admin user create \
              --admin \
              --username ${lib.escapeShellArg cfg.admin.username} \
              --password "$admin_password" \
              --email ${lib.escapeShellArg cfg.admin.email} \
              --must-change-password=false || true
          else
            ${forgejoCli} admin user change-password \
              --username ${lib.escapeShellArg cfg.admin.username} \
              --password "$admin_password" || true
          fi
        '' + lib.optionalString oauthEnabled ''

          oauth_secret="$(${pkgs.coreutils}/bin/tr -d '\n' < ${if prototype then pkgs.writeText "forgejo-demo-oauth-secret" (demoSecret cfg.oidc.clientSecretSecret) else config.sops.secrets.${cfg.oidc.clientSecretSecret}.path})"
          if ! ${forgejoCli} admin auth list | grep -Fq ${lib.escapeShellArg cfg.oidc.name}; then
            ${forgejoCli} admin auth add-oauth \
              --name ${lib.escapeShellArg cfg.oidc.name} \
              --provider ${lib.escapeShellArg cfg.oidc.provider} \
              --key ${lib.escapeShellArg cfg.oidc.clientId} \
              --secret "$oauth_secret" \
              --auto-discover-url ${lib.escapeShellArg "${cfg.oidc.issuerBaseUrl}/application/o/${cfg.oidc.clientId}/.well-known/openid-configuration"} \
              --scopes ${lib.escapeShellArg (lib.concatStringsSep "," cfg.oidc.scopes)} || true
          fi
        '';
      };

      portablevps.backups.components.forgejo = {
        order = 30;
        paths = [ dataRoot ];
        clearBeforeRestore = [ dataRoot ];
      };
    }

    (lib.mkIf (!prototype) {
      sops.secrets = {
        ${cfg.secretKeySecret} = { };
        ${cfg.internalTokenSecret} = { };
        ${cfg.admin.passwordSecret} = { };
      } // lib.optionalAttrs oauthEnabled { ${cfg.oidc.clientSecretSecret} = { }; };

      sops.templates."portablevps/forgejo.env" = {
        path = "/etc/portablevps/forgejo.env";
        mode = "0400";
        content = envContent;
      };
    })

    (lib.mkIf prototype {
      environment.etc."portablevps/forgejo.env" = {
        mode = "0400";
        text = envContent;
      };
    })
  ]);
}

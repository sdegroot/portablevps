# portablevps app: authentik identity provider.
#
# The GENERIC runtime for authentik (2026.5, Postgres-backed, no Redis): the
# server + worker Quadlet containers on the host network, a dedicated database
# on the platform PostgreSQL, the sops-rendered env file, the media/templates/
# blueprints directories, backup wiring, and a provision oneshot that syncs a
# consumer-provided config-as-code tree (blueprints) and brand assets from the
# Nix store onto the host before the containers start.
#
# Consumer-supplied content reaches the container two different ways, and the
# split is deliberate:
#   * blueprints + brand assets are SYNCED onto the host, because authentik owns
#     them afterwards (it re-reads blueprints itself and serves media through its
#     file backend).
#   * email templates are BIND-MOUNTED READ-ONLY FROM THE STORE, because Django
#     caches them in-process and they only take effect on a container restart —
#     which requires their hash to be in the .container file, and requires the
#     content to already be in place when that restart happens. See
#     templatesVolume.
#
# This module is deployment-agnostic. A consumer enables it and supplies the
# parts that are theirs: the blueprints + brand assets + email templates (as
# store paths), the public domain, SMTP relay, and any extra secret-backed env
# (e.g. per-app OAuth client secrets referenced by their blueprints). Secrets are
# named by their sops key; the module declares them and renders
# `key=<placeholder>`.
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

  # Where /templates comes from. Consumer-supplied templates are mounted STRAIGHT
  # FROM THE NIX STORE rather than synced onto the host like the blueprints, and
  # the difference is load-bearing:
  #
  #  * Django compiles a template once and caches it in-process for the life of
  #    the process (authentik sets APP_DIRS without OPTIONS.loaders, so Django
  #    wraps the loaders in cached.Loader unconditionally — no DEBUG escape). So,
  #    unlike a blueprint (which the worker re-reads on its own), an edited
  #    template only takes effect when the container RESTARTS.
  #  * The generic quadlet restarter (modules/runtime/podman.nix) bounces a
  #    container when its .container file changes. Naming the store path here puts
  #    the templates' content hash INTO that file, so an edit — and only an edit —
  #    bounces both containers.
  #  * It also has to be a store path, not a synced copy. That restarter is an
  #    ACTIVATION SCRIPT, and switch-to-configuration runs activation scripts
  #    BEFORE it restarts changed units — so a sync done by authentik-provision
  #    would land AFTER the bounce it is supposed to feed, and the new template
  #    would not take effect until the next switch. A store path is already there,
  #    immutable, before anything starts: there is no window to lose.
  #
  # `ro` and never `Z`: :Z asks podman to relabel the SOURCE for exclusive use by
  # this container, which on a read-only, globally-shared store path is both wrong
  # and impossible. Store paths are world-readable, which is all authentik (uid
  # 1000) needs — get_template_choices() skips anything not R_OK.
  #
  # With no consumer templates, /templates stays the host dir: unmanaged, and the
  # only mode in which an admin dropping a file there means anything.
  templatesVolume =
    if cfg.emailTemplatesDir != null
    then "${cfg.emailTemplatesDir}:/templates:ro"
    else "${templatesDir}:/templates:Z";

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
    Volume=${templatesVolume}
    # authentik ships `ak healthcheck` (checks DB + broker connectivity); the
    # upstream compose uses it for both server and worker. On failure podman
    # kills the container so systemd's Restart=always revives a hung (not just
    # crashed) process. StartPeriod covers first-boot migrations run by the
    # worker. Killing (not podman-internal restart) is the correct action under
    # systemd/Quadlet, which owns the restart.
    HealthCmd=ak healthcheck
    HealthStartPeriod=120s
    HealthInterval=30s
    HealthTimeout=30s
    HealthRetries=3
    HealthOnFailure=kill
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

  # Local-VM / prototype mode: no sops, no real host key. Render deterministic
  # demo values instead of sops placeholders so the app boots for local
  # backup/restore validation. The postgres password must match the postgres
  # module's prototype default (demo-password) so authentik can connect.
  prototype = config.portablevps.secrets.allowPrototypeDefaults;

  demoSecret = key:
    if key == cfg.postgres.passwordSecret then "demo-password"
    else if key == cfg.secretKeySecret then lib.concatStrings (lib.genList (_: "0") 64)
    else if key == cfg.bootstrapPasswordSecret then "demo-admin-password"
    else "demo";

  renderSecret = key:
    if prototype then demoSecret key else config.sops.placeholder.${key};

  envContent =
    lib.concatStringsSep "\n" (
      (lib.mapAttrsToList (k: v: "${k}=${v}") plainEnv)
      ++ (lib.mapAttrsToList (k: secret: "${k}=${renderSecret secret}") secretEnv)
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

    emailTemplatesDir = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      example = lib.literalExpression "./templates";
      description = ''
        A directory of custom Django email templates, bind-mounted READ-ONLY from
        the Nix store onto the container's template dir (`/templates`, authentik's
        `email.template_dir` default — note the config key is `email.template_dir`,
        i.e. `AUTHENTIK_EMAIL__TEMPLATE_DIR`, not `AUTHENTIK_TEMPLATE_DIR`, which
        nothing reads). authentik globs `**/*.html` under it and offers each hit to
        an email stage's `template` field by its path RELATIVE to the dir — so
        `templates/email/foo.html` here is referenced as `email/foo.html` from a
        blueprint. Consumer-owned; null leaves authentik on its bundled set and
        `/templates` on the (unmanaged, backed-up) host dir instead.

        Setting this makes the templates immutable and store-derived, like the
        blueprints: they are not backed up, and a file dropped into the host dir by
        hand is ignored.

        Three upstream footguns a consumer's tree must respect:
          * A stage's `template` is validated against what is on disk AT APPLY
            TIME, so a blueprint naming a missing template fails outright
            ("Invalid template ... specified"). Mounting from the store means the
            file is always in place before anything starts.
          * authentik derives the plaintext sibling with a naive
            `template_name.replace("html", "txt")` — every occurrence, not just
            the extension. NEVER put "html" in a template's name or path beyond
            the extension itself (`html_digest.html` would look for
            `txt_digest.txt`). Ship a matching .txt next to each .html; a missing
            one silently yields an HTML-only mail with an empty body.
          * The filesystem loader OUTRANKS the app-directories loader, so naming a
            file after a bundled template (`email/password_reset.html`) silently
            SHADOWS it everywhere rather than adding a choice. Use distinct names
            unless shadowing is the intent.
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
      # --checksum is REQUIRED: Nix store sources all have mtime 1970, and a
      # same-length edit (e.g. swapping "explicit-consent" -> "implicit-consent",
      # both 16 chars) leaves file size AND mtime unchanged, so a plain `rsync -a`
      # (size+mtime quick-check) silently SKIPS the change and the blueprint never
      # updates. Compare by content instead. (Small files; cost is negligible.)
      # Templates are absent here on purpose — they are bind-mounted from the
      # store (see templatesVolume), so there is nothing to sync and no ordering
      # to get right.
      script = ''
        install -d -o ${toString akUid} -g ${toString akUid} -m 0750 ${blueprintsDir} ${publicMediaDir}
      '' + lib.optionalString (cfg.blueprintsDir != null) ''
        rsync -a --checksum --delete --chown=${toString akUid}:${toString akUid} ${cfg.blueprintsDir}/ ${blueprintsDir}/
      '' + lib.optionalString (cfg.assetsDir != null) ''
        rsync -a --checksum --chown=${toString akUid}:${toString akUid} ${cfg.assetsDir}/ ${publicMediaDir}/
      '';
    };

    # Normal mode: declare every referenced sops secret (core + smtp + consumer
    # extras) and render the env file from sops. Prototype/local-VM mode: no
    # sops — write the env file with demo values directly.
    sops.secrets = lib.mkIf (!prototype)
      (lib.genAttrs (lib.unique (lib.attrValues secretEnv)) (_: { }));

    sops.templates = lib.mkIf (!prototype) {
      "portablevps/authentik.env" = {
        path = "/etc/portablevps/authentik.env";
        mode = "0400";
        content = envContent;
      };
    };

    environment.etc."portablevps/authentik.env" = lib.mkIf prototype {
      mode = "0400";
      text = envContent;
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

    # Back up authentik's file state (uploaded media, and custom templates only
    # when they are host-managed). The database is covered by the platform
    # PostgreSQL online backup. Blueprints are regenerated from the Nix store, so
    # they are not backed up — and store-mounted templates are store-derived for
    # exactly the same reason, so they aren't either. When emailTemplatesDir is
    # null the host dir is the source of truth and does need covering.
    portablevps.backups.components.authentik =
      let templateState = lib.optional (cfg.emailTemplatesDir == null) templatesDir;
      in {
        order = 30;
        paths = [ mediaDir ] ++ templateState;
        clearBeforeRestore = [ mediaDir ] ++ templateState;
      };
  };
}

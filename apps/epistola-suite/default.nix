# portablevps app: epistola-suite (the Epistola document-generation server).
#
# A generic, deployment-agnostic runtime for the epistola-suite Spring Boot
# container: a single podman Quadlet on the host network, bound to loopback and
# fronted by the portablevps proxy, connected to the platform PostgreSQL. It is
# reusable — a client who prefers plain containers over Kubernetes can run
# epistola on portablevps by enabling this app.
#
# The app does NOT claim the PostgreSQL instance (unlike authentik): the server
# owns `portablevps.postgres` centrally, because a single box may host several
# apps sharing one cluster (e.g. the demo box also runs valtimo). This module
# only takes datasource connection settings.
#
# OIDC is optional and provider-neutral. epistola-suite activates OIDC purely
# from the presence of `spring.security.oauth2.*` — no Spring profile needed
# (see epistola-suite docs/authentik-setup.md). Both the client issuer-uri and
# the resource-server jwt issuer-uri must be set to the same value, with a
# trailing slash on the authentik issuer.
#
# Private ghcr pulls: set `pullAuthUser` (username, not a secret) + a
# `pullAuthSecret` sops key holding ONLY a classic PAT with `read:packages`
# (same contract as apps/website).
{ config, lib, pkgs, ... }:

let
  cfg = config.portablevps.apps.epistola-suite;
  prototype = config.portablevps.secrets.allowPrototypeDefaults;

  registry = builtins.head (lib.splitString "/" cfg.image); # e.g. ghcr.io
  authFile = "/etc/portablevps/${cfg.containerName}-registry-auth.json";
  useAuth = cfg.pullAuthSecret != null && !prototype;

  restoreGate = "ConditionPathExists=!/run/portablevps/restore-mode";

  jdbcUrl = "jdbc:postgresql://${cfg.database.host}:${toString cfg.database.port}/${cfg.database.name}";

  # OIDC is disabled in prototype/local-VM mode: with the issuer unreachable,
  # Spring's startup OIDC discovery would fail and the container would never
  # boot, defeating the local backup/restore harness.
  oidcEnabled = cfg.oidc.enable && !prototype;
  reg = lib.toUpper cfg.oidc.registrationId;

  # Plain (non-secret) env, rendered literally.
  plainEnv = {
    SPRING_PROFILES_ACTIVE = cfg.profiles;
    # Bind loopback only; the proxy fronts it on 127.0.0.1:${toString cfg.port}.
    SERVER_ADDRESS = "127.0.0.1";
    SERVER_PORT = toString cfg.port;
    # Behind the Traefik proxy: honour X-Forwarded-Proto/Host so Spring builds
    # the OIDC redirect_uri with the external https host (else it uses the
    # internal http request and authentik rejects a mismatching redirect_uri).
    # NB: Spring relaxed binding removes dashes — server.forward-headers-strategy
    # is SERVER_FORWARDHEADERSSTRATEGY, NOT SERVER_FORWARD_HEADERS_STRATEGY.
    SERVER_FORWARDHEADERSSTRATEGY = "framework";
    SPRING_DATASOURCE_URL = jdbcUrl;
    SPRING_DATASOURCE_USERNAME = cfg.database.user;
  } // lib.optionalAttrs oidcEnabled {
    "SPRING_SECURITY_OAUTH2_CLIENT_REGISTRATION_${reg}_CLIENTID" = cfg.oidc.clientId;
    "SPRING_SECURITY_OAUTH2_CLIENT_REGISTRATION_${reg}_SCOPE" = cfg.oidc.scope;
    "SPRING_SECURITY_OAUTH2_CLIENT_PROVIDER_${reg}_ISSUERURI" = cfg.oidc.issuerUri;
    SPRING_SECURITY_OAUTH2_RESOURCESERVER_JWT_ISSUERURI = cfg.oidc.issuerUri;
    EPISTOLA_AUTH_AUTOPROVISION = lib.boolToString cfg.oidc.autoProvision;
  } // lib.optionalAttrs (oidcEnabled && cfg.oidc.userNameAttribute != null) {
    # Which claim becomes the Spring principal name (shown in the UI). authentik
    # defaults to `sub` (an opaque id); set `preferred_username` to match
    # Keycloak's default and show a real username. Relaxed binding removes
    # dashes: user-name-attribute -> USERNAMEATTRIBUTE.
    "SPRING_SECURITY_OAUTH2_CLIENT_PROVIDER_${reg}_USERNAMEATTRIBUTE" = cfg.oidc.userNameAttribute;
  } // cfg.extraEnv;

  # Secret env, rendered as sops placeholders (or demo values in prototype).
  secretEnv = {
    SPRING_DATASOURCE_PASSWORD = cfg.database.passwordSecret;
  } // lib.optionalAttrs oidcEnabled {
    "SPRING_SECURITY_OAUTH2_CLIENT_REGISTRATION_${reg}_CLIENTSECRET" = cfg.oidc.clientSecretSecret;
  } // cfg.extraSecretEnv;

  # Prototype/local-VM mode: the postgres password must match the postgres
  # module's prototype default so the app can connect.
  demoSecret = key:
    if key == cfg.database.passwordSecret then "demo-password"
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
  options.portablevps.apps.epistola-suite = {
    enable = lib.mkEnableOption "the epistola-suite application container";

    image = lib.mkOption {
      type = lib.types.str;
      example = "ghcr.io/epistola-app/epistola-suite:1.0.0-RC2";
      description = "Pinned epistola-suite image (use an immutable tag/digest, not :latest).";
    };

    containerName = lib.mkOption {
      type = lib.types.str;
      default = "epistola-suite";
      description = "Podman container/unit name.";
    };

    port = lib.mkOption {
      type = lib.types.port;
      default = 4000;
      description = ''
        Port epistola-suite listens on (its app default is 4000; under the
        `demo` profile the actuator is served on the same port, only `prod`
        moves it to 4040). Bound to 127.0.0.1 and fronted by the proxy.
      '';
    };

    profiles = lib.mkOption {
      type = lib.types.str;
      default = "demo";
      example = "demo,localauth";
      description = ''
        SPRING_PROFILES_ACTIVE. `demo` seeds demo data (DemoLoader) and has no
        auth side-effects; add `localauth` for form login alongside OIDC.
      '';
    };

    database = {
      host = lib.mkOption {
        type = lib.types.str;
        default = "127.0.0.1";
        description = "PostgreSQL host (the platform cluster on the host network).";
      };
      port = lib.mkOption {
        type = lib.types.port;
        default = 5432;
        description = "PostgreSQL port.";
      };
      name = lib.mkOption {
        type = lib.types.str;
        default = "epistola";
        description = "Database name (must exist in the cluster — see portablevps.postgres.{database,extraDatabases}).";
      };
      user = lib.mkOption {
        type = lib.types.str;
        default = "demo";
        description = "PostgreSQL role epistola-suite connects as.";
      };
      passwordSecret = lib.mkOption {
        type = lib.types.str;
        default = "postgres/password";
        description = "sops key holding the PostgreSQL password.";
      };
    };

    oidc = {
      enable = lib.mkEnableOption "OIDC login against an external provider (e.g. authentik)";
      registrationId = lib.mkOption {
        type = lib.types.str;
        default = "authentik";
        description = ''
          Spring registration id (lowercase alphanumeric). Also the redirect
          URI segment: https://<host>/login/oauth2/code/<registrationId>.
        '';
      };
      issuerUri = lib.mkOption {
        type = lib.types.str;
        default = "";
        example = "https://auth.epistola.app/application/o/epistola-demo/";
        description = "OIDC issuer URI — authentik form ends with a trailing slash.";
      };
      clientId = lib.mkOption {
        type = lib.types.str;
        default = "";
        description = "OAuth2 client id (not a secret).";
      };
      clientSecretSecret = lib.mkOption {
        type = lib.types.str;
        default = "epistola-suite/oidc-client-secret";
        description = "sops key holding the OAuth2 client secret.";
      };
      scope = lib.mkOption {
        type = lib.types.str;
        default = "openid,profile,email,epistola-roles";
        description = ''
          Requested scopes. `epistola-roles` carries the flat `roles` claim
          (authentik scope mapping) epistola-suite reads for authorization.
        '';
      };
      autoProvision = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Create the local user record on first OIDC login (EPISTOLA_AUTH_AUTOPROVISION).";
      };
      userNameAttribute = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = "preferred_username";
        description = ''
          Claim used as the Spring principal name shown in the UI. authentik
          defaults to `sub` (opaque id); set `preferred_username` to match
          Keycloak's default and display a real username. Null leaves the
          provider default.
        '';
      };
    };

    pullAuthUser = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "epistola-bot";
      description = "Registry username paired with pullAuthSecret (not a secret). Required when pullAuthSecret is set.";
    };

    pullAuthSecret = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "epistola-suite/ghcr-pull-auth";
      description = "Optional sops key holding ONLY the registry pull token (ghcr: a classic PAT with read:packages). Null for a public image.";
    };

    extraEnv = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = { };
      description = "Extra plain environment variables passed to the container.";
    };

    extraSecretEnv = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = { };
      example = { EPISTOLA_ENCRYPTION_KEY = "epistola-suite/encryption-key"; };
      description = "Extra env backed by sops secrets, as ENV_VAR = \"<sops key>\".";
    };
  };

  config = lib.mkIf cfg.enable (lib.mkMerge [
    {
      assertions = [
        {
          assertion = !oidcEnabled || (cfg.oidc.issuerUri != "" && cfg.oidc.clientId != "");
          message = "portablevps.apps.epistola-suite.oidc: issuerUri and clientId must be set when oidc.enable = true.";
        }
      ];

      environment.etc."containers/systemd/${cfg.containerName}.container".text = ''
        [Unit]
        Description=${cfg.containerName} (epistola-suite) for portablevps
        After=network-online.target postgres.service
        Wants=network-online.target
        Requires=postgres.service
        PartOf=apps.target
        ${restoreGate}

        [Container]
        Image=${cfg.image}
        ContainerName=${cfg.containerName}
        # Host network, bound to loopback — the proxy fronts it on 127.0.0.1:${toString cfg.port}.
        Network=host
        EnvironmentFile=/etc/portablevps/${cfg.containerName}.env
        ${lib.optionalString useAuth "PodmanArgs=--authfile=${authFile}"}
        # No HealthCmd: the Paketo run image ships no curl/wget, so an HTTP probe
        # here would false-fail. epistola-suite exposes /readyz for the proxy and
        # external checks; Restart=always covers a crashed process.

        [Service]
        Restart=always
        TimeoutStartSec=300

        [Install]
        ${lib.optionalString (!prototype) "WantedBy=apps.target"}
      '';

      # Normal mode: declare the referenced sops secrets and render the env file
      # from sops. Prototype/local-VM mode: no sops — write demo values directly.
      sops.secrets = lib.mkIf (!prototype)
        (lib.genAttrs (lib.unique (lib.attrValues secretEnv)) (_: { }));

      sops.templates = lib.mkIf (!prototype) {
        "portablevps/${cfg.containerName}.env" = {
          path = "/etc/portablevps/${cfg.containerName}.env";
          mode = "0400";
          content = envContent;
        };
      };

      environment.etc."portablevps/${cfg.containerName}.env" = lib.mkIf prototype {
        mode = "0400";
        text = envContent;
      };
    }

    # Private-registry pull auth: a oneshot combines the (non-secret) username
    # with the sops token and base64-encodes user:token into a podman authfile
    # before the container (same contract as apps/website).
    (lib.mkIf useAuth {
      assertions = [{
        assertion = cfg.pullAuthUser != null;
        message = "portablevps.apps.epistola-suite: pullAuthUser must be set when pullAuthSecret is set.";
      }];
      sops.secrets.${cfg.pullAuthSecret} = { };
      systemd.services."${cfg.containerName}-registry-auth" = {
        description = "Render the ${cfg.containerName} podman registry authfile from sops";
        before = [ "${cfg.containerName}.service" ];
        requiredBy = [ "${cfg.containerName}.service" ];
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
  ]);
}

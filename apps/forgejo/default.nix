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
    # With OIDC auto-registration on, registration must be allowed but limited to
    # the external provider (no local self-signup): DISABLE_REGISTRATION is forced
    # off and ALLOW_ONLY_EXTERNAL_REGISTRATION on. Otherwise honour disableRegistration.
    FORGEJO__service__DISABLE_REGISTRATION = lib.boolToString (cfg.disableRegistration && !oidcAutoRegister);
    FORGEJO__service__ALLOW_ONLY_EXTERNAL_REGISTRATION = lib.boolToString oidcAutoRegister;
    # Hide the local username/password sign-in form (leaving only the OIDC
    # button) when an OIDC provider is configured and oidc.hidePasswordForm is
    # set. Local login stays reachable at /user/login?force_login=true for the
    # break-glass admin.
    FORGEJO__service__ENABLE_PASSWORD_SIGNIN_FORM =
      lib.boolToString (!(cfg.oidc.enable && cfg.oidc.hidePasswordForm));
    FORGEJO__service__ENABLE_NOTIFY_MAIL = "false";
    FORGEJO__openid__ENABLE_OPENID_SIGNIN = "false";
    FORGEJO__openid__ENABLE_OPENID_SIGNUP = "false";
    FORGEJO__security__INSTALL_LOCK = "true";
    FORGEJO__api__ENABLE_SWAGGER = "false";
    FORGEJO__actions__ENABLED = lib.boolToString cfg.actions.enable;
  } // lib.optionalAttrs oidcAutoRegister {
    # Auto-provision a Forgejo account on first SSO login — no manual
    # complete/link page. Authentik is the gatekeeper for who reaches this flow,
    # so anyone it lets through gets an account. ACCOUNT_LINKING=auto links to an
    # existing local account by email (e.g. the break-glass admin).
    FORGEJO__oauth2_client__ENABLE_AUTO_REGISTRATION = "true";
    FORGEJO__oauth2_client__ACCOUNT_LINKING = "auto";
    FORGEJO__oauth2_client__USERNAME = cfg.oidc.usernameClaim;
    FORGEJO__oauth2_client__UPDATE_AVATAR = "true";
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
  # Always passed (empty values included) so that UNSETTING an option also
  # clears it on update-oauth, instead of leaving the old value in the source.
  oidcGroupFlags = lib.concatStringsSep " " [
    "--group-claim-name ${lib.escapeShellArg cfg.oidc.groupClaimName}"
    "--admin-group ${lib.escapeShellArg cfg.oidc.adminGroup}"
    "--restricted-group ${lib.escapeShellArg cfg.oidc.restrictedGroup}"
    "--group-team-map ${lib.escapeShellArg (if cfg.oidc.groupTeamMap == { } then "" else builtins.toJSON cfg.oidc.groupTeamMap)}"
    "--group-team-map-removal=${lib.boolToString cfg.oidc.groupTeamMapRemoval}"
  ];
  adminPasswordFile =
    if prototype
    then pkgs.writeText "forgejo-demo-admin-password" (demoSecret cfg.admin.passwordSecret)
    else config.sops.secrets.${cfg.admin.passwordSecret}.path;
  provisionOrgs = pkgs.writeShellApplication {
    name = "forgejo-provision-orgs";
    runtimeInputs = [ pkgs.coreutils pkgs.curl pkgs.gnused pkgs.jq ];
    text = builtins.readFile ./provision-orgs.sh;
  };
  upstreamStateDir = "/var/lib/portablevps/forgejo-upstream-sync";
  upstreamTokenFile = "${upstreamStateDir}/token";
  upstreamCopiesJson = pkgs.writeText "forgejo-upstream-copies.json" (builtins.toJSON (lib.mapAttrs
    (name: copy: {
      inherit (copy) upstream privatePrefix;
      hook = pkgs.writeText "forgejo-upstream-copy-hook-${lib.replaceStrings [ "/" ] [ "-" ] name}"
        (lib.replaceStrings [ "@BOT@" "@PREFIX@" ] [ cfg.upstreamSync.botUsername copy.privatePrefix ]
          (builtins.readFile ./upstream-copy-hook.sh));
    })
    cfg.upstreamCopies));
  provisionUpstreamCopies = pkgs.writeShellApplication {
    name = "forgejo-provision-upstream-copies";
    runtimeInputs = [ pkgs.coreutils pkgs.curl pkgs.git pkgs.gnused pkgs.jq ];
    text = builtins.readFile ./provision-upstream-copies.sh;
  };
  upstreamSync = pkgs.writeShellApplication {
    name = "forgejo-upstream-sync";
    runtimeInputs = [ pkgs.coreutils pkgs.gawk pkgs.git pkgs.gnugrep pkgs.gnused pkgs.jq ];
    text = builtins.readFile ./upstream-sync.sh;
  };
  organizationsJson = pkgs.writeText "forgejo-organizations.json" (builtins.toJSON (lib.mapAttrs
    (_: org: {
      full_name = org.fullName;
      inherit (org) description visibility;
      teams = lib.mapAttrs
        (_: team: {
          inherit (team) description;
          units_map = team.units;
          units = lib.attrNames team.units;
          includes_all_repositories = team.includesAllRepositories;
          can_create_org_repo = team.canCreateOrgRepo;
        })
        org.teams
      // lib.optionalAttrs org.packagePublisher.enable {
        package-publishers = {
          description = "Publishes this organization's packages (and nothing else). Members: the package-publishing bot.";
          units_map = { "repo.packages" = "write"; };
          units = [ "repo.packages" ];
          includes_all_repositories = true;
          can_create_org_repo = false;
        };
      };
    })
    cfg.organizations));
  packagePublishers = lib.filterAttrs (_: org: org.packagePublisher.enable) cfg.organizations;
  packagePublishersJson = pkgs.writeText "forgejo-package-publishers.json" (builtins.toJSON
    (lib.mapAttrs (_: org: { inherit (org.packagePublisher) username secretName userVariable; }) packagePublishers));
  provisionPackagePublishers = pkgs.writeShellApplication {
    name = "forgejo-provision-package-publishers";
    runtimeInputs = [ pkgs.coreutils pkgs.curl pkgs.jq ];
    text = builtins.readFile ./provision-package-publishers.sh;
  };
  oidcAutoRegister = cfg.oidc.enable && cfg.oidc.autoRegister;
  forgejoCli = "podman exec --user ${toString forgejoUid}:${toString forgejoUid} forgejo forgejo";
  disabledOpenSshServicePath = "/run/portablevps/forgejo-disabled-openssh-s6";
  disabledOpenSshService = pkgs.runCommand "forgejo-disabled-openssh-s6-service" { } ''
    mkdir -p "$out"
    cat > "$out/run" <<'EOF'
#!/bin/sh
exec sleep infinity
EOF
    cat > "$out/setup" <<'EOF'
#!/bin/sh
exit 0
EOF
    cat > "$out/finish" <<'EOF'
#!/bin/sh
exit 0
EOF
    chmod 0555 "$out" "$out/run" "$out/setup" "$out/finish"
  '';
in
{
  options.portablevps.apps.forgejo = {
    enable = lib.mkEnableOption "Forgejo code hosting app";

    image = lib.mkOption {
      type = lib.types.str;
      default = "codeberg.org/forgejo/forgejo:16.0.5";
      description = "Forgejo container image.";
    };

    dataRoot = lib.mkOption {
      type = lib.types.str;
      default = "/data/forgejo";
      description = "Host directory mounted as /data in the Forgejo container.";
    };

    domain = lib.mkOption {
      type = lib.types.str;
      example = "code.example.com";
      description = "Canonical Forgejo web domain.";
    };

    rootUrl = lib.mkOption {
      type = lib.types.str;
      example = "https://code.example.com/";
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
        example = "code.example.com";
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
        default = "breakglass";
        description = ''
          Break-glass local admin username. Must not be one of Forgejo's reserved
          names (notably "admin", which Forgejo refuses with
          `CreateUser: name is reserved`).
        '';
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
      hidePasswordForm = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Hide the local username/password sign-in form so the login page
          offers only the OIDC (single sign-on) button. Local login remains
          reachable at /user/login?force_login=true for a break-glass admin.
        '';
      };
      autoRegister = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Automatically create a Forgejo account on first SSO login instead of
          showing the manual complete/link-account page. Local self-signup stays
          disabled — only the OIDC provider (which is your access gate) can
          register users. Existing local accounts are linked by email.
        '';
      };
      usernameClaim = lib.mkOption {
        type = lib.types.enum [ "userid" "nickname" "email" "preferred_username" ];
        default = "preferred_username";
        description = "OIDC claim used as the Forgejo username on auto-registration.";
      };
      scopes = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ "openid" "email" "profile" "groups" ];
        description = "OIDC scopes requested by Forgejo.";
      };
      groupClaimName = lib.mkOption {
        type = lib.types.str;
        default = "";
        example = "groups";
        description = ''
          Claim carrying the user's group names. Required by adminGroup,
          restrictedGroup and groupTeamMap; empty disables group handling.
        '';
      };
      adminGroup = lib.mkOption {
        type = lib.types.str;
        default = "";
        description = ''
          Group whose members become Forgejo site admins at login. When set,
          Forgejo also REVOKES site admin from SSO users outside the group on
          their next login, so the IdP becomes the only source of admin rights.
        '';
      };
      restrictedGroup = lib.mkOption {
        type = lib.types.str;
        default = "";
        description = ''
          Group whose members are marked restricted at login (and non-members
          unrestricted). Leave empty to keep service.DEFAULT_USER_IS_RESTRICTED
          as the only source of the restricted flag.
        '';
      };
      groupTeamMap = lib.mkOption {
        type = lib.types.attrsOf (lib.types.attrsOf (lib.types.listOf lib.types.str));
        default = { };
        example = { forgejo_acme_developers = { acme = [ "developers" ]; }; };
        description = ''
          IdP group -> { org -> [ team ] }. Members are added to those teams at
          every login. The teams must exist (see organizations).
        '';
      };
      groupTeamMapRemoval = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Remove team memberships covered by groupTeamMap when the user no longer
          has the group, so revoking in the IdP revokes in Forgejo at next login.
        '';
      };
    };

    organizations = lib.mkOption {
      default = { };
      description = ''
        Organizations and teams created (or updated) by forgejo-provision
        through the API as the break-glass admin. Declarative in the additive
        sense: declared orgs/teams are upserted, anything else is left alone.
        Repository<->team assignment is not managed here.
      '';
      type = lib.types.attrsOf (lib.types.submodule ({ name, ... }: {
        options = {
          packagePublisher = {
            enable = lib.mkEnableOption ''
              a package-publishing bot for this organization. forgejo-provision
              creates a local account that may only publish packages here (team
              "package-publishers", unit repo.packages = write), issues it a
              write:package token, and stores that token as an organization
              Actions secret and the account name as an organization Actions
              variable, so every workflow in the org can publish with
              ''${{ secrets.<secretName> }} and ''${{ vars.<userVariable> }}.
              (Forgejo's automatic job token cannot publish packages.)
            '';
            username = lib.mkOption {
              type = lib.types.str;
              default = "${name}-packages";
              description = "Local Forgejo account of the bot (no SSO, no known password).";
            };
            secretName = lib.mkOption {
              type = lib.types.str;
              default = "PACKAGES_TOKEN";
              description = "Organization Actions secret holding the bot's token.";
            };
            userVariable = lib.mkOption {
              type = lib.types.str;
              default = "PACKAGES_USER";
              description = "Organization Actions variable holding the bot's username (the registry login).";
            };
          };
          fullName = lib.mkOption {
            type = lib.types.str;
            default = "";
            description = "Display name.";
          };
          description = lib.mkOption {
            type = lib.types.str;
            default = "";
            description = "Organization description.";
          };
          visibility = lib.mkOption {
            type = lib.types.enum [ "public" "limited" "private" ];
            default = "private";
            description = "Organization visibility.";
          };
          teams = lib.mkOption {
            default = { };
            description = "Teams in this organization (the built-in Owners team is left alone).";
            type = lib.types.attrsOf (lib.types.submodule {
              options = {
                description = lib.mkOption {
                  type = lib.types.str;
                  default = "";
                  description = "Team description.";
                };
                units = lib.mkOption {
                  type = lib.types.attrsOf (lib.types.enum [ "none" "read" "write" "admin" ]);
                  example = { "repo.code" = "write"; "repo.pulls" = "write"; };
                  description = ''
                    Per-unit access (Forgejo units_map): repo.code, repo.issues,
                    repo.pulls, repo.releases, repo.wiki, repo.ext_wiki,
                    repo.ext_issues, repo.projects, repo.packages, repo.actions.
                  '';
                };
                includesAllRepositories = lib.mkOption {
                  type = lib.types.bool;
                  default = false;
                  description = "Grant the team every repository in the org (otherwise repos are added explicitly).";
                };
                canCreateOrgRepo = lib.mkOption {
                  type = lib.types.bool;
                  default = false;
                  description = "Whether members may create repositories in the org.";
                };
              };
            });
          };
        };
      }));
    };

    upstreamCopies = lib.mkOption {
      default = { };
      example = lib.literalExpression ''
        { "acme/widget".upstream = "https://github.com/acme/widget.git"; }
      '';
      description = ''
        Forgejo repositories kept as copies of an upstream repository (any git
        URL), keyed by "owner/repo". Upstream owns every branch and tag except
        branches under `privatePrefix`, which are local work:

        - a timer (forgejo-upstream-sync) copies upstream's branches and tags
          into Forgejo: the default branch and tags fast-forward only (a
          rewrite upstream fails the run instead of rewriting history here),
          other branches follow upstream exactly, deletions are copied, and
          the private prefix is never read, pushed or deleted;
        - a pre-receive hook lets only the sync bot move upstream-owned refs,
          lets everyone else push only under the private prefix, and keeps the
          bot out of the private prefix.

        forgejo-provision creates the bot and its token, creates missing
        repositories (a pull mirror is refused: it cannot take pushes), gives
        the bot write access and installs the hook. Unlike a pull mirror, the
        copy can carry private branches.
      '';
      type = lib.types.attrsOf (lib.types.submodule {
        options = {
          upstream = lib.mkOption {
            type = lib.types.str;
            example = "https://github.com/acme/widget.git";
            description = "Upstream git URL the copy follows (read anonymously).";
          };
          privatePrefix = lib.mkOption {
            type = lib.types.str;
            default = "internal/";
            description = "Branch prefix that is local work: never synced, and the only thing people may push.";
          };
        };
      });
    };

    upstreamSync = {
      botUsername = lib.mkOption {
        type = lib.types.str;
        default = "upstream-sync";
        description = "Local Forgejo account the sync pushes as (no SSO, no known password).";
      };
      interval = lib.mkOption {
        type = lib.types.str;
        default = "5min";
        description = "How often upstream copies are synced (systemd time span).";
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
        "d ${disabledOpenSshServicePath} 0755 root root -"
        "C ${disabledOpenSshServicePath}/run 0555 root root - ${disabledOpenSshService}/run"
        "C ${disabledOpenSshServicePath}/setup 0555 root root - ${disabledOpenSshService}/setup"
        "C ${disabledOpenSshServicePath}/finish 0555 root root - ${disabledOpenSshService}/finish"
      ];

      environment.etc."containers/systemd/forgejo.container".text = ''
        [Unit]
        Description=Forgejo code hosting
        After=network-online.target postgres.service systemd-tmpfiles-setup.service
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
        # The upstream image also starts an OpenSSH s6 service. It ignores
        # Forgejo's SSH_LISTEN_HOST and always binds all interfaces, so mask it
        # and use Forgejo's built-in SSH server behind the NetBird-only proxy.
        Volume=${disabledOpenSshServicePath}:/etc/s6/openssh:Z

        [Service]
        ExecStartPre=${pkgs.systemd}/bin/systemd-tmpfiles --create
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

          admin_password="$(${pkgs.coreutils}/bin/tr -d '\n' < ${adminPasswordFile})"
          # Exact match on the Username column ($2), not a substring search of the
          # whole table — a loose `grep admin` also matches "akadmin" (Authentik's
          # SSO superuser) or the email column, wrongly taking the change-password
          # branch on a break-glass user that was never created.
          if ! ${forgejoCli} admin user list | ${pkgs.gawk}/bin/awk -v u=${lib.escapeShellArg cfg.admin.username} 'NR>1 && $2==u {f=1} END{exit(f?0:1)}'; then
            ${forgejoCli} admin user create \
              --admin \
              --username ${lib.escapeShellArg cfg.admin.username} \
              --password "$admin_password" \
              --email ${lib.escapeShellArg cfg.admin.email} \
              --must-change-password=false
          else
            # change-password defaults to --must-change-password=true, which
            # makes Forgejo answer every API call as this user with 403 ("You
            # must change your password") and forces a change at the next
            # break-glass login. The password is managed in sops, so never.
            ${forgejoCli} admin user change-password \
              --username ${lib.escapeShellArg cfg.admin.username} \
              --password "$admin_password" \
              --must-change-password=false
          fi
        '' + lib.optionalString oauthEnabled ''

          oauth_secret="$(${pkgs.coreutils}/bin/tr -d '\n' < ${if prototype then pkgs.writeText "forgejo-demo-oauth-secret" (demoSecret cfg.oidc.clientSecretSecret) else config.sops.secrets.${cfg.oidc.clientSecretSecret}.path})"
          auth_discover_url=${lib.escapeShellArg "${cfg.oidc.issuerBaseUrl}/application/o/${cfg.oidc.clientId}/.well-known/openid-configuration"}
          # The auth source's cached endpoints are refetched from --auto-discover-url
          # only when the source is (re)written, so ADD if missing and UPDATE if it
          # exists — otherwise an issuer/hostname change (e.g. moving Authentik to a
          # new domain) never propagates and login keeps redirecting to the old host.
          src_id="$(${forgejoCli} admin auth list | ${pkgs.gawk}/bin/awk -v n=${lib.escapeShellArg cfg.oidc.name} '$2 == n { print $1; exit }')"
          if [ -n "$src_id" ]; then
            ${forgejoCli} admin auth update-oauth \
              --id "$src_id" \
              --key ${lib.escapeShellArg cfg.oidc.clientId} \
              --secret "$oauth_secret" \
              --auto-discover-url "$auth_discover_url" \
              --scopes ${lib.escapeShellArg (lib.concatStringsSep "," cfg.oidc.scopes)} \
              ${oidcGroupFlags}
          else
            ${forgejoCli} admin auth add-oauth \
              --name ${lib.escapeShellArg cfg.oidc.name} \
              --provider ${lib.escapeShellArg cfg.oidc.provider} \
              --key ${lib.escapeShellArg cfg.oidc.clientId} \
              --secret "$oauth_secret" \
              --auto-discover-url "$auth_discover_url" \
              --scopes ${lib.escapeShellArg (lib.concatStringsSep "," cfg.oidc.scopes)} \
              ${oidcGroupFlags}
          fi
        '' + lib.optionalString (cfg.organizations != { }) ''

          FORGEJO_API=${lib.escapeShellArg "http://${cfg.listenHttp.host}:${toString cfg.listenHttp.port}/api/v1"} \
          FORGEJO_ADMIN_USER=${lib.escapeShellArg cfg.admin.username} \
          FORGEJO_ADMIN_PASSWORD_FILE=${adminPasswordFile} \
          FORGEJO_ORGS_JSON=${organizationsJson} \
            ${provisionOrgs}/bin/forgejo-provision-orgs
        '' + lib.optionalString (packagePublishers != { }) ''

          FORGEJO_API=${lib.escapeShellArg "http://${cfg.listenHttp.host}:${toString cfg.listenHttp.port}/api/v1"} \
          FORGEJO_ADMIN_USER=${lib.escapeShellArg cfg.admin.username} \
          FORGEJO_ADMIN_PASSWORD_FILE=${adminPasswordFile} \
          FORGEJO_CLI=${lib.escapeShellArg forgejoCli} \
          PUBLISHERS_JSON=${packagePublishersJson} \
          PUBLISHER_STATE_DIR=/var/lib/portablevps/forgejo-package-publishers \
            ${provisionPackagePublishers}/bin/forgejo-provision-package-publishers
        '' + lib.optionalString (cfg.upstreamCopies != { }) ''

          FORGEJO_API=${lib.escapeShellArg "http://${cfg.listenHttp.host}:${toString cfg.listenHttp.port}/api/v1"} \
          FORGEJO_ADMIN_USER=${lib.escapeShellArg cfg.admin.username} \
          FORGEJO_ADMIN_PASSWORD_FILE=${adminPasswordFile} \
          FORGEJO_CLI=${lib.escapeShellArg forgejoCli} \
          UPSTREAM_BOT=${lib.escapeShellArg cfg.upstreamSync.botUsername} \
          UPSTREAM_TOKEN_FILE=${upstreamTokenFile} \
          UPSTREAM_COPIES_JSON=${upstreamCopiesJson} \
          FORGEJO_REPOS_ROOT=${dataRoot}/git/repositories \
          FORGEJO_UID=${toString forgejoUid} \
            ${provisionUpstreamCopies}/bin/forgejo-provision-upstream-copies
        '';
      };

      portablevps.backups.components.forgejo = {
        order = 30;
        paths = [ dataRoot ];
        clearBeforeRestore = [ dataRoot ];
      };

      portablevps.migration.quiesceUnits = [ "forgejo.service" ];
    }

    (lib.mkIf (cfg.upstreamCopies != { }) {
      systemd.services.forgejo-upstream-sync = {
        description = "Sync Forgejo upstream copies from their upstream repositories";
        after = [ "forgejo.service" "forgejo-provision.service" "network-online.target" ];
        wants = [ "network-online.target" ];
        requires = [ "forgejo.service" ];
        unitConfig.ConditionPathExists = [ "!/run/portablevps/restore-mode" upstreamTokenFile ];
        environment = {
          FORGEJO_URL = "http://${cfg.listenHttp.host}:${toString cfg.listenHttp.port}";
          UPSTREAM_TOKEN_FILE = upstreamTokenFile;
          UPSTREAM_COPIES_JSON = "${upstreamCopiesJson}";
          UPSTREAM_CACHE_DIR = "${upstreamStateDir}/cache";
          GIT_SSL_CAINFO = "/etc/ssl/certs/ca-certificates.crt";
          HOME = upstreamStateDir;
        };
        serviceConfig = {
          Type = "oneshot";
          StateDirectory = "portablevps/forgejo-upstream-sync";
          StateDirectoryMode = "0700";
          ExecStart = "${upstreamSync}/bin/forgejo-upstream-sync";
          TimeoutStartSec = "30min";
        };
      };

      systemd.timers.forgejo-upstream-sync = {
        wantedBy = lib.optional (!config.portablevps.restoreMode) "timers.target";
        timerConfig = {
          OnBootSec = "2min";
          OnUnitActiveSec = cfg.upstreamSync.interval;
          RandomizedDelaySec = "30s";
        };
      };
    })

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

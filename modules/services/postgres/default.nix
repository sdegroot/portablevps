# Owns the PostgreSQL container, service exposure, and physical backup/restore hooks.
{ config, lib, postgresPkgs, pkgs, ... }:

let
  cfg = config.portablevps.postgres;

  # Extra databases are created via an init script mounted into
  # /docker-entrypoint-initdb.d (runs once, on an empty cluster, as the
  # superuser). CREATE DATABASE cannot run in a transaction and has no
  # IF NOT EXISTS, so use the SELECT ... \gexec idiom guarded on pg_database.
  extraDbInitScript = pkgs.writeText "portablevps-postgres-extra-databases.sql" (
    lib.concatMapStringsSep "\n"
      (db: ''
        SELECT 'CREATE DATABASE "${db}" OWNER "${cfg.user}"'
        WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '${db}')\gexec'')
      cfg.extraDatabases
  );
  hasExtraDatabases = cfg.extraDatabases != [ ];

  extensionInitScript = pkgs.writeText "portablevps-postgres-extensions.sql" (
    ''
      \connect "${cfg.database}"
    ''
    + lib.concatMapStringsSep "\n" (ext: ''CREATE EXTENSION IF NOT EXISTS "${ext}";'') cfg.extensions
  );
  hasExtensions = cfg.extensions != [ ];

  # ---- unprivileged application roles ---------------------------------------
  # Reconciled on every activation rather than seeded once through
  # /docker-entrypoint-initdb.d: an init script only ever runs on an EMPTY
  # cluster, which would leave a restored host (restore.sh replays a physical
  # backup) and a rotated password with no way to converge.
  prototype = config.portablevps.secrets.allowPrototypeDefaults;

  appRoleList = lib.mapAttrsToList (name: role: role // { inherit name; }) cfg.appRoles;
  hasAppRoles = appRoleList != [ ];

  appRoleDatabase = role: if role.database == null then cfg.database else role.database;

  # The password lives in sops normally; a local VM has no sops, so prototype
  # mode falls back to a fixed value written to /etc (same pattern as the apps).
  appRolePasswordFile = role:
    if prototype
    then "/etc/portablevps/postgres-app-role-${role.name}.pw"
    else config.sops.secrets.${role.passwordSecret}.path;

  # One SQL file per role. These land in the world-readable store, so the
  # password is NOT in them: \getenv reads it from the unit's environment and
  # :'pw' quotes it safely. CREATE ROLE/SCHEMA have no IF NOT EXISTS, hence the
  # SELECT ... \gexec idiom already used for extraDatabases above.
  appRoleSql = role:
    let
      db = appRoleDatabase role;
    in
    pkgs.writeText "portablevps-postgres-app-role-${role.name}.sql" (''
      \set ON_ERROR_STOP on
      \getenv pw PORTABLEVPS_APP_ROLE_PASSWORD
      SELECT format('CREATE ROLE %I LOGIN', '${role.name}')
      WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '${role.name}')\gexec
      -- Stated every time, not just at creation: this is the whole point of the
      -- option, so it must not drift if someone grants the role more by hand.
      ALTER ROLE "${role.name}" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
      ALTER ROLE "${role.name}" PASSWORD :'pw';
      REVOKE ALL ON DATABASE "${db}" FROM PUBLIC;
      GRANT CONNECT, TEMPORARY ON DATABASE "${db}" TO "${role.name}";
    ''
    + (
      if role.schema == "public" then ''
        -- PostgreSQL 15+ ships public owned by pg_database_owner and no longer
        -- lets non-owners create in it, so hand it over wholesale.
        ALTER SCHEMA public OWNER TO "${role.name}";
      '' else ''
        SELECT format('CREATE SCHEMA %I AUTHORIZATION %I', '${role.schema}', '${role.name}')
        WHERE NOT EXISTS (SELECT FROM pg_namespace WHERE nspname = '${role.schema}')\gexec
        ALTER SCHEMA "${role.schema}" OWNER TO "${role.name}";
        -- An app connecting with the default search_path would not find its own
        -- schema otherwise.
        ALTER ROLE "${role.name}" SET search_path = "${role.schema}";
      ''
    ));
in
{
  options.portablevps.postgres = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Enable the PostgreSQL demo container.";
    };

    dataRoot = lib.mkOption {
      type = lib.types.str;
      default = "/data/postgres";
      description = "Host directory mounted into the PostgreSQL container.";
    };

    dataDir = lib.mkOption {
      type = lib.types.str;
      default = "/data/postgres/18/docker";
      description = "PostgreSQL 18 live cluster directory on the host.";
    };

    backupRoot = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/portablevps-backups/postgres-physical";
      description = "Scratch directory containing the PostgreSQL physical backup chain.";
    };

    containerName = lib.mkOption {
      type = lib.types.str;
      default = "postgres-demo";
      description = "Podman container name.";
    };

    image = lib.mkOption {
      type = lib.types.str;
      default = "docker.io/library/postgres:18";
      description = "PostgreSQL container image. Override for extension-bearing images such as pgvector.";
    };

    database = lib.mkOption {
      type = lib.types.str;
      default = "demo";
      description = ''
        Name of the database the container initialises on first boot
        (POSTGRES_DB). Only takes effect on an empty data directory; changing
        it on a populated cluster does not rename the existing database.
      '';
    };

    user = lib.mkOption {
      type = lib.types.str;
      default = "demo";
      description = ''
        Superuser role the container creates on first boot (POSTGRES_USER),
        owning ${"\${database}"}. Its password comes from the postgres/password secret.
      '';
    };

    extraDatabases = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      example = [ "valtimo" ];
      description = ''
        Additional databases created in the same cluster on first boot, each
        owned by ${"\${user}"}. Use when a single box hosts several apps that
        each need their own database but can share one PostgreSQL instance
        (e.g. a box running two apps that share one cluster). Rendered as an
        idempotent init script in /docker-entrypoint-initdb.d, so — like
        POSTGRES_DB — it only takes effect on an empty data directory. The
        physical (pg_basebackup) backup already covers the whole cluster, so
        no per-database backup wiring is needed.
      '';
    };

    extensions = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      example = [ "hstore" "pg_trgm" "unaccent" "vector" ];
      description = ''
        PostgreSQL extensions created in the primary database on first boot.
        The selected image must ship each extension's control files.
      '';
    };

    maxConnections = lib.mkOption {
      type = lib.types.ints.positive;
      default = 100;
      description = ''
        PostgreSQL max_connections. The default matches upstream; raise it for
        apps that pool heavily (e.g. authentik without Redis wants 200).
      '';
    };

    backup.maxChainLength = lib.mkOption {
      type = lib.types.ints.positive;
      default = 24;
      description = ''
        Number of physical backups in a chain before the next backup is
        forced to be a fresh full base backup. Restore replays the whole
        chain with pg_combinebackup, so shorter chains bound both restore
        time and the blast radius of a corrupt chain link. With the hourly
        backup timer the default re-bases once a day.
      '';
    };

    appRoles = lib.mkOption {
      type = lib.types.attrsOf (lib.types.submodule {
        options = {
          passwordSecret = lib.mkOption {
            type = lib.types.str;
            example = "website/db-password";
            description = ''
              sops key holding ONLY this role's password. Read at activation from
              its secret file and handed to psql through the environment (never
              argv), so it stays out of the process list and out of the store.
            '';
          };

          database = lib.mkOption {
            type = lib.types.nullOr lib.types.str;
            default = null;
            description = ''
              Database the role may connect to. Defaults to the cluster's primary
              database; set it to one of the extra databases on a box that hosts
              several apps on one cluster.
            '';
          };

          schema = lib.mkOption {
            type = lib.types.str;
            default = "public";
            description = ''
              Schema the role OWNS in that database — this is what lets a
              migration tool create and alter its own tables with no cluster-wide
              privilege. Created if missing; "public" is taken over from its
              default owner instead.
            '';
          };
        };
      });
      default = { };
      example = {
        website.passwordSecret = "website/db-password";
      };
      description = ''
        Unprivileged login roles for the applications on this box, reconciled on
        every activation — so a rotated password or a freshly restored cluster
        converges on the next switch, not only on an empty data directory.

        Each role is created and then explicitly held to
        `NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`, granted
        `CONNECT`/`TEMPORARY` on its one database with `PUBLIC` revoked, and made
        owner of its schema. That is DDL and DML on its own data and nothing
        else: it cannot reach another database, add extensions, create roles,
        bypass row-level security, or open a replication stream.

        Point applications at one of these instead of at the `user` option: that
        one names the cluster SUPERUSER the container image creates on first
        boot, and it stays reserved for administration and for the
        `pg_basebackup` chain, which genuinely needs `REPLICATION`.

        NOTE: a non-superuser role cannot `CREATE EXTENSION`, so declare
        extensions in `portablevps.postgres.extensions` rather than in an
        application migration.
      '';
    };

    serviceName = lib.mkOption {
      type = lib.types.str;
      default = "postgres.service";
      description = "Systemd service generated by Quadlet.";
    };
  };

  config = lib.mkIf cfg.enable (lib.mkMerge [
    {
    # Quadlet unit for the PostgreSQL 18 container, rendered from the options
    # above so a consumer app can claim a dedicated database/user and tune
    # max_connections.
    environment.etc."containers/systemd/postgres.container".text = ''
      [Unit]
      Description=PostgreSQL 18 container (${cfg.database})
      After=network-online.target
      Wants=network-online.target
      PartOf=apps.target
      ConditionPathIsDirectory=${cfg.dataRoot}
      ConditionPathExists=!/run/portablevps/restore-mode

      [Container]
      Image=${cfg.image}
      ContainerName=${cfg.containerName}
      Environment=POSTGRES_USER=${cfg.user}
      Environment=POSTGRES_DB=${cfg.database}
      EnvironmentFile=/etc/portablevps/postgres.env
      Volume=${cfg.dataRoot}:/var/lib/postgresql
      ${lib.optionalString hasExtraDatabases
        "Volume=${extraDbInitScript}:/docker-entrypoint-initdb.d/10-portablevps-extra-databases.sql:ro"}
      ${lib.optionalString hasExtensions
        "Volume=${extensionInitScript}:/docker-entrypoint-initdb.d/20-portablevps-extensions.sql:ro"}
      PublishPort=127.0.0.1:5432:5432
      Exec=-c summarize_wal=on -c max_connections=${toString cfg.maxConnections}

      [Service]
      Restart=always

      [Install]
      WantedBy=apps.target
    '';

    systemd.tmpfiles.rules = [
      "d ${cfg.dataRoot} 0755 root root -"
    ];

    assertions =
      map
        (role: {
          assertion = role.name != cfg.user;
          message = ''
            portablevps.postgres.appRoles.${role.name} has the same name as
            portablevps.postgres.user, which is the cluster SUPERUSER the
            container image creates. Give the application its own role name (the
            point of an app role is that it is not that one).
          '';
        })
        appRoleList
      ++ map
        (role: {
          assertion =
            (appRoleDatabase role) == cfg.database
            || lib.elem (appRoleDatabase role) cfg.extraDatabases;
          message = ''
            portablevps.postgres.appRoles.${role.name}.database is
            "${appRoleDatabase role}", which this cluster does not create. Use
            portablevps.postgres.database ("${cfg.database}") or one of
            extraDatabases (${lib.concatStringsSep ", " cfg.extraDatabases}).
          '';
        })
        appRoleList;

    # The role's password is a secret this module consumes but never renders into
    # a template: the unit reads the file at activation.
    sops.secrets = lib.mkIf (hasAppRoles && !prototype)
      (lib.genAttrs (lib.unique (map (role: role.passwordSecret) appRoleList)) (_: { }));

    systemd.services.portablevps-postgres-app-roles = lib.mkIf hasAppRoles {
      description = "Reconcile unprivileged PostgreSQL application roles";
      after = [ cfg.serviceName ];
      wants = [ cfg.serviceName ];
      # Ordered before apps.target so a first boot normally has the role in place
      # by the time an app container starts. Containers are WantedBy the target
      # rather than After this unit, so a slow reconcile can still lose the race —
      # app containers are Restart=always, which covers that.
      wantedBy = lib.optional (!config.portablevps.restoreMode) "apps.target";
      before = lib.optional (!config.portablevps.restoreMode) "apps.target";
      unitConfig.ConditionPathExists = "!/run/portablevps/restore-mode";
      path = [ postgresPkgs.postgresql_18 pkgs.coreutils ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        # Connects as the cluster superuser: PGUSER/PGPASSWORD come from here.
        EnvironmentFile = [ "/etc/portablevps/postgres.env" ];
      };
      script = ''
        set -euo pipefail
        export PGHOST=127.0.0.1
        export PGPORT=5432

        for _ in $(seq 1 60); do
          if pg_isready -q; then
            break
          fi
          sleep 1
        done

        if ! pg_isready -q; then
          echo "error: PostgreSQL is not ready" >&2
          exit 70
        fi

        ${lib.concatMapStringsSep "\n" (role: ''
          PORTABLEVPS_APP_ROLE_PASSWORD="$(cat ${appRolePasswordFile role})" \
            PGDATABASE=${lib.escapeShellArg (appRoleDatabase role)} \
            psql --no-psqlrc --quiet --file=${appRoleSql role}
          echo "app role ${role.name}: reconciled on ${appRoleDatabase role} (schema ${role.schema})"
        '') appRoleList}
      '';
    };

    portablevps.serviceExposure.services.postgres.netbird.tcp = [
      {
        listenPort = 5432;
        targetHost = "127.0.0.1";
        targetPort = 5432;
        afterServices = [ cfg.serviceName ];
        wantsServices = [ cfg.serviceName ];
      }
    ];

    portablevps.backups.components.postgres = {
      order = 10;
      paths = [ cfg.backupRoot ];
      clearBeforeRestore = [
        cfg.backupRoot
        cfg.dataRoot
      ];
      packages = [
        postgresPkgs.postgresql_18
        pkgs.podman
      ];
      afterServices = [ cfg.serviceName ];
      wantsServices = [ cfg.serviceName ];
      preBackup = ''
        source /run/current-system/sw/bin/runtime-env.sh
        # Connect as the configured superuser/database (parameterised per app),
        # not runtime-env's built-in demo defaults, so pg_basebackup can
        # authenticate. load_postgres_env keeps a caller-set value.
        export PGUSER="${cfg.user}"
        export PGDATABASE="${cfg.database}"
        load_postgres_env

        POSTGRES_BACKUP_ROOT="${cfg.backupRoot}"
        POSTGRES_BACKUP_CHAIN_DIR="$POSTGRES_BACKUP_ROOT/chain"
        POSTGRES_BACKUP_METADATA_DIR="$POSTGRES_BACKUP_ROOT/metadata"
        POSTGRES_DATA_DIR="${cfg.dataDir}"

        for _ in $(seq 1 60); do
          if pg_isready -q; then
            break
          fi
          sleep 1
        done

        if ! pg_isready -q; then
          echo "error: PostgreSQL is not ready" >&2
          exit 70
        fi

        replication_hba_line="host replication all all scram-sha-256"
        if [ ! -f "$POSTGRES_DATA_DIR/pg_hba.conf" ]; then
          echo "error: PostgreSQL HBA config not found: $POSTGRES_DATA_DIR/pg_hba.conf" >&2
          exit 70
        fi

        if ! grep -Fxq "$replication_hba_line" "$POSTGRES_DATA_DIR/pg_hba.conf"; then
          printf '\n%s\n' "$replication_hba_line" >>"$POSTGRES_DATA_DIR/pg_hba.conf"
        fi
        # Always reload: a previous run may have appended the line but failed to
        # reload (e.g. before the connecting user/password was correct), leaving
        # it present-but-unloaded, which every later run would then skip.
        psql --no-psqlrc --quiet --tuples-only --command='select pg_reload_conf();' >/dev/null

        mkdir -p "$POSTGRES_BACKUP_CHAIN_DIR" "$POSTGRES_BACKUP_METADATA_DIR"

        backup_id="$(date -u +%Y%m%dT%H%M%SZ)"
        target_dir="$POSTGRES_BACKUP_CHAIN_DIR/$backup_id"
        while [ -e "$target_dir" ]; do
          backup_id="$(date -u +%Y%m%dT%H%M%SZ)-$RANDOM"
          target_dir="$POSTGRES_BACKUP_CHAIN_DIR/$backup_id"
        done

        latest_backup_id=""
        latest_backup_id_path="$POSTGRES_BACKUP_METADATA_DIR/latest-backup-id"
        if [ -r "$latest_backup_id_path" ]; then
          latest_backup_id="$(tr -d '[:space:]' <"$latest_backup_id_path")"
        fi

        chain_length="$(find "$POSTGRES_BACKUP_CHAIN_DIR" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d '[:space:]')"

        backup_mode="full"
        incremental_args=()
        if [ -n "$latest_backup_id" ] &&
          [ -r "$POSTGRES_BACKUP_CHAIN_DIR/$latest_backup_id/backup_manifest" ] &&
          [ "$chain_length" -lt ${toString cfg.backup.maxChainLength} ]; then
          backup_mode="incremental"
          incremental_args=(
            "--incremental=$POSTGRES_BACKUP_CHAIN_DIR/$latest_backup_id/backup_manifest"
          )
        fi

        pg_basebackup \
          --pgdata="$target_dir" \
          --format=plain \
          --wal-method=stream \
          --checkpoint=fast \
          "''${incremental_args[@]}"

        pg_verifybackup "$target_dir"

        if [ "$backup_mode" = "full" ]; then
          # A verified full starts a new chain. Drop older chain entries and
          # stale metadata so the scratch directory and the restic snapshot
          # hold exactly one chain; older chains stay in older snapshots.
          find "$POSTGRES_BACKUP_CHAIN_DIR" -mindepth 1 -maxdepth 1 -type d \
            ! -name "$backup_id" -exec rm -rf {} +
          find "$POSTGRES_BACKUP_METADATA_DIR" -mindepth 1 -maxdepth 1 \
            -name '*.mode' ! -name "$backup_id.mode" -delete
        fi

        printf '%s\n' "$backup_id" >"$latest_backup_id_path"
        printf '%s\n' "$backup_mode" >"$POSTGRES_BACKUP_METADATA_DIR/$backup_id.mode"

        echo "postgres backup mode: $backup_mode"
        echo "postgres backup id: $backup_id"
      '';
      preRestore = ''
        if podman container exists ${cfg.containerName} >/dev/null 2>&1 &&
          [ "$(podman inspect -f '{{.State.Running}}' ${cfg.containerName})" = "true" ]; then
          echo "error: refusing restore while ${cfg.containerName} container is running" >&2
          exit 78
        fi
      '';
      postRestore = ''
        POSTGRES_BACKUP_ROOT="${cfg.backupRoot}"
        POSTGRES_BACKUP_CHAIN_DIR="$POSTGRES_BACKUP_ROOT/chain"
        POSTGRES_DATA_DIR="${cfg.dataDir}"

        if [ ! -d "$POSTGRES_BACKUP_CHAIN_DIR" ]; then
          echo "error: restore did not create $POSTGRES_BACKUP_CHAIN_DIR" >&2
          exit 70
        fi

        mapfile -t backup_dirs < <(
          find "$POSTGRES_BACKUP_CHAIN_DIR" -mindepth 1 -maxdepth 1 -type d | sort
        )

        if [ "''${#backup_dirs[@]}" -eq 0 ]; then
          echo "error: no PostgreSQL physical backups found in $POSTGRES_BACKUP_CHAIN_DIR" >&2
          exit 70
        fi

        work_dir="$(mktemp -d /tmp/portablevps-pg-combine.XXXXXX)"
        combined_dir="$work_dir/combined"
        cleanup() {
          rm -rf "$work_dir"
        }
        trap cleanup EXIT

        pg_combinebackup --output="$combined_dir" "''${backup_dirs[@]}"

        mkdir -p ${cfg.dataRoot}
        find ${cfg.dataRoot} -mindepth 1 -maxdepth 1 -exec rm -rf {} +
        mkdir -p "$POSTGRES_DATA_DIR"
        tar -C "$combined_dir" -cf - . | tar -C "$POSTGRES_DATA_DIR" -xf -
      '';
    };
    }

    # Prototype/local-VM mode has no sops, so the app role's password comes from
    # a fixed file instead. Separate mkMerge branch because the block above
    # already defines environment.etc for the Quadlet unit.
    (lib.mkIf (hasAppRoles && prototype) {
      environment.etc = lib.listToAttrs (map
        (role: {
          name = "portablevps/postgres-app-role-${role.name}.pw";
          value = { mode = "0400"; text = "demo-password\n"; };
        })
        appRoleList);
    })
  ]);
}

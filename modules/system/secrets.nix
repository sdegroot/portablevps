# Wires sops-nix secrets into runtime env files for PostgreSQL, restic, S3, and Netbird.
{ lib, config, pkgs, ... }:

let
  cfg = config.portablevps.secrets;
  secretsFile = cfg.file;
  hasSecretsFile = secretsFile != null && builtins.pathExists secretsFile;
  useSops = hasSecretsFile && !cfg.allowPrototypeDefaults;

  # Postgres connection identity (user/database) belongs in postgres.env so
  # everything that sources it — the backup hook, verify-test-data.sh,
  # load_postgres_env — connects to the app's parameterised database rather than
  # the built-in demo defaults. Only emitted when the postgres module is present.
  pgConn = lib.optionalString (config.portablevps ? postgres) ''
    PGUSER=${config.portablevps.postgres.user}
    PGDATABASE=${config.portablevps.postgres.database}
  '';

  # A server only carries Postgres / restic secrets if it actually runs those.
  # A monitoring box (no postgres module, no backup components) has neither, so
  # requiring them in its sops file would fail sops-install-secrets.
  hasPostgres = config.portablevps ? postgres;
  hasBackupComponents = (config.portablevps.backups.components or { }) != { };
in
{
  options.portablevps.secrets = {
    allowPrototypeDefaults = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Allow plaintext prototype secrets when no sops file exists.";
    };

    file = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      description = "Encrypted sops secrets file for this host, provided by the consumer repository.";
    };

    ageKeyFile = lib.mkOption {
      type = lib.types.path;
      default = "/etc/sops/age/keys.txt";
      description = "Age identity used by sops-nix to decrypt host secrets.";
    };

    localBackupRepository = lib.mkOption {
      type = lib.types.str;
      default = "s3:http://10.0.2.2:9000/portablevps-dr";
      description = ''
        restic repository used under prototype defaults (local VMs). Points at a
        local test S3 (the QEMU host-gateway MinIO by default) so a local VM's
        own backup service never touches the real backup bucket. This is the
        "local provider" backup capability; override for a different local S3.
      '';
    };
  };

  config = {
    environment.systemPackages = with pkgs; [
      age
      sops
      ssh-to-age
    ];

    assertions = [
      {
        assertion = cfg.allowPrototypeDefaults || hasSecretsFile;
        message = "Encrypted secrets are required for this host. Set portablevps.secrets.file to an existing sops file, or enable portablevps.secrets.allowPrototypeDefaults for local-only profiles.";
      }
    ];

    sops = lib.mkIf useSops (lib.mkMerge [
      {
        defaultSopsFile = secretsFile;
        age.keyFile = cfg.ageKeyFile;
      }

      (lib.mkIf hasPostgres {
        secrets."postgres/password" = { };
        templates."portablevps/postgres.env" = {
          path = "/etc/portablevps/postgres.env";
          mode = "0400";
          content = ''
            POSTGRES_PASSWORD=${config.sops.placeholder."postgres/password"}
            PGPASSWORD=${config.sops.placeholder."postgres/password"}
          '' + pgConn;
        };
      })

      (lib.mkIf hasBackupComponents {
        secrets."restic/password" = { };
        secrets."restic/aws-access-key-id" = { };
        secrets."restic/aws-secret-access-key" = { };
        templates."portablevps/restic.env" = {
          path = "/etc/portablevps/restic.env";
          mode = "0400";
          content = ''
            RESTIC_REPOSITORY=${config.portablevps.backups.restic.repository}
            RESTIC_PASSWORD=${config.sops.placeholder."restic/password"}
            AWS_ACCESS_KEY_ID=${config.sops.placeholder."restic/aws-access-key-id"}
            AWS_SECRET_ACCESS_KEY=${config.sops.placeholder."restic/aws-secret-access-key"}
            AWS_DEFAULT_REGION=${config.portablevps.backups.restic.region}
            AWS_REGION=${config.portablevps.backups.restic.region}
          '';
        };
      })
    ]);

    environment.etc = lib.mkIf cfg.allowPrototypeDefaults (lib.mkMerge [
      (lib.mkIf hasPostgres {
        "portablevps/postgres.env" = {
          mode = "0400";
          text = ''
            POSTGRES_PASSWORD=demo-password
            PGPASSWORD=demo-password
          '' + pgConn;
        };
      })

      (lib.mkIf hasBackupComponents {
        "portablevps/restic.env" = {
          mode = "0400";
          text = ''
            RESTIC_REPOSITORY=${cfg.localBackupRepository}
            RESTIC_PASSWORD=dev-password
            AWS_ACCESS_KEY_ID=${config.portablevps.backups.restic.awsAccessKeyId}
            AWS_SECRET_ACCESS_KEY=portablevps-minio-password
            AWS_DEFAULT_REGION=${config.portablevps.backups.restic.region}
            AWS_REGION=${config.portablevps.backups.restic.region}
          '';
        };
      })
    ]);
  };
}

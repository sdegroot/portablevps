# Wires sops-nix secrets into runtime env files for PostgreSQL, restic, S3, and Netbird.
{ lib, config, pkgs, ... }:

let
  cfg = config.my.secrets;
  secretsFile = cfg.file;
  hasSecretsFile = secretsFile != null && builtins.pathExists secretsFile;
  useSops = hasSecretsFile && !cfg.allowPrototypeDefaults;
in
{
  options.my.secrets = {
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
        message = "Encrypted secrets are required for this host. Set my.secrets.file to an existing sops file, or enable my.secrets.allowPrototypeDefaults for local-only profiles.";
      }
    ];

    sops = lib.mkIf useSops {
      defaultSopsFile = secretsFile;
      age.keyFile = cfg.ageKeyFile;

      secrets."postgres/password" = { };
      secrets."restic/password" = { };
      secrets."restic/aws-access-key-id" = { };
      secrets."restic/aws-secret-access-key" = { };

      templates."epistola/postgres.env" = {
        path = "/etc/epistola/postgres.env";
        mode = "0400";
        content = ''
          POSTGRES_PASSWORD=${config.sops.placeholder."postgres/password"}
          PGPASSWORD=${config.sops.placeholder."postgres/password"}
        '';
      };

      templates."epistola/restic.env" = {
        path = "/etc/epistola/restic.env";
        mode = "0400";
        content = ''
          RESTIC_REPOSITORY=${config.my.backups.restic.repository}
          RESTIC_PASSWORD=${config.sops.placeholder."restic/password"}
          AWS_ACCESS_KEY_ID=${config.sops.placeholder."restic/aws-access-key-id"}
          AWS_SECRET_ACCESS_KEY=${config.sops.placeholder."restic/aws-secret-access-key"}
          AWS_DEFAULT_REGION=nl-ams
          AWS_REGION=nl-ams
        '';
      };
    };

    environment.etc = lib.mkIf cfg.allowPrototypeDefaults {
      "epistola/postgres.env" = {
        mode = "0400";
        text = ''
          POSTGRES_PASSWORD=demo-password
          PGPASSWORD=demo-password
        '';
      };

      "epistola/restic.env" = {
        mode = "0400";
        text = ''
          RESTIC_REPOSITORY=${config.my.backups.restic.repository}
          RESTIC_PASSWORD=dev-password
          AWS_ACCESS_KEY_ID=${config.my.backups.restic.awsAccessKeyId}
          AWS_SECRET_ACCESS_KEY=epistola-minio-password
          AWS_DEFAULT_REGION=us-east-1
          AWS_REGION=us-east-1
        '';
      };
    };
  };
}

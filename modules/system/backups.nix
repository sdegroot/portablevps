# Provides the module-owned backup/restore component registry and scheduled backup service.
{ lib, config, pkgs, ... }:

let
  cfg = config.portablevps.backups;
  componentType = lib.types.submodule ({ name, ... }: {
    options = {
      paths = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = "Paths included in the coordinated restic snapshot.";
      };

      clearBeforeRestore = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = "Paths removed before restic restore.";
      };

      preBackup = lib.mkOption {
        type = lib.types.lines;
        default = "";
        description = "Shell hook run before restic backup.";
      };

      preRestore = lib.mkOption {
        type = lib.types.lines;
        default = "";
        description = "Shell hook run before restic restore.";
      };

      postRestore = lib.mkOption {
        type = lib.types.lines;
        default = "";
        description = "Shell hook run after restic restore.";
      };

      packages = lib.mkOption {
        type = lib.types.listOf lib.types.package;
        default = [ ];
        description = "Packages needed by this component's hooks.";
      };

      wantsServices = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = "Services wanted by the scheduled backup.";
      };

      afterServices = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = "Services the scheduled backup runs after.";
      };

      order = lib.mkOption {
        type = lib.types.int;
        default = 50;
        description = "Hook execution order.";
      };
    };
  });

  components = cfg.components;
  componentList = lib.mapAttrsToList
    (name: component: component // { inherit name; })
    components;

  # A server with no backup components (e.g. a monitoring box) declares nothing
  # to back up, so the scheduled backup/maintenance timers stay disarmed. The
  # option is still declared here because core modules (deployment/secrets) wire
  # restic settings on every host.
  hasComponents = componentList != [ ];

  hookFile = hookDirName: hookOptionName: component:
    let
      # Zero-pad the order so the lexicographic `sort` in backup.sh/restore.sh
      # matches numeric order (otherwise order=5 would run after order=15, and
      # order=100 before order=15).
      orderName = "${lib.fixedWidthNumber 4 component.order}-${component.name}";
      hookText = component.${hookOptionName};
    in
    lib.optionalAttrs (hookText != "") {
      "portablevps/backups/${hookDirName}.d/${orderName}" = {
        text = ''
          #!/usr/bin/env bash
          set -euo pipefail
          ${hookText}
        '';
        mode = "0555";
      };
    };

  pathsFile = component:
    lib.optionalAttrs (component.paths != [ ]) {
      "portablevps/backups/paths.d/${component.name}".text =
        lib.concatMapStringsSep "\n" (path: path) component.paths + "\n";
    };

  clearFile = component:
    lib.optionalAttrs (component.clearBeforeRestore != [ ]) {
      "portablevps/backups/clear-before-restore.d/${component.name}".text =
        lib.concatMapStringsSep "\n" (path: path) component.clearBeforeRestore + "\n";
    };

  etcEntries = lib.mkMerge (
    lib.concatMap
      (component: [
        (pathsFile component)
        (clearFile component)
        (hookFile "pre-backup" "preBackup" component)
        (hookFile "pre-restore" "preRestore" component)
        (hookFile "post-restore" "postRestore" component)
      ])
      componentList
  );
in
{
  options.portablevps.backups = {
    restic = {
      repository = lib.mkOption {
        type = lib.types.str;
        default = "";
        example = "s3:https://s3.example.com/my-bucket/myservice/restic";
        description = ''
          Restic repository URL used by backup and restore scripts. Required on
          any host that registers backup components (unless it runs with
          prototype secrets, which use portablevps.secrets.localBackupRepository
          instead). Usually set via portablevps.server.backupRepository. Left
          empty it fails the build rather than silently backing up nowhere.
        '';
      };

      awsAccessKeyId = lib.mkOption {
        type = lib.types.str;
        default = "portablevps";
        description = "AWS access key ID for S3-compatible restic repositories.";
      };

      region = lib.mkOption {
        type = lib.types.str;
        default = "us-east-1";
        example = "nl-ams";
        description = ''
          Region reported to the S3 SDK (AWS_DEFAULT_REGION / AWS_REGION) for
          the restic repository. The default suits AWS us-east-1 and MinIO;
          set it to your bucket's region (e.g. "nl-ams" for Scaleway
          Amsterdam) — a mismatched region can fail request signing on some
          S3-compatible providers.
        '';
      };
    };

    components = lib.mkOption {
      type = lib.types.attrsOf componentType;
      default = { };
      description = "Stateful backup and restore components.";
    };

    retention = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          Apply the restic retention policy during weekly maintenance.
          Disable this for append-only repositories whose bucket policy
          denies object deletion; such repositories need out-of-band
          rotation instead.
        '';
      };

      keepHourly = lib.mkOption {
        type = lib.types.ints.positive;
        default = 48;
        description = "Hourly snapshots kept by restic forget.";
      };

      keepDaily = lib.mkOption {
        type = lib.types.ints.positive;
        default = 14;
        description = "Daily snapshots kept by restic forget.";
      };

      keepWeekly = lib.mkOption {
        type = lib.types.ints.positive;
        default = 8;
        description = "Weekly snapshots kept by restic forget.";
      };

      keepMonthly = lib.mkOption {
        type = lib.types.ints.positive;
        default = 12;
        description = "Monthly snapshots kept by restic forget.";
      };

      prune = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Prune unreferenced repository data after restic forget.";
      };
    };

    check.readDataSubset = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = "2%";
      description = ''
        Value for restic check --read-data-subset during weekly
        maintenance. Set to null to verify repository structure only.
      '';
    };
  };

  config = {
    environment.systemPackages =
      [ pkgs.restic ]
      ++ lib.unique (lib.concatMap (component: component.packages) componentList);

    environment.etc = etcEntries;

    systemd.tmpfiles.rules = [
      "d /backup-repo 0700 root root -"
      "d /etc/portablevps/backups/paths.d 0755 root root -"
      "d /etc/portablevps/backups/clear-before-restore.d 0755 root root -"
      "d /etc/portablevps/backups/pre-backup.d 0755 root root -"
      "d /etc/portablevps/backups/pre-restore.d 0755 root root -"
      "d /etc/portablevps/backups/post-restore.d 0755 root root -"
    ];

    systemd.services.portablevps-backup = {
      description = "Run coordinated module-owned backup";
      # network-online is explicit (not just transitive via component afterServices):
      # the restic repo is remote (S3), and ExecStartPre=init-backup-repo.sh needs
      # the network on a boot-time Persistent catch-up run.
      after = lib.unique ([ "network-online.target" ] ++ lib.concatMap (component: component.afterServices) componentList);
      wants = lib.unique ([ "network-online.target" ] ++ lib.concatMap (component: component.wantsServices) componentList);
      # backup.sh and each component hook use `#!/usr/bin/env bash` and call
      # restic / the component tools (pg_basebackup, podman, …) bare, so the
      # service PATH must carry them — the systemd default has neither bash nor
      # these tools, which silently failed every timer run with status 127.
      path = [ pkgs.bash pkgs.restic pkgs.jq pkgs.coreutils pkgs.gnugrep pkgs.gnused pkgs.findutils ]
        ++ lib.unique (lib.concatMap (component: component.packages) componentList);
      serviceConfig = {
        Type = "oneshot";
        EnvironmentFile = [
          "/etc/portablevps/restic.env"
        ];
        # Self-initialise the restic repository on first run (idempotent), so
        # backups just work on a freshly installed host without a manual
        # `restic init` step — the same on cloud and local.
        ExecStartPre = "/run/current-system/sw/bin/init-backup-repo.sh";
        ExecStart = "${pkgs.util-linux}/bin/flock -w 900 /run/lock/portablevps-backups.lock /run/current-system/sw/bin/backup.sh";
      };
    };

    systemd.timers.portablevps-backup = {
      wantedBy = lib.optional (hasComponents && !config.portablevps.restoreMode) "timers.target";
      timerConfig = {
        OnCalendar = "hourly";
        Persistent = true;
      };
    };

    systemd.services.portablevps-backup-maintenance = {
      description = "Apply restic retention policy and verify repository integrity";
      serviceConfig = {
        Type = "oneshot";
        EnvironmentFile = [
          "/etc/portablevps/restic.env"
        ];
      };
      script = ''
        set -euo pipefail
        exec ${pkgs.util-linux}/bin/flock -w 3600 /run/lock/portablevps-backups.lock ${pkgs.writeShellScript "portablevps-backup-maintenance-locked" ''
          set -euo pipefail
          ${lib.optionalString cfg.retention.enable ''
            ${pkgs.restic}/bin/restic forget \
              --keep-hourly ${toString cfg.retention.keepHourly} \
              --keep-daily ${toString cfg.retention.keepDaily} \
              --keep-weekly ${toString cfg.retention.keepWeekly} \
              --keep-monthly ${toString cfg.retention.keepMonthly} \
              ${lib.optionalString cfg.retention.prune "--prune"}
          ''}
          ${pkgs.restic}/bin/restic check \
            ${lib.optionalString (cfg.check.readDataSubset != null) "--read-data-subset=${cfg.check.readDataSubset}"}
        ''}
      '';
    };

    systemd.timers.portablevps-backup-maintenance = {
      wantedBy = lib.optional (hasComponents && !config.portablevps.restoreMode) "timers.target";
      timerConfig = {
        OnCalendar = "weekly";
        RandomizedDelaySec = "1h";
        Persistent = true;
      };
    };
  };
}

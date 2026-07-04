# Exposes node_exporter metrics for an external monitoring server over NetBird.
{ lib, config, pkgs, ... }:

let
  cfg = config.portablevps.monitoring;
  netbirdEnabled = config.portablevps.netbird.enable or false;
  netbirdInterface = config.portablevps.netbird.interface or (config.portablevps.cloud.netbirdInterface or "wt0");
  backupsPresent = (config.portablevps.backups.components or { }) != { };

  # Writes the backup outcome as node_exporter textfile metrics. On failure the
  # previous success timestamp is preserved so staleness alerts keep working.
  backupMetricsScript = pkgs.writeShellScript "portablevps-backup-metrics" ''
    set -euo pipefail
    outcome="$1"
    metrics_file=${lib.escapeShellArg "${cfg.textfileDirectory}/portablevps-backup.prom"}
    now="$(date +%s)"

    last_success=0
    if [ "$outcome" = "success" ]; then
      last_success="$now"
    elif [ -f "$metrics_file" ]; then
      previous="$(awk '$1 == "portablevps_backup_last_success_timestamp_seconds" { print $2 }' "$metrics_file")"
      if [ -n "$previous" ]; then
        last_success="$previous"
      fi
    fi

    if [ "$outcome" = "success" ]; then
      status=0
    else
      status=1
    fi

    tmp_file="$metrics_file.tmp"
    {
      echo "# HELP portablevps_backup_last_run_timestamp_seconds Unix time of the last completed backup attempt."
      echo "# TYPE portablevps_backup_last_run_timestamp_seconds gauge"
      echo "portablevps_backup_last_run_timestamp_seconds $now"
      echo "# HELP portablevps_backup_last_success_timestamp_seconds Unix time of the last successful backup."
      echo "# TYPE portablevps_backup_last_success_timestamp_seconds gauge"
      echo "portablevps_backup_last_success_timestamp_seconds $last_success"
      echo "# HELP portablevps_backup_last_run_failed Whether the last backup attempt failed."
      echo "# TYPE portablevps_backup_last_run_failed gauge"
      echo "portablevps_backup_last_run_failed $status"
    } > "$tmp_file"
    mv "$tmp_file" "$metrics_file"
  '';
in
{
  options.portablevps.monitoring = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Run node_exporter so a separate monitoring server can scrape this host.";
    };

    port = lib.mkOption {
      type = lib.types.port;
      default = 9100;
      description = "node_exporter listen port.";
    };

    openPublicFirewall = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Open the node_exporter port on the public firewall instead of only the NetBird interface.";
    };

    textfileDirectory = lib.mkOption {
      type = lib.types.str;
      default = "/var/lib/portablevps-metrics/textfile";
      description = "node_exporter textfile collector directory for custom metrics.";
    };
  };

  config = lib.mkIf cfg.enable {
    services.prometheus.exporters.node = {
      enable = true;
      port = cfg.port;
      enabledCollectors = [ "systemd" ];
      extraFlags = [
        "--collector.textfile.directory=${cfg.textfileDirectory}"
      ];
    };

    systemd.tmpfiles.rules = [
      "d /var/lib/portablevps-metrics 0755 root root -"
      "d ${cfg.textfileDirectory} 0755 root root -"
    ];

    networking.firewall.interfaces = lib.mkIf netbirdEnabled {
      ${netbirdInterface}.allowedTCPPorts = [ cfg.port ];
    };

    networking.firewall.allowedTCPPorts = lib.mkIf cfg.openPublicFirewall [ cfg.port ];

    systemd.services.portablevps-backup = lib.mkIf backupsPresent {
      onFailure = [ "portablevps-backup-failure-metrics.service" ];
      serviceConfig.ExecStartPost = "${backupMetricsScript} success";
    };

    systemd.services.portablevps-backup-failure-metrics = lib.mkIf backupsPresent {
      description = "Record failed portablevps-backup run in node_exporter textfile metrics";
      serviceConfig = {
        Type = "oneshot";
        ExecStart = "${backupMetricsScript} failure";
      };
    };
  };
}

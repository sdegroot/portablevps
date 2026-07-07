# Fleet telemetry shipper: an OpenTelemetry Collector that ships this host's
# metrics + logs to the monitoring server's OTLP gateway over the mesh. Enabled
# per server by setting `portablevps.telemetry.endpoint` to the gateway URL.
#
# Replaces node_exporter (pull) with OTLP push — the monitoring stack ingests
# OTLP, so there is nothing to scrape. The dedicated monitoring server does NOT
# set an endpoint: its own gateway scrapes its host directly.
{ lib, config, pkgs, ... }:

let
  cfg = config.portablevps.telemetry;
  enabled = cfg.endpoint != "";
  backupsPresent = (config.portablevps.backups.components or { }) != { };

  # Ship-only collector config. host.name is detected from the OS hostname
  # (resourcedetection) and becomes the series' host_name label under the
  # gateway's VictoriaMetrics usePrometheusNaming.
  shipConfig = pkgs.writeText "otelcol-ship.yaml" ''
    receivers:
      hostmetrics:
        collection_interval: ${cfg.scrapeInterval}
        root_path: /host
        scrapers:
          cpu:
          memory:
          disk:
          filesystem:
          load:
          network:
          processes:
      docker_stats:
        endpoint: unix:///var/run/docker.sock
        collection_interval: ${cfg.scrapeInterval}
      filelog:
        include:
          - /var/log/syslog
        start_at: end
    processors:
      batch:
        timeout: 10s
        send_batch_size: 1024
      resourcedetection:
        detectors: [system, env]
        system:
          hostname_sources: [os]
        override: false
      resource:
        attributes:
          - key: service.namespace
            value: ${cfg.serviceNamespace}
            action: upsert
          - key: host.id
            from_attribute: host.name
            action: upsert
    exporters:
      otlphttp:
        endpoint: ${cfg.endpoint}
    service:
      telemetry:
        logs:
          level: info
      pipelines:
        metrics:
          receivers: [hostmetrics, docker_stats]
          processors: [batch, resourcedetection, resource]
          exporters: [otlphttp]
        logs:
          receivers: [filelog]
          processors: [batch, resourcedetection, resource]
          exporters: [otlphttp]
  '';

  # Best-effort push of a single gauge (value = now) to the gateway, stamping
  # host.name so it lands as a per-host series. Never fails its caller — the
  # backup it reports on has already succeeded. Ported from the epistola-vps-infra
  # backup role's restic-report-metric.
  reportMetric = pkgs.writeShellScript "portablevps-report-metric" ''
    set -u
    metric="''${1:-}"
    [ -n "$metric" ] || { echo "usage: $0 <metric_name>" >&2; exit 2; }
    now_ns="$(${pkgs.coreutils}/bin/date +%s)000000000"
    ts="$(${pkgs.coreutils}/bin/date +%s)"
    host=${lib.escapeShellArg config.networking.hostName}
    payload='{"resourceMetrics":[{"resource":{"attributes":[{"key":"host.name","value":{"stringValue":"'"$host"'"}},{"key":"service.namespace","value":{"stringValue":"${cfg.serviceNamespace}"}}]},"scopeMetrics":[{"metrics":[{"name":"'"$metric"'","gauge":{"dataPoints":[{"timeUnixNano":"'"$now_ns"'","asDouble":'"$ts"'}]}}]}]}]}'
    ${pkgs.curl}/bin/curl -sf --max-time 10 \
      -H 'Content-Type: application/json' \
      --data "$payload" \
      "${cfg.endpoint}/v1/metrics" >/dev/null 2>&1 || true
  '';
in
{
  imports = [ ../networking/network.nix ];

  options.portablevps.telemetry = {
    endpoint = lib.mkOption {
      type = lib.types.str;
      default = "";
      example = "http://mon.example.int:4318";
      description = "OTLP/HTTP gateway URL this host ships metrics + logs to. Empty disables the shipper (e.g. on the monitoring server itself, whose gateway scrapes it directly).";
    };

    serviceNamespace = lib.mkOption {
      type = lib.types.str;
      default = "portablevps";
      description = "service.namespace stamped on this host's telemetry.";
    };

    scrapeInterval = lib.mkOption {
      type = lib.types.str;
      default = "30s";
      description = "hostmetrics / docker_stats collection interval.";
    };

    image = lib.mkOption {
      type = lib.types.str;
      default = "docker.io/otel/opentelemetry-collector-contrib:0.153.0";
      description = "OpenTelemetry Collector (contrib) image.";
    };
  };

  config = lib.mkIf enabled {
    # rsyslog populates /var/log/syslog for the filelog receiver (the contrib
    # image has no journalctl, so the journald receiver is not an option).
    services.rsyslogd.enable = true;

    environment.etc."containers/systemd/otelcol-ship.container".text = ''
      [Unit]
      Description=OpenTelemetry Collector (fleet metrics + logs shipper)
      After=network-online.target podman.socket
      Wants=network-online.target podman.socket

      [Container]
      Image=${cfg.image}
      ContainerName=otelcol-ship
      # Root + host namespaces: hostmetrics/process scrapers need the real
      # /proc, and docker_stats + filelog read root-owned sources.
      User=0
      Network=host
      PodmanArgs=--pid=host
      Volume=/proc:/host/proc:ro
      Volume=/sys:/host/sys:ro
      Volume=/run/podman/podman.sock:/var/run/docker.sock:ro
      Volume=/var/log:/var/log:ro
      Volume=${shipConfig}:/etc/otelcol/config.yaml:ro
      Exec=--config=/etc/otelcol/config.yaml

      [Service]
      Restart=always
      RestartSec=10

      [Install]
      WantedBy=multi-user.target default.target
    '';

    # Push the backup-success metric over OTLP after a successful run (the
    # gateway's freshness/presence alerts watch it). ExecStartPost on the oneshot
    # runs only when the backup itself succeeded.
    systemd.services.portablevps-backup = lib.mkIf backupsPresent {
      serviceConfig.ExecStartPost = "${reportMetric} portablevps_backup_last_success_timestamp_seconds";
    };
  };
}

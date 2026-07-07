# Fleet telemetry shipper: an OpenTelemetry Collector that ships this host's
# metrics + logs to the monitoring server's OTLP gateway over the mesh. Enabled
# per server by setting `portablevps.telemetry.endpoint` to the gateway URL.
#
# Runs the collector NATIVELY (not in a container) so its `journald` receiver can
# read the systemd journal directly via journalctl — no rsyslog, no duplicated
# /var/log/syslog file to grow and rotate. Replaces node_exporter (pull) with
# OTLP push. The dedicated monitoring server does NOT set an endpoint: its own
# gateway scrapes its host directly.
{ lib, config, pkgs, ... }:

let
  cfg = config.portablevps.telemetry;
  enabled = cfg.endpoint != "";
  backupsPresent = (config.portablevps.backups.components or { }) != { };

  # Resource attributes stamped on every metric/log. service.name (the
  # application) is added when set so telemetry can be differentiated by app, not
  # just host. host.name comes from the CONFIGURED machine name, not the OS
  # hostname, so a repurposed/renamed box never reports under a stale name.
  resourceAttrs = [
    { key = "host.name"; value = config.networking.hostName; action = "upsert"; }
    { key = "service.namespace"; value = cfg.serviceNamespace; action = "upsert"; }
    { key = "host.id"; from_attribute = "host.name"; action = "upsert"; }
  ] ++ lib.optional (cfg.serviceName != "") {
    key = "service.name"; value = cfg.serviceName; action = "upsert";
  };

  # Best-effort push of a single gauge (value = now) to the gateway, stamping
  # host.name so it lands as a per-host series. Never fails its caller — the
  # backup it reports on has already succeeded.
  reportMetric = pkgs.writeShellScript "portablevps-report-metric" ''
    set -u
    metric="''${1:-}"
    [ -n "$metric" ] || { echo "usage: $0 <metric_name>" >&2; exit 2; }
    now_ns="$(${pkgs.coreutils}/bin/date +%s)000000000"
    ts="$(${pkgs.coreutils}/bin/date +%s)"
    host=${lib.escapeShellArg config.networking.hostName}
    payload='{"resourceMetrics":[{"resource":{"attributes":[{"key":"host.name","value":{"stringValue":"'"$host"'"}},{"key":"service.namespace","value":{"stringValue":"${cfg.serviceNamespace}"}}${lib.optionalString (cfg.serviceName != "") '',{"key":"service.name","value":{"stringValue":"${cfg.serviceName}"}}''}]},"scopeMetrics":[{"metrics":[{"name":"'"$metric"'","gauge":{"dataPoints":[{"timeUnixNano":"'"$now_ns"'","asDouble":'"$ts"'}]}}]}]}]}'
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
      description = "service.namespace stamped on this host's telemetry (the fleet/domain).";
    };

    serviceName = lib.mkOption {
      type = lib.types.str;
      default = "";
      description = "service.name stamped on this host's telemetry — the application it runs (e.g. authentik), so metrics and logs can be differentiated by app in addition to host.name.";
    };

    scrapeInterval = lib.mkOption {
      type = lib.types.str;
      default = "30s";
      description = "hostmetrics / docker_stats collection interval.";
    };
  };

  config = lib.mkIf enabled {
    services.opentelemetry-collector = {
      enable = true;
      # contrib build for the docker_stats + journald receivers.
      package = pkgs.opentelemetry-collector-contrib;
      settings = {
        receivers = {
          hostmetrics = {
            collection_interval = cfg.scrapeInterval;
            scrapers = {
              cpu = { };
              memory = { };
              disk = { };
              filesystem = { };
              load = { };
              network = { };
              processes = { };
            };
          };
          # NOTE: no docker_stats receiver. The contrib docker_stats receiver
          # can't reliably parse podman's docker-compat containerStats response
          # ("Could not parse docker containerStats" / "context canceled"), so
          # per-container metrics are dropped rather than shipped noisily. Host
          # metrics + logs cover the fleet; revisit with a podman-native metrics
          # source if per-container stats are needed.

          # Read the systemd journal directly (journalctl) — no rsyslog / file.
          journald = { };
        };
        processors = {
          batch = { timeout = "10s"; send_batch_size = 1024; };
          resourcedetection = {
            detectors = [ "system" "env" ];
            system.hostname_sources = [ "os" ];
            override = false;
          };
          resource.attributes = resourceAttrs;
        };
        exporters.otlphttp.endpoint = cfg.endpoint;
        service = {
          telemetry = {
            logs.level = "info";
            # Disable the collector's own :8888 Prometheus self-metrics endpoint —
            # nothing scrapes it, and it otherwise contends for the port.
            metrics.level = "none";
          };
          pipelines = {
            metrics = {
              receivers = [ "hostmetrics" ];
              processors = [ "batch" "resourcedetection" "resource" ];
              exporters = [ "otlphttp" ];
            };
            logs = {
              receivers = [ "journald" ];
              processors = [ "batch" "resourcedetection" "resource" ];
              exporters = [ "otlphttp" ];
            };
          };
        };
      };
    };

    # The collector reads root-owned sources: /proc for host + process metrics,
    # the podman API socket for container stats, and the journal via journalctl.
    # Run it as root with journalctl on PATH, and relax the module's default
    # hardening enough to see all processes.
    systemd.services.opentelemetry-collector = {
      path = [ pkgs.systemd ];
      serviceConfig = {
        DynamicUser = lib.mkForce false;
        User = lib.mkForce "root";
        Group = lib.mkForce "root";
        ProtectProc = lib.mkForce "default";
        ProcSubset = lib.mkForce "all";
        PrivateUsers = lib.mkForce false;
        PrivateDevices = lib.mkForce false;
      };
    };

    # Push the backup-success metric over OTLP after a successful run (the
    # gateway's freshness/presence alerts watch it).
    systemd.services.portablevps-backup = lib.mkIf backupsPresent {
      serviceConfig.ExecStartPost = "${reportMetric} portablevps_backup_last_success_timestamp_seconds";
    };
  };
}

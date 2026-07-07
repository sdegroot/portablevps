# portablevps monitoring stack — a dedicated observability server.
#
# Ported from epistola-vps-infra/servers/monitoring (Ansible + podman) to
# NixOS + podman Quadlet. Runs, as containers on a shared podman network:
#   VictoriaMetrics (metrics TSDB) · VictoriaLogs (logs) · vmalert (rules) ·
#   Alertmanager (routing → email via Brevo) · Grafana (dashboards) ·
#   OpenTelemetry Collector (fleet OTLP gateway + this host's own agent).
#
# The fleet ships metrics+logs here over the mesh via OTLP (see
# modules/system/telemetry.nix). The UIs bind loopback and are fronted by the
# portablevps proxy (Traefik) on the mesh — unlike the old repo's co-located
# Caddy "mesh-proxy". The OTLP gateway ports are opened on the NetBird interface
# only.
{ config, lib, pkgs, ... }:

let
  cfg = config.portablevps.apps.monitoring;

  # Local-VM / prototype mode: no sops, no real secrets. Render demo config so
  # the stack boots for local validation (email won't actually send; OIDC off).
  prototype = config.portablevps.secrets.allowPrototypeDefaults;

  netbirdEnabled = config.portablevps.network.enable or false;
  netbirdInterface = config.portablevps.network.interface;

  dataDir = cfg.dataDir;
  net = cfg.podmanNetwork;

  restoreGate = "ConditionPathExists=!/run/portablevps/restore-mode";

  # A stack container on the shared podman network with a static bridge IP
  # (keeps netavark's published-port DNAT stable across recreates).
  mkContainer = { name, image, ip, uid, publishPorts ? [ ], volumes ? [ ]
                , exec ? null, environmentFile ? null, extra ? [ ]
                , after ? [ ], requires ? [ ], podmanArgs ? [ ], network ? net
                , timeoutStart ? 120 }:
    let
      afterUnits = [ "network-online.target" ] ++ after;
      publishLines = map (p: "PublishPort=${p}") publishPorts;
      volumeLines = map (v: "Volume=${v}") volumes;
      podmanArgLines = map (a: "PodmanArgs=${a}") podmanArgs;
    in
    ''
      [Unit]
      Description=${name} (monitoring) for portablevps
      After=${lib.concatStringsSep " " afterUnits}
      Wants=network-online.target
      ${lib.concatMapStringsSep "\n" (u: "Requires=${u}") requires}
      PartOf=apps.target
      ${restoreGate}

      [Container]
      Image=${image}
      ContainerName=${name}
      ${lib.optionalString (network != null) "Network=${network}"}
      ${lib.optionalString (uid != null) "User=${toString uid}"}
      ${lib.optionalString (environmentFile != null) "EnvironmentFile=${environmentFile}"}
      ${lib.concatStringsSep "\n" (publishLines ++ volumeLines ++ podmanArgLines ++ extra)}
      ${lib.optionalString (exec != null) "Exec=${exec}"}

      [Service]
      Restart=always
      TimeoutStartSec=${toString timeoutStart}

      [Install]
      WantedBy=apps.target
    '';

  # Static config mounted read-only from the Nix store.
  gatewayConfig = ./otelcol-gateway.yaml;
  scrapeConfig = ./scrape.yml;
  rulesDir = ./rules;
  grafanaProvisioning = ./grafana/provisioning;
  grafanaDashboards = ./grafana/dashboards;

  # Alertmanager config carries the SMTP password, so in normal mode it is a
  # sops template rendered to a 0400 file; in prototype mode a demo file.
  alertmanagerConfigPath =
    if prototype
    then "/etc/portablevps/monitoring/alertmanager.yml"
    else config.sops.templates."portablevps/monitoring/alertmanager.yml".path;

  alertmanagerConfig = ''
    global:
      resolve_timeout: 5m
      smtp_smarthost: "${cfg.smtp.host}:${toString cfg.smtp.port}"
      smtp_from: "${cfg.alertEmailFrom}"
      smtp_auth_username: "${if prototype then "demo" else "PLACEHOLDER_SMTP_USERNAME"}"
      smtp_auth_password: "${if prototype then "demo" else "PLACEHOLDER_SMTP_PASSWORD"}"
      smtp_require_tls: true

    route:
      receiver: email
      group_by: ['alertname', 'host_name']
      group_wait: 30s
      group_interval: 5m
      repeat_interval: ${cfg.alertRepeatInterval}

    receivers:
      - name: email
        email_configs:
          - to: "${cfg.alertEmailTo}"
            send_resolved: true

    inhibit_rules:
      - source_matchers: ['severity="critical"']
        target_matchers: ['severity="warning"']
        equal: ['alertname', 'host_name']
  '';

  # Grafana env: local admin (break-glass) + optional authentik OIDC. Secret
  # values come from a sops-rendered EnvironmentFile in normal mode.
  grafanaEnvPath =
    if prototype
    then "/etc/portablevps/monitoring/grafana.env"
    else config.sops.templates."portablevps/monitoring/grafana.env".path;

  publishLoopbackAnd = port: containerPort: [
    "127.0.0.1:${toString port}:${toString containerPort}"
  ];
in
{
  options.portablevps.apps.monitoring = {
    enable = lib.mkEnableOption "the portablevps monitoring stack (VictoriaMetrics/Logs, vmalert, Alertmanager, Grafana, otelcol gateway)";

    dataDir = lib.mkOption {
      type = lib.types.str;
      default = "/data/monitoring";
      description = "Writable data root for the stack (metrics/logs TSDBs, Grafana, Alertmanager). Deliberately not registered for backup — it is the only copy and treated as disposable.";
    };

    podmanNetwork = lib.mkOption {
      type = lib.types.str;
      default = "monitoring.network";
      description = "Quadlet network reference shared by the stack containers.";
    };

    images = {
      victoriametrics = lib.mkOption { type = lib.types.str; default = "docker.io/victoriametrics/victoria-metrics:v1.144.0"; description = "VictoriaMetrics image."; };
      victorialogs = lib.mkOption { type = lib.types.str; default = "docker.io/victoriametrics/victoria-logs:v1.50.0"; description = "VictoriaLogs image."; };
      vmalert = lib.mkOption { type = lib.types.str; default = "docker.io/victoriametrics/vmalert:v1.144.0"; description = "vmalert image (keep the tag == victoriametrics)."; };
      alertmanager = lib.mkOption { type = lib.types.str; default = "docker.io/prom/alertmanager:v0.32.1"; description = "Alertmanager image."; };
      grafana = lib.mkOption { type = lib.types.str; default = "docker.io/grafana/grafana:13.0.1"; description = "Grafana image."; };
      otelcol = lib.mkOption { type = lib.types.str; default = "docker.io/otel/opentelemetry-collector-contrib:0.153.0"; description = "OpenTelemetry Collector (contrib) image."; };
    };

    retention = {
      metrics = lib.mkOption { type = lib.types.str; default = "90d"; description = "VictoriaMetrics retention window (this window IS the history — no backup)."; };
      logs = lib.mkOption { type = lib.types.str; default = "30d"; description = "VictoriaLogs retention window."; };
    };

    smtp = {
      host = lib.mkOption { type = lib.types.str; default = "smtp-relay.brevo.com"; description = "Alertmanager SMTP smarthost."; };
      port = lib.mkOption { type = lib.types.port; default = 587; description = "SMTP port (STARTTLS)."; };
      usernameSecret = lib.mkOption { type = lib.types.str; default = "monitoring/smtp-username"; description = "sops secret holding the SMTP username."; };
      passwordSecret = lib.mkOption { type = lib.types.str; default = "monitoring/smtp-password"; description = "sops secret holding the SMTP password."; };
    };

    alertEmailFrom = lib.mkOption { type = lib.types.str; default = "alerts@epistola.io"; description = "Alert email From address."; };
    alertEmailTo = lib.mkOption { type = lib.types.str; default = "sander@degroot.dev"; description = "Alert email recipient."; };
    alertRepeatInterval = lib.mkOption { type = lib.types.str; default = "4h"; description = "Alertmanager repeat interval for a still-firing alert."; };

    grafana = {
      rootUrl = lib.mkOption { type = lib.types.str; default = "https://grafana.int.epistola.io"; description = "GF_SERVER_ROOT_URL (the mesh proxy name)."; };
      adminPasswordSecret = lib.mkOption { type = lib.types.str; default = "monitoring/grafana-admin-password"; description = "sops secret for the break-glass local admin password."; };
    };
  };

  config = lib.mkIf cfg.enable (lib.mkMerge [
    {
      # rsyslog populates /var/log/syslog for the otelcol filelog receiver (the
      # contrib image has no journalctl, so journald receiver is not an option).
      services.rsyslogd.enable = true;

      # Shared podman bridge network with a pinned subnet so containers get
      # static IPs (10.89.0.10-14).
      environment.etc."containers/systemd/monitoring.network".text = ''
        [Network]
        NetworkName=monitoring
        Subnet=10.89.0.0/24
        Gateway=10.89.0.1
      '';

      # Writable data directories, owned by each container's non-root uid.
      systemd.tmpfiles.rules = [
        "d ${dataDir} 0750 root root -"
        "d ${dataDir}/victoriametrics 0750 10000 10000 -"
        "d ${dataDir}/victorialogs 0750 10001 10001 -"
        "d ${dataDir}/grafana 0750 472 472 -"
        "d ${dataDir}/alertmanager 0750 65534 65534 -"
      ];

      # --- VictoriaMetrics ------------------------------------------------------
      environment.etc."containers/systemd/victoriametrics.container".text = mkContainer {
        name = "victoriametrics";
        image = cfg.images.victoriametrics;
        ip = "10.89.0.10";
        uid = 10000;
        network = "${net}:ip=10.89.0.10";
        requires = [ "monitoring-network.service" ];
        after = [ "monitoring-network.service" ];
        publishPorts = publishLoopbackAnd 8428 8428;
        volumes = [
          "${dataDir}/victoriametrics:/victoria-metrics-data:Z"
          "${scrapeConfig}:/etc/vmscrape/scrape.yml:ro"
        ];
        exec = "-httpListenAddr=:8428 -retentionPeriod=${cfg.retention.metrics} -storageDataPath=/victoria-metrics-data -opentelemetry.usePrometheusNaming=true -promscrape.config=/etc/vmscrape/scrape.yml";
      };

      # --- VictoriaLogs ---------------------------------------------------------
      environment.etc."containers/systemd/victorialogs.container".text = mkContainer {
        name = "victorialogs";
        image = cfg.images.victorialogs;
        ip = "10.89.0.11";
        uid = 10001;
        network = "${net}:ip=10.89.0.11";
        requires = [ "monitoring-network.service" ];
        after = [ "monitoring-network.service" ];
        publishPorts = publishLoopbackAnd 9428 9428;
        volumes = [ "${dataDir}/victorialogs:/victoria-logs-data:Z" ];
        exec = "-httpListenAddr=:9428 -retentionPeriod=${cfg.retention.logs} -storageDataPath=/victoria-logs-data";
      };

      # --- Alertmanager ---------------------------------------------------------
      environment.etc."containers/systemd/alertmanager.container".text = mkContainer {
        name = "alertmanager";
        image = cfg.images.alertmanager;
        ip = "10.89.0.14";
        uid = 65534;
        network = "${net}:ip=10.89.0.14";
        requires = [ "monitoring-network.service" ];
        after = [ "monitoring-network.service" ];
        publishPorts = publishLoopbackAnd 9093 9093;
        volumes = [
          "${alertmanagerConfigPath}:/etc/alertmanager/alertmanager.yml:ro"
          "${dataDir}/alertmanager:/alertmanager:Z"
        ];
        exec = "--config.file=/etc/alertmanager/alertmanager.yml --storage.path=/alertmanager --web.listen-address=:9093 --web.external-url=${cfg.grafana.rootUrl}";
        timeoutStart = 60;
      };

      # --- vmalert --------------------------------------------------------------
      environment.etc."containers/systemd/vmalert.container".text = mkContainer {
        name = "vmalert";
        image = cfg.images.vmalert;
        ip = "10.89.0.13";
        uid = 10002;
        network = "${net}:ip=10.89.0.13";
        requires = [ "monitoring-network.service" ];
        after = [ "monitoring-network.service" "victoriametrics.service" "alertmanager.service" ];
        publishPorts = publishLoopbackAnd 8880 8880;
        volumes = [ "${rulesDir}:/etc/alerts:ro" ];
        exec = "-httpListenAddr=:8880 -datasource.url=http://victoriametrics:8428 -remoteWrite.url=http://victoriametrics:8428 -remoteRead.url=http://victoriametrics:8428 -notifier.url=http://alertmanager:9093 -rule=/etc/alerts/*.yml -evaluationInterval=30s";
        timeoutStart = 60;
      };

      # --- Grafana --------------------------------------------------------------
      environment.etc."containers/systemd/grafana.container".text = mkContainer {
        name = "grafana";
        image = cfg.images.grafana;
        ip = "10.89.0.12";
        uid = 472;
        network = "${net}:ip=10.89.0.12";
        requires = [ "monitoring-network.service" ];
        after = [ "monitoring-network.service" "victoriametrics.service" "victorialogs.service" ];
        environmentFile = grafanaEnvPath;
        publishPorts = publishLoopbackAnd 3000 3000;
        volumes = [
          "${dataDir}/grafana:/var/lib/grafana:Z"
          "${grafanaProvisioning}:/etc/grafana/provisioning:ro"
          "${grafanaDashboards}:/etc/grafana/dashboards:ro"
        ];
        timeoutStart = 180;
      };

      # --- OpenTelemetry Collector (fleet OTLP gateway + host agent) -------------
      environment.etc."containers/systemd/otelcol.container".text = mkContainer {
        name = "otelcol";
        image = cfg.images.otelcol;
        ip = null;
        uid = 0;
        network = "host";
        after = [ "podman.socket" "victoriametrics.service" "victorialogs.service" ];
        podmanArgs = [ "--pid=host" ];
        volumes = [
          "/proc:/host/proc:ro"
          "/sys:/host/sys:ro"
          "/run/podman/podman.sock:/var/run/docker.sock:ro"
          "/var/log:/var/log:ro"
          "${gatewayConfig}:/etc/otelcol/config.yaml:ro"
        ];
        exec = "--config=/etc/otelcol/config.yaml";
      };
    }

    # OTLP receiver ports are reachable on the NetBird interface only (the fleet
    # ships here over the mesh); never on the public NIC.
    (lib.mkIf netbirdEnabled {
      networking.firewall.interfaces.${netbirdInterface}.allowedTCPPorts = [ 4317 4318 ];
    })

    # Secret-backed config (normal mode): render Alertmanager config + Grafana
    # env through sops so the SMTP password / admin password never hit the store.
    (lib.mkIf (!prototype) {
      sops.secrets.${cfg.smtp.usernameSecret} = { };
      sops.secrets.${cfg.smtp.passwordSecret} = { };
      sops.secrets.${cfg.grafana.adminPasswordSecret} = { };

      sops.templates."portablevps/monitoring/alertmanager.yml" = {
        mode = "0400";
        content = builtins.replaceStrings
          [ "PLACEHOLDER_SMTP_USERNAME" "PLACEHOLDER_SMTP_PASSWORD" ]
          [ config.sops.placeholder.${cfg.smtp.usernameSecret} config.sops.placeholder.${cfg.smtp.passwordSecret} ]
          alertmanagerConfig;
      };

      sops.templates."portablevps/monitoring/grafana.env" = {
        mode = "0400";
        content = ''
          GF_SERVER_ROOT_URL=${cfg.grafana.rootUrl}
          GF_SECURITY_ADMIN_USER=admin
          GF_SECURITY_ADMIN_PASSWORD=${config.sops.placeholder.${cfg.grafana.adminPasswordSecret}}
          GF_USERS_ALLOW_SIGN_UP=false
          GF_AUTH_ANONYMOUS_ENABLED=false
          GF_ANALYTICS_REPORTING_ENABLED=false
          GF_ANALYTICS_CHECK_FOR_UPDATES=false
        '';
      };
    })

    # Prototype/local mode: demo config so the stack boots without sops.
    (lib.mkIf prototype {
      environment.etc."portablevps/monitoring/alertmanager.yml".text = alertmanagerConfig;
      environment.etc."portablevps/monitoring/grafana.env".text = ''
        GF_SERVER_ROOT_URL=${cfg.grafana.rootUrl}
        GF_SECURITY_ADMIN_USER=admin
        GF_SECURITY_ADMIN_PASSWORD=demo-admin
        GF_USERS_ALLOW_SIGN_UP=false
        GF_AUTH_ANONYMOUS_ENABLED=false
      '';
    })
  ]);
}

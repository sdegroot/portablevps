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
  scrapeConfig = ./scrape.yml;
  grafanaProvisioning = ./grafana/provisioning;
  grafanaDashboards = ./grafana/dashboards;

  # Per-host presence alerts are generated from the fleet lists (one absent()
  # alert per expected host, so a dark node keeps firing). Emitted as JSON —
  # valid YAML — to sidestep indentation. Combined with the static rules
  # (./rules/*.yml) into one directory mounted at /etc/alerts.
  mkPresenceFile = name: group:
    pkgs.writeText name (builtins.toJSON { groups = [ group ]; });

  fleetPresenceFile = mkPresenceFile "fleet-presence.yml" {
    name = "fleet-presence";
    interval = "30s";
    rules = map (host: {
      alert = "NodeMetricsMissing";
      expr = ''absent(system_cpu_time_seconds_total{host_name="${host}"})'';
      "for" = "5m";
      labels = { severity = "critical"; host_name = host; };
      annotations = {
        summary = "No metrics from ${host}";
        description = "VictoriaMetrics has received no metrics from ${host} for over 5 minutes — the host is down, its otelcol stopped, or its OTLP endpoint is misconfigured.";
      };
    }) cfg.monitoredHosts;
  };

  backupPresenceFile = mkPresenceFile "backup-presence.yml" {
    name = "backup-presence";
    interval = "1m";
    rules = map (host: {
      alert = "BackupMetricAbsent";
      expr = ''absent(portablevps_backup_last_success_timestamp_seconds{host_name="${host}"})'';
      "for" = "90m";
      labels = { severity = "critical"; host_name = host; };
      annotations = {
        summary = "No backup metric from ${host}";
        description = "VictoriaMetrics has not seen portablevps_backup_last_success_timestamp_seconds for ${host} — either no restic backup has succeeded since deploy, or the reporter is broken.";
      };
    }) cfg.backupHosts;
  };

  alertsDir = pkgs.runCommand "portablevps-monitoring-rules" { } ''
    mkdir -p "$out"
    cp ${./rules}/*.yml "$out"/
    ${lib.optionalString (cfg.monitoredHosts != [ ]) ''cp ${fleetPresenceFile} "$out/fleet-presence.yml"''}
    ${lib.optionalString (cfg.backupHosts != [ ]) ''cp ${backupPresenceFile} "$out/backup-presence.yml"''}
  '';

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

  oidc = cfg.grafana.oidc;
  # OIDC is wired only in normal mode with an issuer set — it needs authentik
  # reachable and a real client secret, neither of which exists in a local VM.
  grafanaOidcEnabled = (!prototype) && oidc.enable && oidc.issuerBaseUrl != "";

  grafanaCommonEnv = ''
    GF_SERVER_ROOT_URL=${cfg.grafana.rootUrl}
    GF_SECURITY_ADMIN_USER=admin
    GF_USERS_ALLOW_SIGN_UP=false
    GF_AUTH_ANONYMOUS_ENABLED=false
    GF_ANALYTICS_REPORTING_ENABLED=false
    GF_ANALYTICS_CHECK_FOR_UPDATES=false
    GF_INSTALL_PLUGINS=${lib.concatStringsSep "," cfg.grafana.plugins}
  '';

  grafanaOidcEnv = clientSecret: ''
    GF_AUTH_GENERIC_OAUTH_ENABLED=true
    GF_AUTH_GENERIC_OAUTH_NAME=${oidc.name}
    GF_AUTH_GENERIC_OAUTH_CLIENT_ID=${oidc.clientId}
    GF_AUTH_GENERIC_OAUTH_CLIENT_SECRET=${clientSecret}
    GF_AUTH_GENERIC_OAUTH_SCOPES=openid email profile
    GF_AUTH_GENERIC_OAUTH_AUTH_URL=${oidc.issuerBaseUrl}/application/o/authorize/
    GF_AUTH_GENERIC_OAUTH_TOKEN_URL=${oidc.issuerBaseUrl}/application/o/token/
    GF_AUTH_GENERIC_OAUTH_API_URL=${oidc.issuerBaseUrl}/application/o/userinfo/
    GF_AUTH_GENERIC_OAUTH_USE_PKCE=true
    GF_AUTH_GENERIC_OAUTH_ALLOW_SIGN_UP=true
    GF_AUTH_GENERIC_OAUTH_ROLE_ATTRIBUTE_PATH=${oidc.roleAttributePath}
  '';

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

    monitoredHosts = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "host.name each fleet server stamps on its telemetry. One NodeMetricsMissing (absent) alert is generated per host, so a dark node keeps firing instead of silently ageing out.";
    };

    backupHosts = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Hosts expected to push a backup-success metric. One BackupMetricAbsent alert generated per host (catches a reporter broken since deploy, which a staleness rule cannot).";
    };

    alertEmailFrom = lib.mkOption { type = lib.types.str; default = "alerts@epistola.io"; description = "Alert email From address."; };
    alertEmailTo = lib.mkOption { type = lib.types.str; default = "sander@degroot.dev"; description = "Alert email recipient."; };
    alertRepeatInterval = lib.mkOption { type = lib.types.str; default = "4h"; description = "Alertmanager repeat interval for a still-firing alert."; };

    grafana = {
      rootUrl = lib.mkOption { type = lib.types.str; default = "https://grafana.int.epistola.io"; description = "GF_SERVER_ROOT_URL (the mesh proxy name)."; };
      adminPasswordSecret = lib.mkOption { type = lib.types.str; default = "monitoring/grafana-admin-password"; description = "sops secret for the break-glass local admin password."; };
      plugins = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ "victoriametrics-metrics-datasource 0.24.0" "victoriametrics-logs-datasource 0.27.1" ];
        description = "Datasource plugins installed at container start (GF_INSTALL_PLUGINS, comma-joined).";
      };
      extraHosts = lib.mkOption {
        type = lib.types.attrsOf lib.types.str;
        default = { };
        example = { "auth.int.epistola.io" = "100.85.212.44"; };
        description = ''
          Static host:ip entries added to the Grafana container (podman --add-host).
          Grafana's server-side OIDC calls must resolve the authentik issuer, but
          podman containers can't resolve NetBird mesh names: aardvark can't forward
          to the host's systemd-resolved stub, and the NetBird resolver is firewalled
          off the podman bridge. An /etc/hosts entry to the issuer's mesh IP resolves
          it without disturbing container-name resolution (aardvark). Interim measure
          — a split-DNS forwarder reachable from the bridge is the clean general fix.
        '';
      };
      oidc = {
        enable = lib.mkEnableOption "Authentik (generic OAuth) SSO for Grafana; the local admin login stays as break-glass";
        name = lib.mkOption { type = lib.types.str; default = "Authentik"; description = "OAuth provider display name."; };
        clientId = lib.mkOption { type = lib.types.str; default = "grafana"; description = "OAuth client id."; };
        clientSecretSecret = lib.mkOption { type = lib.types.str; default = "monitoring/grafana-oauth-secret"; description = "sops secret holding the OAuth client secret."; };
        issuerBaseUrl = lib.mkOption { type = lib.types.str; default = ""; description = "authentik base URL, e.g. https://auth.epistola.app; the authorize/token/userinfo URLs derive from it."; };
        roleAttributePath = lib.mkOption {
          type = lib.types.str;
          default = "(contains(groups, 'epistola_employees') || ends_with(email, '@epistola.app')) && 'Editor' || 'Viewer'";
          description = "JMESPath mapping OIDC claims to a Grafana org role.";
        };
      };
    };
  };

  config = lib.mkIf cfg.enable (lib.mkMerge [
    {
      # Shared podman bridge network with a pinned subnet so containers get
      # static IPs (10.89.0.10-14).
      environment.etc."containers/systemd/monitoring.network".text = ''
        [Network]
        NetworkName=monitoring
        Subnet=10.89.0.0/24
        Gateway=10.89.0.1
        # aardvark-dns (podman's built-in) resolves this network's container names
        # and forwards everything else to its upstream. Point that upstream at
        # systemd-resolved (127.0.0.53) so NetBird mesh + public names resolve via
        # the host's split-DNS. aardvark runs in the host netns, so it CAN reach the
        # loopback stub the containers themselves can't. Container->gateway:53 is
        # unblocked by trusting podman bridges (see modules/runtime/podman.nix).
        DNS=127.0.0.53
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
        volumes = [ "${alertsDir}:/etc/alerts:ro" ];
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
        # Static host entries so server-side OIDC (and any other mesh calls) can
        # resolve mesh names the container's resolver can't (see grafana.extraHosts).
        podmanArgs = lib.mapAttrsToList (h: ip: "--add-host=${h}:${ip}") cfg.grafana.extraHosts;
        environmentFile = grafanaEnvPath;
        publishPorts = publishLoopbackAnd 3000 3000;
        volumes = [
          "${dataDir}/grafana:/var/lib/grafana:Z"
          "${grafanaProvisioning}:/etc/grafana/provisioning:ro"
          "${grafanaDashboards}:/etc/grafana/dashboards:ro"
        ];
        timeoutStart = 180;
      };

      # --- OpenTelemetry Collector: fleet OTLP gateway + this host's agent ------
      # Native (not containerized) so the journald receiver reads the journal
      # directly and podman_stats talks podman's native API — no rsyslog, no
      # /var/log/syslog file, no docker_stats parse errors. Receives OTLP from the
      # fleet, scrapes this host, and exports to VictoriaMetrics/Logs on loopback.
      services.opentelemetry-collector = {
        enable = true;
        package = pkgs.opentelemetry-collector-contrib;
        settings = {
          receivers = {
            otlp.protocols = {
              grpc.endpoint = "0.0.0.0:4317";
              http.endpoint = "0.0.0.0:4318";
            };
            hostmetrics = {
              collection_interval = "30s";
              scrapers = {
                cpu = { }; memory = { }; disk = { }; filesystem = { };
                load = { }; network = { }; processes = { };
              };
            };
            podman_stats = { endpoint = "unix:///run/podman/podman.sock"; collection_interval = "30s"; };
            journald = { };
          };
          processors = {
            # fleet metrics can arrive as delta; VM wants cumulative.
            deltatocumulative = { };
            batch = { timeout = "10s"; send_batch_size = 1024; };
            # override:false enriches THIS host's own telemetry without clobbering
            # the host.name the fleet's collectors already stamped.
            resourcedetection = { detectors = [ "system" "env" ]; system.hostname_sources = [ "os" ]; override = false; };
            resource.attributes = [ { key = "host.id"; from_attribute = "host.name"; action = "upsert"; } ];
          };
          exporters = {
            "otlphttp/victoriametrics" = {
              metrics_endpoint = "http://127.0.0.1:8428/opentelemetry/v1/metrics";
              encoding = "proto";
              compression = "gzip";
            };
            # The journald receiver lands the log text in a `MESSAGE` field, not the
            # OTLP body, so VictoriaLogs' canonical `_msg` stays empty ("missing _msg
            # field") and full-text search misses these lines. Tell VictoriaLogs to
            # use MESSAGE as _msg on ingestion — keeps every other journal field.
            "otlphttp/victorialogs".logs_endpoint = "http://127.0.0.1:9428/insert/opentelemetry/v1/logs?_msg_field=MESSAGE";
          };
          service = {
            telemetry = { logs.level = "info"; metrics.level = "none"; };
            pipelines = {
              metrics = {
                receivers = [ "otlp" "hostmetrics" "podman_stats" ];
                processors = [ "deltatocumulative" "batch" "resourcedetection" "resource" ];
                exporters = [ "otlphttp/victoriametrics" ];
              };
              logs = {
                receivers = [ "otlp" "journald" ];
                processors = [ "batch" "resourcedetection" "resource" ];
                exporters = [ "otlphttp/victorialogs" ];
              };
            };
          };
        };
      };

      systemd.services.opentelemetry-collector = {
        after = [ "victoriametrics.service" "victorialogs.service" "podman.socket" ];
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
    }

    # OTLP receiver ports are reachable on the NetBird interface only (the fleet
    # ships here over the mesh); never on the public NIC.
    (lib.mkIf netbirdEnabled {
      networking.firewall.interfaces.${netbirdInterface}.allowedTCPPorts = [ 4317 4318 ];
    })

    # Secret-backed config (normal mode): render Alertmanager config + Grafana
    # env through sops so the SMTP password / admin password never hit the store.
    (lib.mkIf (!prototype) {
      sops.secrets = {
        ${cfg.smtp.usernameSecret} = { };
        ${cfg.smtp.passwordSecret} = { };
        ${cfg.grafana.adminPasswordSecret} = { };
      } // lib.optionalAttrs grafanaOidcEnabled { ${oidc.clientSecretSecret} = { }; };

      sops.templates."portablevps/monitoring/alertmanager.yml" = {
        # Alertmanager (uid 65534) reads this config itself (volume mount, not an
        # EnvironmentFile), so it must be owned by the container's uid.
        owner = "nobody";
        mode = "0400";
        content = builtins.replaceStrings
          [ "PLACEHOLDER_SMTP_USERNAME" "PLACEHOLDER_SMTP_PASSWORD" ]
          [ config.sops.placeholder.${cfg.smtp.usernameSecret} config.sops.placeholder.${cfg.smtp.passwordSecret} ]
          alertmanagerConfig;
      };

      sops.templates."portablevps/monitoring/grafana.env" = {
        mode = "0400";
        content = grafanaCommonEnv
          + "GF_SECURITY_ADMIN_PASSWORD=${config.sops.placeholder.${cfg.grafana.adminPasswordSecret}}\n"
          + lib.optionalString grafanaOidcEnabled (grafanaOidcEnv config.sops.placeholder.${oidc.clientSecretSecret});
      };
    })

    # Prototype/local mode: demo config so the stack boots without sops.
    (lib.mkIf prototype {
      # Demo config (no real secrets) — world-readable so the alertmanager
      # container (uid 65534) can read the volume-mounted file.
      environment.etc."portablevps/monitoring/alertmanager.yml" = {
        mode = "0444";
        text = alertmanagerConfig;
      };
      environment.etc."portablevps/monitoring/grafana.env".text =
        grafanaCommonEnv + "GF_SECURITY_ADMIN_PASSWORD=demo-admin\n";
    })
  ]);
}

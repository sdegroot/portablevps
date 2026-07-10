# Provides a NetBird-first Traefik proxy for hostname and SNI based routing.
{ config, lib, pkgs, ... }:

let
  cfg = config.portablevps.proxy;
  netbirdEnabled = config.portablevps.network.enable or false;
  # This is a NetBird-first proxy: its internal routes bind the mesh interface
  # and ACME uses the operator's DNS. Where there is no mesh (a local VM), it
  # goes inert — the server keeps its proxy config, but no Traefik is built.
  # (The proxy follows the mesh, not the secrets mode.)
  proxyInert = !netbirdEnabled;
  netbirdName = config.portablevps.network.name or config.networking.hostName;
  netbirdInterface = config.portablevps.network.interface;
  stagingCaServer = "https://acme-staging-v02.api.letsencrypt.org/directory";
  acmeResolverName =
    if cfg.acme.resolverName != null
    then cfg.acme.resolverName
    else "dns01-${cfg.acme.environment}";
  acmeCaServer =
    if cfg.acme.caServer != null
    then cfg.acme.caServer
    else if cfg.acme.environment == "staging"
    then stagingCaServer
    else "";
  # True when any meaningful proxy sub-option is set. Used to fail evaluation
  # when a server configures the proxy but forgets portablevps.proxy.enable = true,
  # which would otherwise leave Traefik and ACME silently inert.
  proxyConfigured =
    cfg.testBackend.enable
    || cfg.http.services != { }
    || cfg.tcp.services != { }
    || cfg.acme.email != ""
    || cfg.acme.dnsProvider != ""
    || cfg.dns.managedZones != [ ]
    || cfg.dns.acmeDelegatedZone != null;
  visibilityType = lib.types.enum [ "internal" "netbird-edge" "direct-public" ];

  httpServiceType = lib.types.submodule {
    options = {
      domain = lib.mkOption {
        type = lib.types.str;
        description = "Primary DNS name routed to this HTTP service.";
      };

      aliases = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = "Additional DNS names routed to this HTTP service.";
      };

      upstream = lib.mkOption {
        type = lib.types.str;
        description = "HTTP upstream URL, for example http://127.0.0.1:3000.";
      };

      visibility = lib.mkOption {
        type = visibilityType;
        default = "internal";
        description = "Ingress path: internal (mesh-only), netbird-edge (public via the mesh edge), or direct-public.";
      };

      extraRouterConfig = lib.mkOption {
        type = lib.types.attrs;
        default = { };
        description = "Extra Traefik HTTP router configuration.";
      };

      deniedPathPrefixes = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        example = [ "/admin" "/api/v1/admin" ];
        description = ''
          Path prefixes shadowed by a higher-priority router that uses
          Traefik's noop internal service. This is intended for public routes
          that should expose the same backend with selected surfaces hidden.
        '';
      };
    };
  };

  tcpServiceType = lib.types.submodule {
    options = {
      domain = lib.mkOption {
        type = lib.types.str;
        description = "SNI name routed to this TCP service.";
      };

      targetHost = lib.mkOption {
        type = lib.types.str;
        default = "127.0.0.1";
        description = "TCP upstream host.";
      };

      targetPort = lib.mkOption {
        type = lib.types.port;
        description = "TCP upstream port.";
      };

      tlsPassthrough = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Pass TLS through to the upstream service.";
      };

      visibility = lib.mkOption {
        type = visibilityType;
        default = "internal";
        description = "Ingress path: internal (mesh-only), netbird-edge (public via the mesh edge), or direct-public.";
      };
    };
  };

  hostRule = names:
    lib.concatStringsSep " || " (map (name: "Host(`${name}`)") names);

  hostSniRule = domain: "HostSNI(`${domain}`)";

  pathPrefixRule = prefixes:
    lib.concatStringsSep " || " (map (prefix: "PathPrefix(`${prefix}`)") prefixes);

  testHttpServices = lib.optionalAttrs cfg.testBackend.enable {
    proxy-test = {
      domain = cfg.testBackend.domain;
      aliases = [ ];
      upstream = "http://127.0.0.1:${toString cfg.testBackend.port}";
      visibility = cfg.testBackend.visibility;
      extraRouterConfig = { };
    };
  };

  httpServicesConfig = cfg.http.services // testHttpServices;
  normalizeDnsName = name:
    if lib.hasSuffix "." name then name else "${name}.";
  normalizeDomain = name:
    lib.removeSuffix "." name;
  normalizePublicTarget = target:
    if cfg.dns.publicRecordType == "CNAME" then normalizeDnsName target else target;
  acmeChallengeName = domain: "_acme-challenge.${domain}.";
  domainIsInZone = domain: zone:
    let
      normalizedDomain = normalizeDomain domain;
      normalizedZone = normalizeDomain zone;
    in
    normalizedDomain == normalizedZone || lib.hasSuffix ".${normalizedZone}" normalizedDomain;
  managedAcmeZoneFor = domain:
    lib.findFirst (zone: domainIsInZone domain zone) null cfg.dns.managedZones;
  acmeChallengeTarget = domain:
    if managedAcmeZoneFor domain != null
    then null
    else if cfg.dns.acmeDelegatedZone == null
    then null
    else "_acme-challenge.${domain}.${normalizeDnsName cfg.dns.acmeDelegatedZone}";
  netbirdDnsTarget =
    if cfg.dns.netbirdCnameTarget != null
    then normalizeDnsName cfg.dns.netbirdCnameTarget
    else normalizeDnsName netbirdName;
  publicDnsTarget =
    if cfg.dns.publicTarget != null
    then normalizePublicTarget cfg.dns.publicTarget
    else null;

  httpRouters = lib.mapAttrs
    (name: service:
      {
        rule = hostRule ([ service.domain ] ++ service.aliases);
        entryPoints = [ cfg.entryPointName ];
        service = name;
        tls = {
          options = "default";
        } // lib.optionalAttrs cfg.acme.enable {
          certResolver = acmeResolverName;
        };
      } // service.extraRouterConfig)
    httpServicesConfig;

  httpDenyRouters = lib.listToAttrs (lib.flatten (lib.mapAttrsToList
    (name: service:
      lib.optional ((service.deniedPathPrefixes or [ ]) != [ ]) {
        name = "${name}-denied-paths";
        value = {
          rule = "(${hostRule ([ service.domain ] ++ service.aliases)}) && (${pathPrefixRule (service.deniedPathPrefixes or [ ])})";
          entryPoints = [ cfg.entryPointName ];
          service = "noop@internal";
          priority = 10000;
          tls = {
            options = "default";
          } // lib.optionalAttrs cfg.acme.enable {
            certResolver = acmeResolverName;
          };
        };
      })
    httpServicesConfig));

  httpServices = lib.mapAttrs
    (_name: service: {
      loadBalancer.servers = [
        { url = service.upstream; }
      ];
    })
    httpServicesConfig;

  tcpRouters = lib.mapAttrs
    (name: service: {
      rule = hostSniRule service.domain;
      entryPoints = [ cfg.entryPointName ];
      service = name;
      tls.passthrough = service.tlsPassthrough;
    })
    cfg.tcp.services;

  tcpServices = lib.mapAttrs
    (_name: service: {
      loadBalancer.servers = [
        { address = "${service.targetHost}:${toString service.targetPort}"; }
      ];
    })
    cfg.tcp.services;

  hasRoutes = httpServicesConfig != { } || cfg.tcp.services != { };
  httpDomainEntries = lib.flatten (lib.mapAttrsToList
    (serviceName: service:
      map
        (domain: {
          inherit domain serviceName;
          routeType = "http";
          visibility = service.visibility;
          upstream = service.upstream;
        })
        ([ service.domain ] ++ service.aliases))
    httpServicesConfig);
  tcpDomainEntries = lib.mapAttrsToList
    (serviceName: service: {
      domain = service.domain;
      inherit serviceName;
      routeType = "tcp";
      visibility = service.visibility;
      upstream = "${service.targetHost}:${toString service.targetPort}";
    })
    cfg.tcp.services;
  domainEntries = httpDomainEntries ++ tcpDomainEntries;
  hasDirectPublicRoutes = lib.any (entry: entry.visibility == "direct-public") domainEntries;
  hasNonDirectPublicRoutes = lib.any (entry: entry.visibility != "direct-public") domainEntries;
  domainPlanEntries = map
    (entry: entry // {
      acme = lib.optionalAttrs cfg.acme.enable {
        challenge = acmeChallengeName entry.domain;
        mode =
          if managedAcmeZoneFor entry.domain != null
          then "managed-zone"
          else "cname-delegation";
        managedZone = managedAcmeZoneFor entry.domain;
        target = acmeChallengeTarget entry.domain;
        resolver = acmeResolverName;
      };
      dns = {
        public = if (entry.visibility == "netbird-edge" || entry.visibility == "direct-public") && publicDnsTarget != null then {
          type = cfg.dns.publicRecordType;
          name = normalizeDnsName entry.domain;
          target = publicDnsTarget;
          purpose =
            if entry.visibility == "netbird-edge"
            then "Public clients enter through the NetBird reverse proxy edge."
            else "Public clients connect directly to the VPS public ingress target.";
        } else null;
        netbird =
          if entry.visibility == "internal" || (entry.visibility == "netbird-edge" && cfg.dns.netbirdEdgeOverrides) then {
            type = "CNAME";
            name = normalizeDnsName entry.domain;
            target = netbirdDnsTarget;
            purpose =
              if entry.visibility == "netbird-edge"
              then "Split-horizon override for connected NetBird clients."
              else "Internal service DNS for connected NetBird clients.";
          } else null;
      };
    })
    domainEntries;
  domainPlan = {
    server = config.networking.hostName;
    netbirdName = netbirdName;
    publicTarget = publicDnsTarget;
    publicRecordType = cfg.dns.publicRecordType;
    netbirdCnameTarget = netbirdDnsTarget;
    acmeDelegatedZone = cfg.dns.acmeDelegatedZone;
    managedZones = cfg.dns.managedZones;
    domains = domainPlanEntries;
  };
  domainPlanFile = pkgs.writeText "portablevps-proxy-domains.json" (builtins.toJSON domainPlan);
  domainPlanCommand = pkgs.writeShellScriptBin "portablevps-proxy-domain-plan" ''
    exec ${pkgs.jq}/bin/jq . ${domainPlanFile}
  '';

  testBackendScript = pkgs.writeText "portablevps-proxy-test-backend.py" ''
    from http.server import BaseHTTPRequestHandler, HTTPServer

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            body = b"portablevps proxy test ok\n"
            self.send_response(200)
            self.send_header("content-type", "text/plain")
            self.send_header("content-length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, fmt, *args):
            return

    HTTPServer(("127.0.0.1", ${toString cfg.testBackend.port}), Handler).serve_forever()
  '';

  testCertificate = pkgs.runCommand "portablevps-proxy-test-certificate"
    { nativeBuildInputs = [ pkgs.openssl ]; }
    ''
      mkdir -p "$out"
      openssl req \
        -x509 \
        -newkey rsa:2048 \
        -nodes \
        -days 30 \
        -subj ${lib.escapeShellArg "/CN=${cfg.testBackend.domain}"} \
        -addext ${lib.escapeShellArg "subjectAltName = DNS:${cfg.testBackend.domain}"} \
        -keyout "$out/key.pem" \
        -out "$out/cert.pem"
    '';
in
{
  imports = [ ./network.nix ];

  options.portablevps.proxy = {
    enable = lib.mkEnableOption "NetBird-first Traefik proxy";

    entryPointName = lib.mkOption {
      type = lib.types.str;
      default = "websecure";
      description = "Traefik entry point used for application HTTPS traffic.";
    };

    port = lib.mkOption {
      type = lib.types.port;
      default = 443;
      description = "Local HTTPS/TLS port served by Traefik.";
    };

    openPublicFirewall = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Open the proxy port on the public host firewall.";
    };

    acme = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Enable ACME DNS-01 certificate issuance.";
      };

      email = lib.mkOption {
        type = lib.types.str;
        default = "";
        description = "ACME account email used by Traefik.";
      };

      dnsProvider = lib.mkOption {
        type = lib.types.str;
        default = "";
        description = "Traefik/lego DNS provider name used for DNS-01 validation.";
      };

      environmentFile = lib.mkOption {
        type = lib.types.str;
        default = "/etc/portablevps/traefik-acme.env";
        description = "Environment file containing DNS provider credentials for Traefik ACME.";
      };

      environment = lib.mkOption {
        type = lib.types.enum [ "production" "staging" ];
        default = "production";
        description = "ACME issuance environment. Staging uses Let's Encrypt staging and a separate resolver name.";
      };

      caServer = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Optional ACME CA directory URL override. Defaults to Let's Encrypt staging only when portablevps.proxy.acme.environment is staging.";
      };

      resolverName = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Optional Traefik certificate resolver name override. Defaults to dns01-production or dns01-staging.";
      };

      propagationDelayBeforeChecks = lib.mkOption {
        type = lib.types.ints.unsigned;
        default = 0;
        description = "Seconds Traefik waits before checking DNS-01 TXT propagation.";
      };

      dnsResolvers = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ "9.9.9.9:53" "1.1.1.1:53" ];
        description = ''
          Nameservers lego uses for ACME DNS-01 zone discovery and propagation
          checks. Defaults to public resolvers so the challenge is resolved
          against public authoritative DNS, not the host's system resolver.
          Essential on hosts where NetBird split-DNS routes the managed zone
          (e.g. int.epistola.io) to the mesh resolver: that resolver only knows
          peer names and returns REFUSED for `_acme-challenge` lookups, which
          otherwise fails issuance ("could not find zone"). Set to [] to use the
          system resolver.
        '';
      };
    };

    dns = {
      acmeDelegatedZone = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = "acme.portablevps.io";
        description = "Delegated DNS zone that receives ACME DNS-01 TXT records.";
      };

      managedZones = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        example = [ "int.portablevps.io" ];
        description = "DNS zones delegated to the ACME provider where Traefik can create challenge TXT records directly.";
      };

      publicTarget = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = "eu1.netbird.services.";
        description = "Public DNS target for services marked visibility = netbird-edge or direct-public.";
      };

      publicRecordType = lib.mkOption {
        type = lib.types.enum [ "CNAME" "A" "AAAA" ];
        default = "CNAME";
        description = "DNS record type used for generated public DNS records.";
      };

      netbirdCnameTarget = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        example = "test-vps.portablevps.int.";
        description = "Private NetBird DNS CNAME target. Defaults to the configured NetBird peer name.";
      };

      netbirdEdgeOverrides = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Also emit NetBird private DNS records for services marked
          visibility = "netbird-edge". This supports split-horizon DNS where
          public clients enter through the NetBird reverse proxy edge, while
          connected NetBird clients resolve the same canonical hostname directly
          to the serving peer.
        '';
      };
    };

    http.services = lib.mkOption {
      type = lib.types.attrsOf httpServiceType;
      default = { };
      description = "HTTP services routed by hostname.";
    };

    tcp.services = lib.mkOption {
      type = lib.types.attrsOf tcpServiceType;
      default = { };
      description = "TCP services routed by SNI.";
    };

    testBackend = {
      enable = lib.mkEnableOption "local proxy smoke-test HTTP backend";

      domain = lib.mkOption {
        type = lib.types.str;
        default = "proxy-test.portablevps.int";
        description = "Hostname routed to the local proxy smoke-test backend.";
      };

      port = lib.mkOption {
        type = lib.types.port;
        default = 18080;
        description = "Loopback port for the local proxy smoke-test backend.";
      };

      visibility = lib.mkOption {
        type = visibilityType;
        default = "internal";
        description = "Ingress path for the proxy smoke-test backend.";
      };
    };
  };

  config = lib.mkMerge [
    {
      assertions = [
        {
          assertion = proxyInert || cfg.enable || !proxyConfigured;
          message = "portablevps.proxy options are configured but portablevps.proxy.enable is false. Set portablevps.proxy.enable = true or remove the proxy configuration.";
        }
      ];
    }

    (lib.mkIf (cfg.enable && !proxyInert) (lib.mkMerge [
    {
      assertions = [
        {
          assertion = !cfg.acme.enable || cfg.acme.email != "";
          message = "portablevps.proxy.acme.email is required when portablevps.proxy.enable is true.";
        }
        {
          assertion = !cfg.acme.enable || cfg.acme.dnsProvider != "";
          message = "portablevps.proxy.acme.dnsProvider is required when portablevps.proxy.enable is true.";
        }
        {
          assertion = lib.all (service: service.tlsPassthrough) (lib.attrValues cfg.tcp.services);
          message = "portablevps.proxy.tcp.services currently supports only tlsPassthrough = true.";
        }
        {
          assertion = !hasDirectPublicRoutes || cfg.openPublicFirewall;
          message = "portablevps.proxy.openPublicFirewall must be true when any proxy route has visibility = \"direct-public\".";
        }
        {
          assertion = !cfg.openPublicFirewall || !hasNonDirectPublicRoutes;
          message = "portablevps.proxy.openPublicFirewall can only be true when all proxy routes have visibility = \"direct-public\".";
        }
      ];

      services.traefik = {
        enable = true;
        environmentFiles = lib.optional cfg.acme.enable cfg.acme.environmentFile;
        staticConfigOptions = lib.mkMerge [
          {
            entryPoints.${cfg.entryPointName}.address = ":${toString cfg.port}";
          }
          (lib.mkIf cfg.acme.enable {
            certificatesResolvers.${acmeResolverName}.acme = {
              email = cfg.acme.email;
              storage = "${config.services.traefik.dataDir}/acme.json";
              dnsChallenge = {
                provider = cfg.acme.dnsProvider;
              } // lib.optionalAttrs (cfg.acme.propagationDelayBeforeChecks > 0) {
                propagation.delayBeforeChecks = cfg.acme.propagationDelayBeforeChecks;
              } // lib.optionalAttrs (cfg.acme.dnsResolvers != [ ]) {
                resolvers = cfg.acme.dnsResolvers;
              };
            } // lib.optionalAttrs (acmeCaServer != "") {
              caServer = acmeCaServer;
            };
          })
        ];
        dynamicConfigOptions = lib.mkIf hasRoutes {
          http = lib.mkIf (httpServicesConfig != { }) {
            routers = httpRouters // httpDenyRouters;
            services = httpServices;
          };
          tcp = lib.mkIf (cfg.tcp.services != { }) {
            routers = tcpRouters;
            services = tcpServices;
          };
          tls = lib.mkIf (!cfg.acme.enable && cfg.testBackend.enable) {
            stores.default.defaultCertificate = {
              certFile = "${testCertificate}/cert.pem";
              keyFile = "${testCertificate}/key.pem";
            };
          };
        };
      };

      environment.etc."portablevps/proxy-domains.json".source = domainPlanFile;
      environment.systemPackages = [ domainPlanCommand ];

      # ACME certificates are service/proxy state. Backing up acme.json lets a
      # restored or migrated host present the existing certificate immediately,
      # while normal DNS-01 issuance remains responsible for renewal. This rides
      # on top of an existing backup destination: it only registers when the
      # host actually has a restic repository, so enabling the proxy on a
      # disposable, backup-less host (e.g. a monitoring server) does not conscript
      # it into needing backup infrastructure just to serve UI names over TLS.
      portablevps.backups.components."traefik-acme" =
        lib.mkIf (cfg.acme.enable && config.portablevps.backups.restic.repository != "") {
          order = 15;
          paths = [ "${config.services.traefik.dataDir}/acme.json" ];
          clearBeforeRestore = [ "${config.services.traefik.dataDir}/acme.json" ];
        };
    }

    (lib.mkIf (cfg.acme.enable && !config.portablevps.secrets.allowPrototypeDefaults) {
      sops.secrets."traefik/acme-env" = {
        path = cfg.acme.environmentFile;
        owner = "traefik";
        group = "traefik";
        mode = "0400";
      };
    })

    (lib.mkIf (cfg.acme.enable && config.portablevps.secrets.allowPrototypeDefaults) {
      environment.etc."portablevps/traefik-acme.env" = {
        mode = "0400";
        text = ''
          # Prototype placeholder. Configure DNS provider credentials before enabling ACME issuance.
        '';
      };
    })

    (lib.mkIf cfg.testBackend.enable {
      systemd.services.portablevps-proxy-test-backend = {
        description = "portablevps proxy smoke-test backend";
        wantedBy = lib.optional (!config.portablevps.restoreMode) "apps.target";
        partOf = [ "apps.target" ];
        serviceConfig = {
          Type = "simple";
          Restart = "always";
          RestartSec = "5s";
          ExecStart = "${pkgs.python3}/bin/python3 ${testBackendScript}";
        };
      };
    })

    (lib.mkIf (netbirdEnabled && !config.portablevps.restoreMode) {
      networking.firewall.interfaces.${netbirdInterface}.allowedTCPPorts = [ cfg.port ];
    })

    (lib.mkIf cfg.openPublicFirewall {
      networking.firewall.allowedTCPPorts = [ cfg.port ];
    })

    (lib.mkIf config.portablevps.restoreMode {
      systemd.services.traefik.wantedBy = lib.mkForce [ ];
    })
    ]))
  ];
}

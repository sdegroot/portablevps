# Temporarily opens public SSH from approved source ranges when Netbird access is unhealthy.
{ config, lib, pkgs, ... }:

let
  cfg = config.my.breakGlassSsh;
  chain = "EPIS_BGLASS_SSH";
  countrySet4 = "EPIS_NL4";
  stateDir = "/var/lib/epistola-break-glass-ssh";
  allowedCidrs = lib.escapeShellArgs cfg.allowedCidrs;
  countryCodes = lib.escapeShellArgs cfg.countryCodes;
  nlZoneUrl = cfg.countryZoneUrls.nl or "https://www.ipdeny.com/ipblocks/data/countries/nl.zone";
  watchdog = pkgs.writeShellScript "epistola-break-glass-ssh" ''
    set -eu

    iptables="${pkgs.iptables}/bin/iptables"
    ip6tables="${pkgs.iptables}/bin/ip6tables"
    ipset="${pkgs.ipset}/bin/ipset"
    ip="${pkgs.iproute2}/bin/ip"
    systemctl="${pkgs.systemd}/bin/systemctl"
    date="${pkgs.coreutils}/bin/date"
    find="${pkgs.findutils}/bin/find"
    grep="${pkgs.gnugrep}/bin/grep"
    curl="${pkgs.curl}/bin/curl"

    chain=${lib.escapeShellArg chain}
    country_set4=${lib.escapeShellArg countrySet4}
    iface=${lib.escapeShellArg cfg.netbirdInterface}
    port=${toString cfg.port}
    close_after_seconds=${toString cfg.closeAfterSeconds}
    state_dir=${lib.escapeShellArg stateDir}
    open_state="$state_dir/open"
    recovered_at_state="$state_dir/recovered-at"
    nl_zone_url=${lib.escapeShellArg nlZoneUrl}
    nl_zone_file="$state_dir/nl.zone"
    nl_zone_bundled=${lib.escapeShellArg "${cfg.countryZoneFiles.nl}"}
    allowed_cidrs=(${allowedCidrs})
    country_codes=(${countryCodes})

    mkdir -p "$state_dir"

    ensure_chain() {
      "$iptables" -w -N "$chain" 2>/dev/null || true
      "$iptables" -w -C INPUT -j "$chain" 2>/dev/null || "$iptables" -w -I INPUT 1 -j "$chain"

      "$ip6tables" -w -N "$chain" 2>/dev/null || true
      "$ip6tables" -w -C INPUT -j "$chain" 2>/dev/null || "$ip6tables" -w -I INPUT 1 -j "$chain" || true
    }

    clear_rules() {
      ensure_chain
      "$iptables" -w -F "$chain" 2>/dev/null || true
      "$ip6tables" -w -F "$chain" 2>/dev/null || true
    }

    refresh_nl_zone() {
      if [ -s "$nl_zone_file" ] && ! "$find" "$nl_zone_file" -mmin +1440 | "$grep" -q .; then
        return 0
      fi

      tmp="$nl_zone_file.tmp"
      if "$curl" -fsSL --connect-timeout 5 --max-time 20 "$nl_zone_url" -o "$tmp"; then
        mv "$tmp" "$nl_zone_file"
      else
        rm -f "$tmp"
      fi
    }

    load_country_sets() {
      for country in "''${country_codes[@]}"; do
        case "$country" in
          nl)
            refresh_nl_zone
            # The bundled list ships in the Nix store, so opening break-glass
            # SSH never depends on a live download during an incident.
            zone_source="$nl_zone_file"
            if [ ! -s "$zone_source" ]; then
              zone_source="$nl_zone_bundled"
              echo "nl zone download unavailable; using bundled country list" >&2
            fi
            if [ -s "$zone_source" ]; then
              "$ipset" create "$country_set4" hash:net family inet -exist
              "$ipset" flush "$country_set4"
              while IFS= read -r cidr; do
                case "$cidr" in
                  ""|\#*) continue ;;
                esac
                "$ipset" add "$country_set4" "$cidr" -exist
              done < "$zone_source"
            fi
            ;;
          *)
            echo "unsupported break-glass country code: $country" >&2
            ;;
        esac
      done
    }

    open_public_ssh() {
      clear_rules
      load_country_sets

      for country in "''${country_codes[@]}"; do
        case "$country" in
          nl)
            if "$ipset" list "$country_set4" >/dev/null 2>&1; then
              "$iptables" -w -A "$chain" -p tcp --dport "$port" -m set --match-set "$country_set4" src -j ACCEPT
            fi
            ;;
        esac
      done

      for cidr in "''${allowed_cidrs[@]}"; do
        case "$cidr" in
          *:*)
            "$ip6tables" -w -A "$chain" -p tcp --dport "$port" -s "$cidr" -j ACCEPT || true
            ;;
          *)
            "$iptables" -w -A "$chain" -p tcp --dport "$port" -s "$cidr" -j ACCEPT
            ;;
        esac
      done

      touch "$open_state"
      echo "public SSH break-glass is open for configured SSH source ranges"
    }

    close_public_ssh() {
      clear_rules
      rm -f "$open_state" "$recovered_at_state"
      echo "public SSH break-glass is closed"
    }

    netbird_healthy() {
      "$systemctl" is-active --quiet netbird-join.service \
        && "$ip" -4 -o addr show dev "$iface" >/dev/null 2>&1
    }

    now="$("$date" +%s)"

    if ! netbird_healthy; then
      rm -f "$recovered_at_state"
      open_public_ssh
      exit 0
    fi

    if [ ! -e "$open_state" ]; then
      close_public_ssh
      exit 0
    fi

    if [ ! -s "$recovered_at_state" ]; then
      echo "$now" > "$recovered_at_state"
      open_public_ssh
      exit 0
    fi

    recovered_at="$(cat "$recovered_at_state")"
    if [ "$((now - recovered_at))" -ge "$close_after_seconds" ]; then
      close_public_ssh
    else
      open_public_ssh
    fi
  '';
in
{
  options.my.breakGlassSsh = {
    enable = lib.mkEnableOption "automatic public SSH break-glass access";

    netbirdInterface = lib.mkOption {
      type = lib.types.str;
      default = config.my.cloud.netbirdInterface or "wt0";
      description = "Netbird interface that normally carries SSH access.";
    };

    port = lib.mkOption {
      type = lib.types.port;
      default = 22;
      description = "SSH port to open temporarily on the public firewall.";
    };

    closeAfterSeconds = lib.mkOption {
      type = lib.types.ints.positive;
      default = 3600;
      description = "Seconds to keep public SSH open after Netbird has recovered.";
    };

    allowedCidrs = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Additional IPv4 or IPv6 CIDRs allowed to use public break-glass SSH.";
    };

    countryCodes = lib.mkOption {
      type = lib.types.listOf (lib.types.enum [ "nl" ]);
      default = [ "nl" ];
      description = "Country CIDR lists allowed to use public break-glass SSH.";
    };

    countryZoneUrls = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = {
        nl = "https://www.ipdeny.com/ipblocks/data/countries/nl.zone";
      };
      description = "IPv4 country-zone list URLs used to refresh break-glass SSH source filtering.";
    };

    countryZoneFiles = lib.mkOption {
      type = lib.types.attrsOf lib.types.path;
      default = {
        nl = ./data/nl.zone;
      };
      description = ''
        Pinned IPv4 country-zone list files bundled with the system. Used as
        the source of truth when the live zone download is unavailable, so
        break-glass SSH does not depend on an external service during the
        incident it exists for.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    networking.firewall.interfaces.${cfg.netbirdInterface}.allowedTCPPorts = [ cfg.port ];

    systemd.services.epistola-break-glass-ssh = {
      description = "Open public SSH temporarily when Netbird is unhealthy";
      after = [ "network-online.target" "firewall.service" ];
      wants = [ "network-online.target" ];
      path = [
        pkgs.coreutils
        pkgs.curl
        pkgs.findutils
        pkgs.gnugrep
        pkgs.iptables
        pkgs.ipset
        pkgs.iproute2
        pkgs.systemd
      ];
      serviceConfig = {
        Type = "oneshot";
        StateDirectory = "epistola-break-glass-ssh";
        ExecStart = watchdog;
      };
    };

    systemd.timers.epistola-break-glass-ssh = {
      description = "Watch Netbird health for public SSH break-glass access";
      wantedBy = [ "timers.target" ];
      timerConfig = {
        OnBootSec = "30s";
        OnUnitActiveSec = "1min";
        AccuracySec = "15s";
        Unit = "epistola-break-glass-ssh.service";
      };
    };
  };
}

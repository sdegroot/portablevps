# Temporarily opens public SSH from approved source ranges when mesh VPN access
# is unhealthy.
{ config, lib, pkgs, ... }:

let
  cfg = config.portablevps.breakGlassSsh;
  net = config.portablevps.network;
  chain = "PVPS_BGLASS_SSH";
  stateDir = "/var/lib/portablevps-break-glass-ssh";
  allowedCidrs = lib.escapeShellArgs cfg.allowedCidrs;
  countryCodes = lib.escapeShellArgs cfg.countryCodes;

  # Resolve the IPv4 zone-list URL for a country code: an explicit override in
  # countryZoneUrls wins, otherwise fall back to the ipdeny.com per-country path.
  zoneUrlFor = code: cfg.countryZoneUrls.${code}
    or "https://www.ipdeny.com/ipblocks/data/countries/${code}.zone";
  # A bundled (Nix-store) copy is optional. When present it makes opening
  # break-glass SSH independent of a live download during an incident; empty
  # string means no bundled fallback ships for that country.
  zoneBundledFor = code: toString (cfg.countryZoneFiles.${code} or "");

  # Render a bash associative-array body ([nl]=<url> [de]=<url> ...) from a
  # per-country function, over exactly the configured country codes.
  mkAssoc = f: lib.concatMapStringsSep " "
    (code: "[${code}]=${lib.escapeShellArg (f code)}") cfg.countryCodes;
  zoneUrlAssoc = mkAssoc zoneUrlFor;
  zoneBundledAssoc = mkAssoc zoneBundledFor;

  watchdog = pkgs.writeShellScript "portablevps-break-glass-ssh" ''
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
    iface=${lib.escapeShellArg net.interface}
    join_unit=${lib.escapeShellArg net.joinUnit}
    port=${toString cfg.port}
    close_after_seconds=${toString cfg.closeAfterSeconds}
    open_after_seconds=${toString cfg.openAfterSeconds}
    state_dir=${lib.escapeShellArg stateDir}
    open_state="$state_dir/open"
    recovered_at_state="$state_dir/recovered-at"
    unhealthy_since_state="$state_dir/unhealthy-since"
    allowed_cidrs=(${allowedCidrs})
    country_codes=(${countryCodes})
    declare -A zone_urls=( ${zoneUrlAssoc} )
    declare -A zone_bundled=( ${zoneBundledAssoc} )

    mkdir -p "$state_dir"

    # ipset holding a country's IPv4 ranges. Keep the name short (ipset caps at
    # 31 chars): PVPS_CC4_<code>.
    set4_name() { echo "PVPS_CC4_$1"; }

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

    refresh_zone() {
      # $1 country code, $2 url, $3 destination file. Refresh at most daily.
      country="$1"; url="$2"; dest="$3"
      if [ -s "$dest" ] && ! "$find" "$dest" -mmin +1440 | "$grep" -q .; then
        return 0
      fi

      tmp="$dest.tmp"
      if "$curl" -fsSL --connect-timeout 5 --max-time 20 "$url" -o "$tmp"; then
        mv "$tmp" "$dest"
      else
        rm -f "$tmp"
      fi
    }

    load_country_sets() {
      for country in "''${country_codes[@]}"; do
        url="''${zone_urls[$country]:-}"
        bundled="''${zone_bundled[$country]:-}"
        zone_file="$state_dir/$country.zone"

        [ -n "$url" ] && refresh_zone "$country" "$url" "$zone_file"

        # The bundled list (if any) ships in the Nix store, so opening
        # break-glass SSH never depends on a live download during an incident.
        zone_source="$zone_file"
        if [ ! -s "$zone_source" ]; then
          if [ -n "$bundled" ] && [ -s "$bundled" ]; then
            zone_source="$bundled"
            echo "$country zone download unavailable; using bundled country list" >&2
          else
            echo "no zone list for country $country (download failed, no bundled copy); skipping" >&2
            continue
          fi
        fi

        set4="$(set4_name "$country")"
        "$ipset" create "$set4" hash:net family inet -exist
        "$ipset" flush "$set4"
        while IFS= read -r cidr; do
          case "$cidr" in
            ""|\#*) continue ;;
          esac
          "$ipset" add "$set4" "$cidr" -exist
        done < "$zone_source"
      done
    }

    open_public_ssh() {
      clear_rules
      load_country_sets

      for country in "''${country_codes[@]}"; do
        set4="$(set4_name "$country")"
        if "$ipset" list "$set4" >/dev/null 2>&1; then
          "$iptables" -w -A "$chain" -p tcp --dport "$port" -m set --match-set "$set4" src -j ACCEPT
        fi
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
      rm -f "$open_state" "$recovered_at_state" "$unhealthy_since_state"
      echo "public SSH break-glass is closed"
    }

    vpn_healthy() {
      "$systemctl" is-active --quiet "$join_unit" \
        && "$ip" -4 -o addr show dev "$iface" >/dev/null 2>&1
    }

    now="$("$date" +%s)"

    if ! vpn_healthy; then
      rm -f "$recovered_at_state"
      # Already open from a prior sustained outage: keep it open (refresh rules).
      if [ -e "$open_state" ]; then
        open_public_ssh
        exit 0
      fi
      # Not open yet: debounce. Only open after the mesh has been unhealthy for
      # open_after_seconds, so a brief flap — or an attacker briefly disturbing
      # the mesh to force public SSH exposure — does not instantly open the port.
      if [ ! -s "$unhealthy_since_state" ]; then
        echo "$now" > "$unhealthy_since_state"
      fi
      unhealthy_since="$(cat "$unhealthy_since_state" 2>/dev/null || echo "$now")"
      if [ "$((now - unhealthy_since))" -ge "$open_after_seconds" ]; then
        open_public_ssh
      else
        echo "mesh unhealthy for $((now - unhealthy_since))s; break-glass opens after ''${open_after_seconds}s"
      fi
      exit 0
    fi

    # Mesh healthy: clear the open-debounce timer.
    rm -f "$unhealthy_since_state"

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
  imports = [ ./network.nix ];

  options.portablevps.breakGlassSsh = {
    enable = lib.mkEnableOption "automatic public SSH break-glass access";

    port = lib.mkOption {
      type = lib.types.port;
      default = 22;
      description = "SSH port to open temporarily on the public firewall.";
    };

    closeAfterSeconds = lib.mkOption {
      type = lib.types.ints.positive;
      default = 3600;
      description = "Seconds to keep public SSH open after the mesh VPN has recovered.";
    };

    openAfterSeconds = lib.mkOption {
      type = lib.types.ints.unsigned;
      default = 180;
      description = ''
        Seconds the mesh must be continuously unhealthy before public SSH is
        opened. Debounces brief flaps and blunts an attacker who briefly
        disturbs the mesh to force public SSH exposure. Set to 0 to open
        immediately (the old behaviour).
      '';
    };

    allowedCidrs = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Additional IPv4 or IPv6 CIDRs allowed to use public break-glass SSH.";
    };

    countryCodes = lib.mkOption {
      type = lib.types.listOf (lib.types.strMatching "[a-z]{2}");
      default = [ "nl" ];
      example = [ "nl" "de" ];
      description = ''
        ISO 3166-1 alpha-2 country codes whose IPv4 ranges may use public
        break-glass SSH. Each code's ranges are fetched from its
        countryZoneUrls entry (defaulting to the ipdeny.com per-country list)
        and, when a countryZoneFiles entry is bundled, that store copy is used
        as an offline fallback during an incident. Only "nl" ships a bundled
        list by default; add your own via countryZoneFiles for offline
        resilience in other countries.
      '';
    };

    countryZoneUrls = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = {
        nl = "https://www.ipdeny.com/ipblocks/data/countries/nl.zone";
      };
      description = ''
        IPv4 country-zone list URLs used to refresh break-glass SSH source
        filtering, keyed by country code. A code without an entry falls back to
        the ipdeny.com per-country path.
      '';
    };

    countryZoneFiles = lib.mkOption {
      type = lib.types.attrsOf lib.types.path;
      default = {
        nl = ./data/nl.zone;
      };
      description = ''
        Pinned IPv4 country-zone list files bundled with the system, keyed by
        country code. Used as the source of truth when the live zone download
        is unavailable, so break-glass SSH does not depend on an external
        service during the incident it exists for. A country without a bundled
        file relies on the live download only.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    networking.firewall.interfaces.${net.interface}.allowedTCPPorts = [ cfg.port ];

    systemd.services.portablevps-break-glass-ssh = {
      description = "Open public SSH temporarily when mesh VPN is unhealthy";
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
        StateDirectory = "portablevps-break-glass-ssh";
        ExecStart = watchdog;
      };
    };

    systemd.timers.portablevps-break-glass-ssh = {
      description = "Watch mesh VPN health for public SSH break-glass access";
      wantedBy = [ "timers.target" ];
      timerConfig = {
        OnBootSec = "30s";
        OnUnitActiveSec = "1min";
        AccuracySec = "15s";
        Unit = "portablevps-break-glass-ssh.service";
      };
    };
  };
}

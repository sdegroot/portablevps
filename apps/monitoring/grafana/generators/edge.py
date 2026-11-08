#!/usr/bin/env python3
"""Generates role-edge.json, the "Role — Edge" dashboard (uid pvps-edge).

Edit the panels here, then from this directory:
    python3 edge.py > ../dashboards/role-edge.json
(commit both; see dash.py for regenerating every role dashboard at once).

An edge is a Traefik that forwards TCP by SNI without terminating TLS, so it has
no request metrics. Sources:
  - proxy_edge_tcp_connections_5m: the rolling 5-minute connection count that
    vmalert-logs records from the log (rules-vlogs/proxy-edge.yml). It is also
    the role metric: only hosts that log connections appear in the pickers.
  - traefik_open_connections: Traefik's own gauge, with
    portablevps.proxy.metrics.enable on the edge.
  - Everything else: the edge Traefik's DEBUG line per forwarded connection
    ("Handling TCP connection", with remoteAddr and the backend address) and
    its error lines, in VictoriaLogs.
"""
from dash import (SEL, VL, Dashboard, color_override, dashed, emit, target)

d = Dashboard()

# LogsQL filters for the selected service and hosts. ${host:regex} turns the
# multi-select (or All) into one regular expression.
LOGS = 'service.name:="$service" host.name:~"${host:regex}" _SYSTEMD_UNIT:traefik.service'
CONN = f'{LOGS} "Handling TCP connection"'
REJ = f'{LOGS} "Connection rejected" "InFlightConnTCP"'
ERR = f'{LOGS} "Error while handling TCP connection"'
SUP = ('service.name:="$service" host.name:~"${host:regex}" SYSLOG_IDENTIFIER:systemd-journald '
       '"Suppressed" "messages from traefik.service"')
CLIENT = 'extract_regexp "\\"remoteAddr\\":\\"(?P<client_ip>[^\\"]+):[0-9]+\\"" from _msg'
TARGET = 'extract_regexp "\\"address\\":\\"(?P<target>[^\\"]+)\\"" from _msg'
REJECTED_IP = 'extract_regexp "max number of connections reached for (?P<client_ip>[^\\"]+)" from _msg'
BANS = ('service.name:="$service" host.name:~"${host:regex}" SYSLOG_IDENTIFIER:~"fail2ban" '
        '"[portablevps-proxy-"')
# Instant stats panels count over the dashboard's time range.
RANGE = "_time:$__range"

CONNS = f"sum(proxy_edge_tcp_connections_5m{{{SEL}}})"
CONNS_7D = f"sum(avg_over_time(proxy_edge_tcp_connections_5m{{{SEL}}}[7d]))"
OPEN = f'traefik_open_connections{{{SEL}, entrypoint!="metrics"}}'


def steps(orange, red):
    return [{"color": "green", "value": None}, {"color": "orange", "value": orange}, {"color": "red", "value": red}]


def vl_stat(title, expr, desc, thresholds=None):
    """A stat over the selected range, counted in VictoriaLogs."""
    p = d.stat(title, "", "short", desc, thresholds, decimals=0, graph=False)
    p["datasource"] = VL
    p["targets"] = [{"refId": "A", "datasource": VL, "expr": f"{RANGE} {expr}", "queryType": "stats"}]
    return p


def vl_top(title, expr, legend, desc):
    """Horizontal bars, one per label value, from an instant VictoriaLogs stats query."""
    return {
        "id": d.nid(), "type": "bargauge", "title": title, "description": desc, "datasource": VL,
        "targets": [{"refId": "A", "datasource": VL, "expr": f"{RANGE} {expr}", "queryType": "stats",
                     "legendFormat": legend}],
        "fieldConfig": {"defaults": {"unit": "short", "min": 0, "color": {"mode": "continuous-BlYlRd"}},
                        "overrides": []},
        "options": {"orientation": "horizontal", "displayMode": "gradient", "showUnfilled": True,
                    "valueMode": "color", "namePlacement": "left", "sizing": "auto",
                    "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False}},
    }


def vl_lines(title, targets, desc, overrides=None):
    """Several VictoriaLogs statsRange series as bars (d.vl_range takes one, stacked)."""
    p = d.ts(title, [{"refId": chr(65 + i), "datasource": VL, "expr": e, "queryType": "statsRange",
                      "legendFormat": legend} for i, (e, legend) in enumerate(targets)],
             "short", desc, overrides=overrides, draw="bars")
    p["datasource"] = VL
    return p


# ---------------------------------------------------------------- overview
d.row("Overview")
d.place(d.stat("Connections, last 5m", CONNS, "short",
               "Connections forwarded in the last 5 minutes, recorded by vmalert-logs from the connection log.",
               steps(400, 600), decimals=0), 0, 4, 4)
d.place(d.stat("vs 7-day average", f"{CONNS} / {CONNS_7D}", "x",
               "The last 5 minutes divided by the 7-day average of the same count. ProxyEdgeConnectionSurge fires above 10x when the count is also over 600.",
               steps(4, 10), decimals=1), 4, 4, 4)
d.place(d.stat("Open connections", f"sum({OPEN})", "short",
               "TCP connections held open through the edge right now. ProxyOpenConnectionsHigh fires above 1000 for 5 minutes.",
               steps(500, 1000), decimals=0), 8, 4, 4)
d.place(vl_stat("Unique clients", f"{CONN} | {CLIENT} | stats count_uniq(client_ip) clients",
                "Distinct client addresses over the selected range."), 12, 3, 4)
d.place(vl_stat("Rejected by per-IP limit", f"{REJ} | stats count() rejected",
                "Connections refused over the selected range because their address already held tcp.maxConnectionsPerIP open.",
                steps(1, 100)), 15, 3, 4)
d.place(vl_stat("Forwarding errors", f"{ERR} | stats count() errors",
                "Accepted connections that broke while being forwarded, over the selected range. A few an hour is clients going away."),
        18, 3, 4)
d.place(vl_stat("Log suppressions", f"{SUP} | stats count() suppressions",
                "Times journald dropped Traefik lines over the selected range. Above zero, every log-based count here is too low.",
                steps(1, 1)), 21, 3, 4)
d.advance(4)

# ---------------------------------------------------------------- traffic
d.row("Traffic")
d.place(d.ts("Connections per 5 minutes", [
    target(CONNS, "last 5m", "A"),
    target(CONNS_7D, "7-day average", "B"),
], "short", "Forwarded connections per rolling 5 minutes, against the 7-day average.",
    overrides=[dashed("7-day average", "text")]), 0, 12, 8)
d.place(d.vl_range("Connections by target", f"{CONN} | {TARGET} | stats by (target) count() connections",
                   "{{target}}", "short", "Forwarded connections per backend address, which is the SNI route they matched."),
        12, 12, 8)
d.advance(8)
d.place(d.ts("Open connections by entrypoint", [
    target(f"sum by (entrypoint) ({OPEN})", "{{entrypoint}}"),
], "short", "Concurrent connections held open, per Traefik entrypoint."), 0, 12, 8)
d.place(vl_lines("Errors, rejections and suppressions", [
    (f"{ERR} | stats count() errors", "forwarding errors"),
    (f"{REJ} | stats count() rejected", "rejected by per-IP limit"),
    (f"{SUP} | stats count() suppressions", "journald suppressions"),
], "Forwarding errors, per-IP limit rejections and journald suppressions over time.",
    overrides=[color_override("forwarding errors", "orange"), color_override("rejected by per-IP limit", "red"),
               color_override("journald suppressions", "purple")]), 12, 12, 8)
d.advance(8)

# ---------------------------------------------------------------- sources
d.row("Sources")
d.place(vl_top("Top clients",
               f"{CONN} | {CLIENT} | stats by (client_ip) count() connections | sort by (connections desc) | limit 15",
               "{{client_ip}}",
               "Client addresses with the most connections over the selected range. ProxyEdgeSingleSourceFlood fires when one opens over 1000 in 5 minutes."),
        0, 12, 10)
d.place(vl_top("Top clients rejected by the per-IP limit",
               f"{REJ} | {REJECTED_IP} | stats by (client_ip) count() rejected | sort by (rejected desc) | limit 15",
               "{{client_ip}}",
               "Addresses refused because they already held the maximum number of open connections, over the selected range."),
        12, 12, 10)
d.advance(10)

# ---------------------------------------------------------------- logs
d.row("Logs")
d.place(d.logs("Blocked addresses", BANS,
               "Bans and unbans by the proxy's automatic blocking (portablevps.proxy.tcp.autoBlock): fail2ban jails portablevps-proxy-limit and portablevps-proxy-flood."),
        0, 24, 6)
d.advance(6)
d.place(d.logs("Errors and rejections", f'{LOGS} "\\"level\\":\\"error\\""',
               "Every Traefik line at error level on the edge: forwarding errors, per-IP rejections, configuration problems."),
        0, 24, 9)
d.advance(9)
d.place(d.logs("Connections", CONN, "One line per forwarded connection: the client address and the backend it went to."),
        0, 24, 10)
d.advance(10)

emit(d, "pvps-edge", "Role — Edge",
     "A TCP-passthrough edge proxy: connections forwarded, who opens them, where they go, and what the per-IP limit refused. Alerts: rules/proxy-edge.yml and rules-vlogs/proxy-edge.yml. Pick the service at the top; links can pass ?var-service=<name>.",
     ["traefik", "edge", "security"], "proxy_edge_tcp_connections_5m")

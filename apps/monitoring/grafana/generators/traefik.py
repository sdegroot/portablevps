#!/usr/bin/env python3
"""Generates role-traefik.json, the "Role — Traefik" dashboard (uid pvps-traefik).

Edit the panels here, then from this directory:
    python3 traefik.py > ../dashboards/role-traefik.json
(commit both; see dash.py for regenerating every role dashboard at once).

Source: each host's collector scrapes its Traefik's Prometheus endpoint on
loopback :8082. Traefik v3 names: router/service/entrypoint request counters
and duration histograms, traefik_open_connections, traefik_tls_certs_not_after,
traefik_service_server_up (only for services with a health check) and the
config reload counter and last-success timestamp (v3 exports no failure counter).
"""
from dash import (SEL, Dashboard, color_override, column_override, emit, regex_color, target)

d = Dashboard()

R = "traefik_router_requests_total"
RB = "traefik_router_request_duration_seconds_bucket"
S = "traefik_service_requests_total"
SB = "traefik_service_request_duration_seconds_bucket"
E = "traefik_entrypoint_requests_total"
# The metrics entrypoint is the collector's own scrape; it is not traffic.
EP = f'{SEL}, entrypoint!="metrics"'

CLASS_COLORS = [color_override("2xx", "green"), color_override("3xx", "blue"),
                color_override("4xx", "orange"), color_override("5xx", "red"),
                color_override("0xx", "purple")]


def q(p, by, metric_bucket):
    return f'histogram_quantile({p}, sum by ({by}) (rate({metric_bucket}{{{SEL}}}[$__rate_interval])))'


def ratio(metric, by, codes, window):
    """Share of `by`'s requests with `codes`; 0 (not empty) for one with none."""
    total = f'sum by ({by}) (rate({metric}{{{SEL}}}[{window}]))'
    return f'(sum by ({by}) (rate({metric}{{{SEL}, code=~"{codes}"}}[{window}])) or {total} * 0) / {total}'


# ---------------------------------------------------------------- overview
d.row("Overview")
d.place(d.stat("Availability",
               f'1 - (sum(increase({R}{{{SEL}, code=~"5.."}}[$__range])) or vector(0)) / sum(increase({R}{{{SEL}}}[$__range]))',
               "percentunit", "Share of requests through this service's routers that did NOT end in a 5xx, over the selected time range.",
               [{"color": "red", "value": None}, {"color": "orange", "value": 0.99}, {"color": "green", "value": 0.999}],
               decimals=2), 0, 4, 4)
d.place(d.stat("Requests / s", f'sum(rate({R}{{{SEL}}}[$__rate_interval]))', "reqps",
               "Requests per second through every router of this service's proxy."), 4, 4, 4)
d.place(d.stat("5xx (range)", f'sum(increase({R}{{{SEL}, code=~"5.."}}[$__range])) or vector(0)', "short",
               "Server errors answered over the selected range.",
               [{"color": "green", "value": None}, {"color": "orange", "value": 1}, {"color": "red", "value": 25}],
               decimals=0), 8, 4, 4)
d.place(d.stat("p95 latency", q(0.95, "le", RB), "s",
               "95th percentile of request duration at the proxy, upstream included. Traefik's default buckets are coarse (0.1, 0.3, 1.2, 5 s), so read it as a band, not a measurement.",
               [{"color": "green", "value": None}, {"color": "orange", "value": 1}, {"color": "red", "value": 3}]), 12, 4, 4)
d.place(d.stat("TLS cert expires in", f'min((max by (cn) (traefik_tls_certs_not_after{{{SEL}}}) - time()) / 86400)', "d",
               "Days until the soonest-expiring certificate Traefik serves (the newest one per CN). ACME renews at 30 days left, so under ~25 means renewal is failing.",
               [{"color": "red", "value": None}, {"color": "orange", "value": 14}, {"color": "green", "value": 25}],
               decimals=0, graph=False), 16, 4, 4)
d.place(d.stat("Services down", f'count(sum by (service) (traefik_service_server_up{{{SEL}}}) == 0) or (count(traefik_service_server_up{{{SEL}}}) * 0)',
               "short", "Backend services with no upstream server Traefik's health check holds up. One idle colour of a blue-green pair is not counted: only a service with none up is. Services without a health check report nothing, so the panel shows no data where none has one.",
               [{"color": "green", "value": None}, {"color": "red", "value": 1}], decimals=0, graph=False), 20, 4, 4)
d.advance(4)

# ---------------------------------------------------------------- traffic
d.row("Traffic")
d.place(d.ts("Requests by status class", [target(
    f'sum by (class) (label_replace(rate({R}{{{SEL}}}[$__rate_interval]), "class", "${{1}}xx", "code", "([0-9]).*"))',
    "{{class}}")], "reqps",
    "Requests per second by status class. 0xx is Traefik's code 0: the client went away before a response.",
    stack=True, overrides=CLASS_COLORS), 0, 12, 8)
d.place(d.ts("Latency (proxy)", [
    target(q(0.50, "le", RB), "p50", "A"),
    target(q(0.95, "le", RB), "p95", "B"),
    target(q(0.99, "le", RB), "p99", "C"),
], "s", "Request duration percentiles at Traefik, TLS termination to the upstream's last byte, interpolated within coarse buckets."), 12, 12, 8)
d.advance(8)
d.place(d.ts("Errors by status code", [target(
    f'sum by (code) (rate({R}{{{SEL}, code=~"[45]..|0"}}[$__rate_interval])) > 0', "{{code}}")], "reqps",
    "4xx, 5xx and aborted (0) requests per code. 401/404 storms are usually clients and crawlers; 5xx are the upstream's (or Traefik's 502/504 when it cannot reach it).",
    stack=True, overrides=[regex_color("5..", "red"), regex_color("4..", "orange")]), 0, 12, 8)
d.place(d.ts("Bandwidth", [
    target(f'sum(rate(traefik_router_responses_bytes_total{{{SEL}}}[$__rate_interval]))', "out (responses)", "A"),
    target(f'sum(rate(traefik_router_requests_bytes_total{{{SEL}}}[$__rate_interval]))', "in (requests)", "B"),
], "Bps", "Bytes served to and received from clients through the routers."), 12, 6, 8)
d.place(d.ts("Requests by method & protocol", [target(
    f'sum by (method, protocol) (rate({R}{{{SEL}}}[$__rate_interval]))', "{{method}} {{protocol}}")],
    "reqps", "Request mix by HTTP method and protocol (http, websocket, sse).", stack=True), 18, 6, 8)
d.advance(8)

# ---------------------------------------------------------------- routers
d.row("Routers")
d.place(d.table("Routers (selected range)", [
    target(f'sum by (router) (rate({R}{{{SEL}}}[$__range]))', "", "A", instant=True, fmt="table"),
    target(ratio(R, "router", "5..", "$__range"), "", "B", instant=True, fmt="table"),
    target(ratio(R, "router", "4..", "$__range"), "", "C", instant=True, fmt="table"),
    target(f'histogram_quantile(0.95, sum by (router, le) (rate({RB}{{{SEL}}}[$__range])))', "", "D", instant=True, fmt="table"),
], "Every router over the selected range: mean request rate, share of 5xx and 4xx, and p95 latency.",
    [("router", "Router"), ("Value #A", "Req/s"), ("Value #B", "5xx ratio"), ("Value #C", "4xx ratio"),
     ("Value #D", "p95")], sort_by="Req/s",
    overrides=[column_override("Req/s", "reqps", 2),
               column_override("5xx ratio", "percentunit", 2,
                               [{"color": "green", "value": None}, {"color": "orange", "value": 0.001},
                                {"color": "red", "value": 0.01}], "color-text"),
               column_override("4xx ratio", "percentunit", 2),
               column_override("p95", "s", 2,
                               [{"color": "green", "value": None}, {"color": "orange", "value": 1},
                                {"color": "red", "value": 3}], "color-text")]), 0, 12, 9)
d.place(d.ts("Requests per router", [target(
    f'sum by (router) (rate({R}{{{SEL}}}[$__rate_interval]))', "{{router}}")], "reqps",
    "Request rate per router.", legend_calcs=["mean", "max"]), 12, 12, 9)
d.advance(9)
d.place(d.ts("5xx ratio per router", [target(
    f'{ratio(R, "router", "5..", "$__rate_interval")} > 0', "{{router}}")], "percentunit",
    "Share of each router's requests that ended in a 5xx; routers without errors are left out."), 0, 12, 8)
d.place(d.ts("p95 latency per router", [target(q(0.95, "router, le", RB), "{{router}}")], "s",
             "95th percentile request duration per router."), 12, 12, 8)
d.advance(8)

# ---------------------------------------------------------------- services
d.row("Services (upstreams)")
d.place(d.ts("Requests per service", [target(
    f'sum by (service) (rate({S}{{{SEL}}}[$__rate_interval]))', "{{service}}")], "reqps",
    "Requests Traefik forwarded to each backend service (Traefik's own service, not service_name).",
    legend_calcs=["mean", "max"]), 0, 8, 8)
d.place(d.ts("5xx ratio per service", [target(
    f'{ratio(S, "service", "5..", "$__rate_interval")} > 0', "{{service}}")], "percentunit",
    "Share of each backend's responses that were 5xx."), 8, 8, 8)
d.place(d.ts("p95 latency per service", [target(q(0.95, "service, le", SB), "{{service}}")], "s",
             "95th percentile of the time the backend took, as Traefik measured it."), 16, 8, 8)
d.advance(8)
d.place(d.table("Backend servers", [
    target(f'max by (service, url) (traefik_service_server_up{{{SEL}}})', "", "A", instant=True, fmt="table"),
], "Each upstream server and whether Traefik's health check holds it up. Only services with a health check are listed (none means no health checks configured).",
    [("service", "Service"), ("url", "Server"), ("Value #A", "State")],
    overrides=[{"matcher": {"id": "byName", "options": "State"}, "properties": [
        {"id": "mappings", "value": [{"type": "value", "options": {
            "1": {"text": "UP", "color": "green"}, "0": {"text": "DOWN", "color": "red"}}}]},
        {"id": "custom.cellOptions", "value": {"type": "color-background"}}]}]), 0, 12, 7)
d.place(d.ts("Backend servers up", [target(
    f'sum by (service) (traefik_service_server_up{{{SEL}}})', "{{service}}")], "short",
    "Healthy upstream servers per service over time. A blue-green service shows 1, briefly 2 during a flip; 0 is an outage."),
    12, 12, 7)
d.advance(7)

# ---------------------------------------------------------------- entrypoints
d.row("Entrypoints & connections")
d.place(d.ts("Requests per entrypoint", [target(
    f'sum by (entrypoint) (rate({E}{{{EP}}}[$__rate_interval]))', "{{entrypoint}}")], "reqps",
    "HTTP requests per entrypoint, including those no router matched (which never show in the router panels)."),
    0, 8, 8)
d.place(d.ts("Open connections", [target(
    f'sum by (entrypoint, protocol) (traefik_open_connections{{{EP}}})', "{{entrypoint}} ({{protocol}})")], "short",
    "Connections currently open per entrypoint, TCP passthrough entrypoints (mail, …) included. The metrics entrypoint is left out: that is the collector itself."),
    8, 8, 8)
d.place(d.ts("TLS versions", [target(
    f'sum by (tls_version) (rate(traefik_entrypoint_requests_tls_total{{{EP}}}[$__rate_interval]))', "TLS {{tls_version}}")],
    "reqps", "TLS requests by protocol version.", stack=True), 16, 8, 8)
d.advance(8)

# ---------------------------------------------------------------- tls & config
d.row("Certificates & configuration")
d.place(d.table("Certificates", [
    target(f'(max by (cn) (traefik_tls_certs_not_after{{{SEL}}}) - time()) / 86400', "", "A", instant=True, fmt="table"),
], "Days until the newest certificate for each CN expires, soonest first. Traefik also reports certificates still in its store for names it no longer serves.",
    [("cn", "Common name"), ("Value #A", "Days left")],
    overrides=[column_override("Days left", "d", 0,
                               [{"color": "red", "value": None}, {"color": "orange", "value": 14},
                                {"color": "green", "value": 25}], "color-background")]), 0, 12, 8)
d.panels[-1]["options"]["sortBy"] = [{"displayName": "Days left", "desc": False}]
d.place(d.ts("Configuration reloads", [
    target(f'sum by (host_name) (increase(traefik_config_reloads_total{{{SEL}}}[$__rate_interval]))', "{{host_name}}"),
], "short", "Dynamic configuration reloads per host (a deploy, a certificate renewal, a file-provider change). Traefik v3 exports no failed-reload counter; a failed reload shows as the 'since last successful reload' age not resetting.",
    draw="bars"), 12, 6, 8)
d.place(d.stat("Since last successful reload", f'time() - max(traefik_config_last_reload_success{{{SEL}}})', "s",
               "Time since Traefik last applied a configuration without error. Long is normal on a quiet host; it matters when a change was just deployed and this did not reset.",
               graph=False), 18, 6, 8)
d.advance(8)

emit(d, "pvps-traefik", "Role — Traefik",
     "Any service's Traefik reverse proxy: availability, traffic, latency, routers and backend services, entrypoints and open connections, certificate expiry and configuration reloads. Pick the service (and optionally hosts) at the top; links can pass ?var-service=<name>.",
     ["traefik"], "traefik_config_reloads_total")

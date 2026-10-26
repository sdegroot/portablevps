#!/usr/bin/env python3
"""Generates role-jvm.json, the "Role — JVM / Spring Boot" dashboard (uid pvps-jvm).

Edit the panels here, then from this directory:
    python3 jvm.py > ../dashboards/role-jvm.json
(commit both; see dash.py for regenerating every role dashboard at once).

Source: a Spring Boot (Micrometer) application's Prometheus endpoint, read by
the host's collector through portablevps.telemetry.prometheusTargets. Names
are Micrometer's Prometheus names as VictoriaMetrics stores them; a few differ
from what Micrometer's docs suggest: process CPU time arrives as
process_cpu_time_ns_total, loaded classes as jvm_classes_loaded_classes, and
Hikari's acquire/usage/creation timers carry only _count/_sum/_max (no
histogram buckets unless the application publishes them). HTTP server
request panels read http_server_requests_seconds_*: _count/_sum/_max always,
and p95/p99 only when the application publishes histogram buckets
(management.metrics.distribution.percentiles-histogram.http.server.requests).
"""
from dash import (SEL, Dashboard, color_override, column_override, dashed, emit, regex_color, right_axis, target)

d = Dashboard()

HTTP = "http_server_requests_seconds"
HK = "hikaricp_connections"
LEVEL_COLORS = [color_override("error", "red"), color_override("warn", "orange"),
                color_override("info", "green"), color_override("debug", "blue"),
                color_override("trace", "purple")]


def avg_of(timer, by="", window="$__rate_interval", sel=SEL):
    """Mean of a Micrometer timer over the window: rate(sum) / rate(count)."""
    b = f" by ({by})" if by else ""
    return (f'sum{b} (rate({timer}_sum{{{sel}}}[{window}])) / '
            f'sum{b} (rate({timer}_count{{{sel}}}[{window}]))')


# ---------------------------------------------------------------- overview
d.row("Overview")
d.place(d.stat("Uptime", f'min(process_uptime_seconds{{{SEL}}})', "s",
               "Time since the JVM started (the youngest instance when several hosts are selected). A reset is a restart.",
               graph=False), 0, 4, 4)
d.place(d.stat("Heap used", f'sum(jvm_memory_used_bytes{{{SEL}, area="heap"}}) / sum(jvm_memory_max_bytes{{{SEL}, area="heap"}} > 0)',
               "percentunit", "Heap in use as a share of the maximum heap. G1 runs high between collections; what matters is the level after GC (Memory row).",
               [{"color": "green", "value": None}, {"color": "orange", "value": 0.8}, {"color": "red", "value": 0.9}],
               decimals=0), 4, 4, 4)
d.place(d.stat("Process CPU", f'max(process_cpu_usage{{{SEL}}})', "percentunit",
               "The JVM's CPU use as a share of all the machine's cores.",
               [{"color": "green", "value": None}, {"color": "orange", "value": 0.7}, {"color": "red", "value": 0.9}],
               decimals=1), 8, 4, 4)
d.place(d.stat("GC pause share", f'max(sum by (host_name) (rate(jvm_gc_pause_seconds_sum{{{SEL}}}[$__rate_interval])))',
               "percentunit", "Share of wall time the application was paused for garbage collection. Above a few percent the heap is too small or something allocates hard.",
               [{"color": "green", "value": None}, {"color": "orange", "value": 0.02}, {"color": "red", "value": 0.1}],
               decimals=2), 12, 4, 4)
d.place(d.stat("DB pool in use", f'sum({HK}_active{{{SEL}}}) / sum({HK}_max{{{SEL}}})', "percentunit",
               "Hikari connections checked out as a share of the pools' maximum. Pinned at 100% means requests queue for a connection.",
               [{"color": "green", "value": None}, {"color": "orange", "value": 0.8}, {"color": "red", "value": 0.95}],
               decimals=0), 16, 4, 4)
d.place(d.stat("Errors logged (range)", f'sum(increase(logback_events_total{{{SEL}, level="error"}}[$__range]))', "short",
               "ERROR lines the application logged over the selected range (Logback).",
               [{"color": "green", "value": None}, {"color": "orange", "value": 1}, {"color": "red", "value": 50}],
               decimals=0), 20, 4, 4)
d.advance(4)

# ---------------------------------------------------------------- memory
d.row("Memory")
d.place(d.ts("Heap by pool", [
    target(f'sum by (id) (jvm_memory_used_bytes{{{SEL}, area="heap"}})', "{{id}}", "A"),
    target(f'sum(jvm_memory_max_bytes{{{SEL}, area="heap"}} > 0)', "max heap", "B"),
], "bytes", "Heap in use per pool (stacked) against the maximum heap.", stack=True,
    overrides=[dashed("max heap"), {"matcher": {"id": "byName", "options": "max heap"},
                                     "properties": [{"id": "custom.stacking", "value": {"mode": "none"}}]}]), 0, 8, 8)
d.place(d.ts("Non-heap by pool", [target(
    f'sum by (id) (jvm_memory_used_bytes{{{SEL}, area="nonheap"}})', "{{id}}")], "bytes",
    "Metaspace, compressed class space and the code cache. Metaspace that only grows points at a class-loader leak.",
    stack=True), 8, 8, 8)
d.place(d.ts("Heap after GC", [
    target(f'max(jvm_memory_usage_after_gc{{{SEL}, area="heap"}})', "long-lived pool after GC", "A"),
    target(f'sum(jvm_gc_live_data_size_bytes{{{SEL}}})', "live data", "B"),
    target(f'sum(jvm_gc_max_data_size_bytes{{{SEL}}})', "max old gen", "C"),
], "percentunit", "What survives a collection: the trend that matters for heap sizing. A floor that keeps rising is a leak.",
    overrides=[right_axis("live data", "bytes"), right_axis("max old gen", "bytes"), dashed("max old gen")]), 16, 8, 8)
d.advance(8)
d.place(d.ts("Allocation & promotion", [
    target(f'sum(rate(jvm_gc_memory_allocated_bytes_total{{{SEL}}}[$__rate_interval]))', "allocated", "A"),
    target(f'sum(rate(jvm_gc_memory_promoted_bytes_total{{{SEL}}}[$__rate_interval]))', "promoted to old gen", "B"),
], "Bps", "Rate of new allocation in the young generation and of objects promoted to the old generation."), 0, 8, 8)
d.place(d.ts("Direct & mapped buffers", [
    target(f'sum by (id) (jvm_buffer_memory_used_bytes{{{SEL}}})', "{{id}} used", "A"),
    target(f'sum by (id) (jvm_buffer_total_capacity_bytes{{{SEL}}})', "{{id}} capacity", "B"),
], "bytes", "Off-heap NIO buffers (Netty, file channels). They count against the container's memory, not the heap."), 8, 8, 8)
d.place(d.ts("Container memory", [
    target(f'sum by (container_name) (container_memory_usage_total_bytes{{{SEL}}})', "{{container_name}}", "A"),
], "bytes", "Memory of every container of this service (podman stats), for comparison with heap + non-heap + buffers. Empty when the application does not run in a container.",
    ), 16, 8, 8)
d.advance(8)

# ---------------------------------------------------------------- gc
d.row("Garbage collection")
d.place(d.ts("GC pauses / s", [target(
    f'sum by (gc, action) (rate(jvm_gc_pause_seconds_count{{{SEL}}}[$__rate_interval]))', "{{gc}} ({{action}})")],
    "ops", "Stop-the-world pauses per second by collector and kind."), 0, 8, 8)
d.place(d.ts("GC pause time share", [target(
    f'sum by (gc) (rate(jvm_gc_pause_seconds_sum{{{SEL}}}[$__rate_interval]))', "{{gc}}")],
    "percentunit", "Share of wall time spent paused, per collector.", stack=True), 8, 8, 8)
d.place(d.ts("Longest pause", [
    target(f'max by (gc, cause) (jvm_gc_pause_seconds_max{{{SEL}}})', "{{gc}}: {{cause}}", "A"),
    target(f'max by (gc) (rate(jvm_gc_concurrent_phase_time_seconds_sum{{{SEL}}}[$__rate_interval]))', "{{gc}} concurrent (share)", "B"),
], "s", "The longest pause in Micrometer's recent window, by collector and cause; and the share of time concurrent GC threads ran (not a pause).",
    overrides=[{"matcher": {"id": "byRegexp", "options": ".*concurrent.*"},
                "properties": [{"id": "unit", "value": "percentunit"}, {"id": "custom.axisPlacement", "value": "right"}]}]),
    16, 8, 8)
d.advance(8)

# ---------------------------------------------------------------- threads, cpu
d.row("Threads, CPU & process")
d.place(d.ts("Threads", [
    target(f'sum(jvm_threads_live_threads{{{SEL}}})', "live", "A"),
    target(f'sum(jvm_threads_daemon_threads{{{SEL}}})', "daemon", "B"),
    target(f'sum(jvm_threads_peak_threads{{{SEL}}})', "peak", "C"),
], "short", "Live, daemon and peak thread counts. Live threads that only climb are a thread leak.",
    overrides=[dashed("peak", "gray")]), 0, 6, 8)
d.place(d.ts("Thread states", [target(
    f'sum by (state) (jvm_threads_states_threads{{{SEL}}})', "{{state}}")], "short",
    "Threads per state. Many blocked threads means lock contention.", stack=True,
    overrides=[color_override("blocked", "red"), color_override("runnable", "green")]), 6, 6, 8)
d.place(d.ts("CPU", [
    target(f'max by (host_name) (process_cpu_usage{{{SEL}}})', "process {{host_name}}", "A"),
    target(f'max by (host_name) (system_cpu_usage{{{SEL}}})', "system {{host_name}}", "B"),
], "percentunit", "The JVM's and the whole machine's CPU, each as a share of all cores.", max1=True), 12, 6, 8)
d.place(d.ts("Open files & classes", [
    target(f'sum(process_files_open_files{{{SEL}}})', "open files", "A"),
    target(f'sum(jvm_classes_loaded_classes{{{SEL}}})', "loaded classes", "B"),
], "short", "File descriptors the JVM holds open (its limit is process_files_max_files, usually far above) and classes currently loaded. Either climbing without end is a leak.",
    overrides=[right_axis("loaded classes", "short")]), 18, 6, 8)
d.advance(8)

# ---------------------------------------------------------------- hikari
d.row("Database connection pool (Hikari)")
d.place(d.ts("Connections", [
    target(f'sum by (pool) ({HK}_active{{{SEL}}})', "{{pool}} active", "A"),
    target(f'sum by (pool) ({HK}_idle{{{SEL}}})', "{{pool}} idle", "B"),
    target(f'sum by (pool) ({HK}_pending{{{SEL}}})', "{{pool}} pending", "C"),
    target(f'sum by (pool) ({HK}_max{{{SEL}}})', "{{pool}} max", "D"),
], "short", "Connections checked out (active), ready (idle), threads waiting for one (pending), against the pool maximum.",
    overrides=[regex_color(".* pending", "red"), regex_color(".* active", "orange"),
               {"matcher": {"id": "byRegexp", "options": ".* max"},
                "properties": [{"id": "custom.lineStyle", "value": {"fill": "dash", "dash": [10, 10]}},
                               {"id": "color", "value": {"mode": "fixed", "fixedColor": "gray"}}]}]), 0, 8, 8)
d.place(d.ts("Acquire time", [
    target(avg_of(f"{HK}_acquire_seconds", "pool"), "{{pool}} mean", "A"),
    target(f'max by (pool) ({HK}_acquire_seconds_max{{{SEL}}})', "{{pool}} max", "B"),
], "s", "How long getting a connection from the pool took: mean over the interval and the recent maximum. Anything above milliseconds means the pool is exhausted."),
    8, 8, 8)
d.place(d.ts("Timeouts, usage & creation", [
    target(f'sum by (pool) (increase({HK}_timeout_total{{{SEL}}}[$__rate_interval]))', "{{pool}} timeouts", "A"),
    target(avg_of(f"{HK}_usage_seconds", "pool"), "{{pool}} mean usage", "B"),
    target(avg_of(f"{HK}_creation_seconds", "pool"), "{{pool}} mean creation", "C"),
], "short", "Requests that gave up waiting for a connection (should be zero); how long a connection is held per use; how long opening one took.",
    overrides=[regex_color(".* timeouts", "red"),
               {"matcher": {"id": "byRegexp", "options": ".* mean .*"},
                "properties": [{"id": "unit", "value": "s"}, {"id": "custom.axisPlacement", "value": "right"}]}]),
    16, 8, 8)
d.advance(8)

# ---------------------------------------------------------------- http
d.row("HTTP server (Spring MVC / WebFlux)")
d.place(d.ts("Requests by status", [target(
    f'sum by (status) (rate({HTTP}_count{{{SEL}}}[$__rate_interval]))', "{{status}}")], "reqps",
    "Requests the application served, per status code (Micrometer http.server.requests).", stack=True,
    overrides=[regex_color("5..", "red"), regex_color("4..", "orange"), regex_color("2..", "green"),
               regex_color("3..", "blue")]), 0, 8, 8)
d.place(d.ts("Errors by outcome", [
    target(f'sum by (outcome) (rate({HTTP}_count{{{SEL}, outcome=~"SERVER_ERROR|CLIENT_ERROR|UNKNOWN"}}[$__rate_interval])) > 0', "{{outcome}}", "A"),
    target(f'sum by (exception) (rate({HTTP}_count{{{SEL}, exception!~"|None|none"}}[$__rate_interval])) > 0', "{{exception}}", "B"),
], "reqps", "Requests that ended as a client or server error, and those that raised an exception, by exception class.",
    stack=False, overrides=[color_override("SERVER_ERROR", "red"), color_override("CLIENT_ERROR", "orange")]), 8, 8, 8)
d.place(d.ts("Latency", [
    target(avg_of(HTTP), "mean", "A"),
    target(f'histogram_quantile(0.95, sum by (le) (rate({HTTP}_bucket{{{SEL}}}[$__rate_interval])))', "p95", "B"),
    target(f'histogram_quantile(0.99, sum by (le) (rate({HTTP}_bucket{{{SEL}}}[$__rate_interval])))', "p99", "C"),
    target(f'max({HTTP}_max{{{SEL}}})', "max", "D"),
], "s", "Server-side request duration: mean and recent maximum always; p95/p99 only when the application publishes histogram buckets for http.server.requests."),
    16, 8, 8)
d.advance(8)
d.place(d.table("Endpoints (selected range)", [
    target(f'sum by (method, uri) (rate({HTTP}_count{{{SEL}}}[$__range]))', "", "A", instant=True, fmt="table"),
    target(f'(sum by (method, uri) (rate({HTTP}_count{{{SEL}, status=~"5.."}}[$__range])) or sum by (method, uri) (rate({HTTP}_count{{{SEL}}}[$__range])) * 0) / sum by (method, uri) (rate({HTTP}_count{{{SEL}}}[$__range]))', "", "B", instant=True, fmt="table"),
    target(avg_of(HTTP, "method, uri", "$__range"), "", "C", instant=True, fmt="table"),
    target(f'max by (method, uri) (max_over_time({HTTP}_max{{{SEL}}}[$__range]))', "", "D", instant=True, fmt="table"),
], "Every endpoint (Spring's URI template) over the selected range: request rate, 5xx share, mean and worst duration.",
    [("method", "Method"), ("uri", "URI"), ("Value #A", "Req/s"), ("Value #B", "5xx ratio"),
     ("Value #C", "Mean"), ("Value #D", "Max")], sort_by="Req/s",
    overrides=[column_override("Req/s", "reqps", 3),
               column_override("5xx ratio", "percentunit", 2,
                               [{"color": "green", "value": None}, {"color": "orange", "value": 0.001},
                                {"color": "red", "value": 0.01}], "color-text"),
               column_override("Mean", "s", 3), column_override("Max", "s", 3)]), 0, 24, 9)
d.advance(9)

# ---------------------------------------------------------------- tomcat, executors, scheduling
d.row("Tomcat, executors & scheduled tasks")
d.place(d.ts("Tomcat sessions", [
    target(f'sum(tomcat_sessions_active_current_sessions{{{SEL}}})', "active", "A"),
    target(f'sum(rate(tomcat_sessions_created_sessions_total{{{SEL}}}[$__rate_interval])) * 60', "created / min", "B"),
    target(f'sum(rate(tomcat_sessions_expired_sessions_total{{{SEL}}}[$__rate_interval])) * 60', "expired / min", "C"),
    target(f'sum(rate(tomcat_sessions_rejected_sessions_total{{{SEL}}}[$__rate_interval])) * 60', "rejected / min", "D"),
], "short", "HTTP sessions held by embedded Tomcat. Rejected sessions mean maxActiveSessions was reached.",
    overrides=[color_override("rejected / min", "red")]), 0, 8, 8)
d.place(d.ts("Executors", [
    target(f'sum by (name) (executor_active_threads{{{SEL}}})', "{{name}} active", "A"),
    target(f'sum by (name) (executor_queued_tasks{{{SEL}}})', "{{name}} queued", "B"),
    target(f'sum by (name) (executor_pool_max_threads{{{SEL}}} < 1e6)', "{{name}} max", "C"),
], "short", "Spring's task executors: threads busy, tasks waiting, and the pool's maximum (left out when unbounded).",
    overrides=[regex_color(".* queued", "red"),
               {"matcher": {"id": "byRegexp", "options": ".* max"},
                "properties": [{"id": "custom.lineStyle", "value": {"fill": "dash", "dash": [10, 10]}}]}]), 8, 8, 8)
d.place(d.ts("Scheduled tasks", [
    target(avg_of("tasks_scheduled_execution_seconds", "code_function"), "{{code_function}}", "A"),
    target(f'sum by (code_function, exception) (rate(tasks_scheduled_execution_seconds_count{{{SEL}, outcome!="SUCCESS"}}[$__rate_interval])) > 0', "{{code_function}} failed: {{exception}}", "B"),
], "s", "Mean duration of each @Scheduled method; failing runs appear as their own series (per second, by exception).",
    overrides=[{"matcher": {"id": "byRegexp", "options": ".* failed: .*"},
                "properties": [{"id": "unit", "value": "ops"}, {"id": "custom.axisPlacement", "value": "right"},
                               {"id": "color", "value": {"mode": "fixed", "fixedColor": "red"}}]}]),
    16, 8, 8)
d.advance(8)

# ---------------------------------------------------------------- logs
d.row("Logging")
d.place(d.ts("Log events by level", [target(
    f'sum by (level) (rate(logback_events_total{{{SEL}}}[$__rate_interval])) * 60', "{{level}}")], "short",
    "Lines logged per minute by level, from Logback's own counter (only levels that are enabled are counted).",
    stack=True, overrides=LEVEL_COLORS, draw="bars"), 0, 16, 8)
d.place(d.ts("Warnings & errors / min", [target(
    f'sum by (level) (rate(logback_events_total{{{SEL}, level=~"warn|error"}}[$__rate_interval])) * 60', "{{level}}")],
    "short", "Only WARN and ERROR, so a burst is not hidden under INFO.", overrides=LEVEL_COLORS), 16, 8, 8)
d.advance(8)

emit(d, "pvps-jvm", "Role — JVM / Spring Boot",
     "Any Spring Boot (Micrometer) application whose Prometheus endpoint the host collector reads (portablevps.telemetry.prometheusTargets): JVM memory, GC, threads and CPU; the Hikari pool; HTTP server requests; Tomcat, executors and scheduled tasks; log events by level. Pick the service at the top; links can pass ?var-service=<name>.",
     ["jvm", "spring"], "jvm_threads_live_threads")

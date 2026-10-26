#!/usr/bin/env python3
"""Generates role-postgres.json, the "Role — PostgreSQL" dashboard (uid pvps-postgres).

Edit the panels here, then from this directory:
    python3 postgres.py > ../dashboards/role-postgres.json
(commit both; see dash.py for regenerating every role dashboard at once).

Source: the collector's `postgresql` receiver, on hosts that set
portablevps.postgres.metrics.enable. Its names differ from postgres_exporter's:
postgresql_backends_ratio (connections), postgresql_rows_ratio{state}
(live/dead rows), postgresql_operations_total{operation} (ins/upd/hot_upd/del),
and postgresql_index_scans_total names its table without the schema while the
table metrics include it. Logs are the postgres container's journald lines in
VictoriaLogs.
"""
from dash import (SEL, Dashboard, color_override, column_override, dashed, emit, right_axis, target)

d = Dashboard()

def hit_ratio(w):
    h = f'sum(rate(postgresql_blks_hit_total{{{SEL}}}[{w}]))'
    r = f'sum(rate(postgresql_blks_read_total{{{SEL}}}[{w}]))'
    return f'{h} / ({h} + {r})'


CT = f'{SEL}, container_name=~"postgres.*"'
TBL = "postgresql_database_name, postgresql_table_name"
PG_LOGS = 'service.name:="$service" AND CONTAINER_NAME:~"^postgres"'
LEVEL = 'extract "] <level>:" | filter level:~"^[A-Z]+$"'

# ---------------------------------------------------------------- overview
d.row("Overview")
d.place(d.stat("Connections used", f'sum(postgresql_backends_ratio{{{SEL}}}) / max(postgresql_connection_max{{{SEL}}})',
               "percentunit", "Open backends across all databases as a share of max_connections (the metrics reader's own connection included).",
               [{"color": "green", "value": None}, {"color": "orange", "value": 0.7}, {"color": "red", "value": 0.9}],
               decimals=0), 0, 4, 4)
d.place(d.stat("Transactions / s", f'sum(rate(postgresql_commits_total{{{SEL}}}[$__rate_interval])) + sum(rate(postgresql_rollbacks_total{{{SEL}}}[$__rate_interval]))',
               "ops", "Committed plus rolled-back transactions per second."), 4, 4, 4)
d.place(d.stat("Rollback share (range)",
               f'sum(increase(postgresql_rollbacks_total{{{SEL}}}[$__range])) / (sum(increase(postgresql_commits_total{{{SEL}}}[$__range])) + sum(increase(postgresql_rollbacks_total{{{SEL}}}[$__range])))',
               "percentunit", "Share of transactions over the range that rolled back. Some frameworks roll back read-only transactions by design; watch for a change, not a level.",
               [{"color": "green", "value": None}, {"color": "orange", "value": 0.05}, {"color": "red", "value": 0.2}],
               decimals=2), 8, 4, 4)
d.place(d.stat("Cache hit (range)", hit_ratio("$__range"), "percentunit",
               "Share of block reads served from shared buffers over the range. Healthy OLTP is above 99%; no reads at all shows no value.",
               [{"color": "red", "value": None}, {"color": "orange", "value": 0.95}, {"color": "green", "value": 0.99}],
               decimals=2), 12, 4, 4)
d.place(d.stat("Database size", f'sum(postgresql_db_size_bytes{{{SEL}}})', "bytes",
               "On-disk size of every database the receiver reads."), 16, 4, 4)
d.place(d.stat("Deadlocks (range)", f'sum(increase(postgresql_deadlocks_total{{{SEL}}}[$__range]))', "short",
               "Deadlocks Postgres detected and broke by aborting a transaction, over the range. Should be zero.",
               [{"color": "green", "value": None}, {"color": "red", "value": 1}], decimals=0, graph=False), 20, 4, 4)
d.advance(4)

# ---------------------------------------------------------------- activity
d.row("Activity")
d.place(d.ts("Connections", [
    target(f'sum by (postgresql_database_name) (postgresql_backends_ratio{{{SEL}}})', "{{postgresql_database_name}}", "A"),
    target(f'max(postgresql_connection_max{{{SEL}}})', "max_connections", "B"),
], "short", "Open backends per database against the cluster's max_connections.", overrides=[dashed("max_connections")]),
    0, 8, 8)
d.place(d.ts("Transactions / s", [
    target(f'sum(rate(postgresql_commits_total{{{SEL}}}[$__rate_interval]))', "commits", "A"),
    target(f'sum(rate(postgresql_rollbacks_total{{{SEL}}}[$__rate_interval]))', "rollbacks", "B"),
], "ops", "Committed and rolled-back transactions. Rollbacks climbing while commits stay flat points at failing statements.",
    overrides=[color_override("commits", "green"), color_override("rollbacks", "red")]), 8, 8, 8)
d.place(d.ts("Cache hit ratio", [target(hit_ratio("$__rate_interval"), "hit ratio")], "percentunit",
             "Shared-buffer hits over all block reads. Sustained under 99% means the working set no longer fits in memory.",
             min0=False, max1=True), 16, 8, 8)
d.advance(8)
d.place(d.ts("Tuple activity / s", [
    target(f'sum(rate(postgresql_tup_returned_total{{{SEL}}}[$__rate_interval]))', "returned", "A"),
    target(f'sum(rate(postgresql_tup_fetched_total{{{SEL}}}[$__rate_interval]))', "fetched", "B"),
    target(f'sum(rate(postgresql_tup_inserted_total{{{SEL}}}[$__rate_interval]))', "inserted", "C"),
    target(f'sum(rate(postgresql_tup_updated_total{{{SEL}}}[$__rate_interval]))', "updated", "D"),
    target(f'sum(rate(postgresql_tup_deleted_total{{{SEL}}}[$__rate_interval]))', "deleted", "E"),
], "ops", "Rows read and written. 'returned' far above 'fetched' means sequential scans read rows they then discard."),
    0, 8, 8)
d.place(d.ts("Scans / s", [
    target(f'sum(rate(postgresql_index_scans_total{{{SEL}}}[$__rate_interval]))', "index scans", "A"),
    target(f'sum(rate(postgresql_sequential_scans_total{{{SEL}}}[$__rate_interval]))', "sequential scans", "B"),
], "ops", "Index against sequential scans over all tables. Sequential scans on small tables are fine; a rising share on a growing table usually means a missing index."),
    8, 8, 8)
d.place(d.ts("Deadlocks, temp files & checkpoints", [
    target(f'sum(increase(postgresql_deadlocks_total{{{SEL}}}[$__rate_interval]))', "deadlocks", "A"),
    target(f'sum(increase(postgresql_temp_files_total{{{SEL}}}[$__rate_interval]))', "temp files", "B"),
    target(f'sum by (type) (increase(postgresql_bgwriter_checkpoint_count_total{{{SEL}}}[$__rate_interval]))', "checkpoints ({{type}})", "C"),
], "short", "Deadlocks should stay at zero; a temp file is a sort or hash that spilled past work_mem; requested checkpoints include those a backup forces.",
    overrides=[color_override("deadlocks", "red")], draw="bars"), 16, 8, 8)
d.advance(8)

# ---------------------------------------------------------------- storage
d.row("Storage & tables")
d.place(d.ts("Database size", [target(
    f'sum by (postgresql_database_name) (postgresql_db_size_bytes{{{SEL}}})', "{{postgresql_database_name}}")],
    "bytes", "On-disk size per database."), 0, 8, 8)
d.place(d.ts("Largest tables", [target(
    f'topk(10, sum by ({TBL}) (postgresql_table_size_bytes{{{SEL}}}))', "{{postgresql_table_name}}")],
    "bytes", "Ten largest tables (heap and TOAST, without indexes)."), 8, 8, 8)
d.place(d.ts("Dead rows", [target(
    f'topk(10, sum by ({TBL}) (postgresql_rows_ratio{{{SEL}, state="dead"}})) > 0', "{{postgresql_table_name}}")],
    "short", "Dead tuples awaiting vacuum, top 10 tables. A table that keeps growing here is not vacuumed fast enough."),
    16, 8, 8)
d.advance(8)
d.place(d.table("Tables (selected range)", [
    target(f'sum by ({TBL}) (postgresql_table_size_bytes{{{SEL}}})', "", "A", instant=True, fmt="table"),
    target(f'sum by ({TBL}) (postgresql_rows_ratio{{{SEL}, state="live"}})', "", "B", instant=True, fmt="table"),
    target(f'sum by ({TBL}) (postgresql_rows_ratio{{{SEL}, state="dead"}})', "", "C", instant=True, fmt="table"),
    target(f'sum by ({TBL}) (postgresql_rows_ratio{{{SEL}, state="dead"}}) / clamp_min(sum by ({TBL}) (postgresql_rows_ratio{{{SEL}}}), 1)', "", "D", instant=True, fmt="table"),
    target(f'sum by ({TBL}) (increase(postgresql_operations_total{{{SEL}}}[$__range]))', "", "E", instant=True, fmt="table"),
    target(f'sum by ({TBL}) (increase(postgresql_sequential_scans_total{{{SEL}}}[$__range]))', "", "F", instant=True, fmt="table"),
    target(f'sum by ({TBL}) (increase(postgresql_table_vacuum_count_total{{{SEL}}}[$__range]))', "", "G", instant=True, fmt="table"),
], "Every table the receiver reads: size, live and dead rows now; row writes, sequential scans and vacuums over the selected range.",
    [("postgresql_database_name", "Database"), ("postgresql_table_name", "Table"), ("Value #A", "Size"),
     ("Value #B", "Live rows"), ("Value #C", "Dead rows"), ("Value #D", "Dead share"), ("Value #E", "Writes"),
     ("Value #F", "Seq scans"), ("Value #G", "Vacuums")], sort_by="Size",
    overrides=[column_override("Size", "bytes"), column_override("Live rows", "short", 0),
               column_override("Dead rows", "short", 0),
               column_override("Dead share", "percentunit", 1,
                               [{"color": "green", "value": None}, {"color": "orange", "value": 0.1},
                                {"color": "red", "value": 0.2}], "color-text"),
               column_override("Writes", "short", 0), column_override("Seq scans", "short", 0),
               column_override("Vacuums", "short", 0)]), 0, 24, 9)
d.advance(9)
d.place(d.ts("Row writes per table / s", [target(
    f'topk(10, sum by (postgresql_table_name, operation) (rate(postgresql_operations_total{{{SEL}}}[$__rate_interval]))) > 0',
    "{{postgresql_table_name}} {{operation}}")],
    "ops", "Inserts (ins), updates (upd, hot_upd: those that needed no index change) and deletes (del) per table, top 10 series."),
    0, 8, 8)
d.place(d.ts("Sequential scans per table / s", [target(
    f'topk(10, sum by (postgresql_table_name) (rate(postgresql_sequential_scans_total{{{SEL}}}[$__rate_interval]))) > 0',
    "{{postgresql_table_name}}")], "ops", "Tables read by sequential scan, top 10. A large table near the top is the index to add."),
    8, 8, 8)
d.place(d.ts("Largest indexes", [target(
    f'topk(10, sum by (postgresql_index_name) (postgresql_index_size_bytes{{{SEL}}}))', "{{postgresql_index_name}}")],
    "bytes", "Ten largest indexes."), 16, 8, 8)
d.advance(8)

# ---------------------------------------------------------------- bgwriter
d.row("Checkpoints, background writer & container")
d.place(d.ts("Buffers written / s", [target(
    f'sum by (source) (rate(postgresql_bgwriter_buffers_writes_total{{{SEL}}}[$__rate_interval]))', "{{source}}")],
    "ops", "Buffers written by checkpoints and by the background writer.", stack=True), 0, 6, 8)
d.place(d.ts("Checkpoint time", [target(
    f'sum by (type) (rate(postgresql_bgwriter_duration_milliseconds_total{{{SEL}}}[$__rate_interval])) / 1000', "{{type}}")],
    "percentunit", "Share of wall time spent writing and syncing checkpoints. A sync share that grows means the disk is the bottleneck.",
    stack=True), 6, 6, 8)
d.place(d.ts("Buffers allocated / s", [target(
    f'sum(rate(postgresql_bgwriter_buffers_allocated_total{{{SEL}}}[$__rate_interval]))', "allocated")],
    "ops", "New buffers allocated in shared buffers: how fast the working set moves through memory."), 12, 6, 8)
d.place(d.ts("Postgres container", [
    target(f'sum(rate(container_cpu_usage_seconds_total{{{CT}}}[$__rate_interval]))', "CPU (cores)", "A"),
    target(f'sum(container_memory_usage_total_bytes{{{CT}}})', "memory", "B"),
], "short", "CPU and memory of the service's postgres container(s), from podman stats. Empty when PostgreSQL does not run in a container named postgres*.",
    overrides=[right_axis("memory", "bytes")]), 18, 6, 8)
d.advance(8)

# ---------------------------------------------------------------- logs
d.row("Logs")
d.place(d.vl_range("Log lines by level", f'{PG_LOGS} | {LEVEL} | stats by (level) count() lines', "{{level}}", "short",
                   "PostgreSQL log lines per level (LOG, ERROR, FATAL, …), read from the message prefix. The postgres container of the selected service, all hosts.",
                   overrides=[color_override("ERROR", "red"), color_override("FATAL", "dark-red"),
                              color_override("PANIC", "purple"), color_override("WARNING", "orange"),
                              color_override("LOG", "green"), color_override("STATEMENT", "blue")]), 0, 24, 7)
d.advance(7)
d.place(d.logs("Errors & warnings", f'{PG_LOGS} AND _msg:~"(ERROR|FATAL|PANIC|WARNING):"',
               "ERROR, FATAL, PANIC and WARNING lines. The statement that caused an error follows on its own STATEMENT line in the full log below."),
        0, 24, 9)
d.advance(9)
d.place(d.logs("PostgreSQL log", PG_LOGS,
               "Everything the postgres container logged: checkpoints, connections refused, slow or failing statements."), 0, 24, 10)
d.advance(10)

emit(d, "pvps-postgres", "Role — PostgreSQL",
     "Any service's PostgreSQL, read by the host collector's postgresql receiver (portablevps.postgres.metrics.enable): connections, transactions, cache hit, scans, tables and dead rows, checkpoints, and the postgres container's log. Pick the service at the top; links can pass ?var-service=<name>.",
     ["postgres"], "postgresql_connection_max")

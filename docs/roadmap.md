# portablevps roadmap / backlog

Deferred features and ideas, with enough design context to pick up later.

## Continuous inter-host mesh latency monitoring

**Goal:** constantly measure latency and reachability between hosts on the mesh,
so a degraded link or a partition between two servers (or two providers) is
visible and alertable — not something you discover by hand with `ping`.

**Why it fits:** every monitored host already ships operational telemetry to the
monitoring server over NetBird. Mesh RTT belongs in the same monitoring plane.

**Design sketch (lightweight first cut):**
- A small systemd timer on each host pings every other portablevps peer's
  NetBird IP and reports OTLP metrics, e.g.
  `portablevps_mesh_rtt_seconds{peer="leaseweb-1"}` and
  `portablevps_mesh_up{peer="leaseweb-1"}`.
- Peer list comes from the NetBird peer set (or the `portablevps-servers`
  group), so it stays current as servers are added/removed.
- The monitoring server stores the per-peer RTT series in VictoriaMetrics; alert
  on RTT above a threshold or `mesh_up == 0` (partition).

**Richer alternative:** run `prometheus/blackbox_exporter` with ICMP probes to
peer mesh IPs instead of a custom timer — gives probe success/duration and is
the more standard building block; heavier. Smokeping-style history is a third
option.

**Open questions:** who owns the peer list (mesh query vs. declared servers);
whether to probe only mesh IPs or also a TCP service; how it interacts with the
NetBird access policies (ICMP must be permitted between the probing host and its
peers).

## Deferred (tracked elsewhere)

- Resource plane vs. machine plane / OpenTofu direction — see
  `docs/adr/0001-resource-plane-vs-machine-plane.md`.
- NetBird access policy default-deny automation and richer topology — being
  built now; the default-allow-all disable is a deliberate opt-in.
- Scheduled separate-host restore rehearsals; retention/observability polish;
  a second real provider lifecycle adapter (netcup / Leaseweb).

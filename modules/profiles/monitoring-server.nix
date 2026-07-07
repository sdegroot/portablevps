# Reusable shape for a dedicated observability server. Unlike single-instance-app
# it runs no PostgreSQL / container-state / app-backup machinery — the monitoring
# stack (portablevps.apps.monitoring) stores its own metrics/logs and is treated
# as disposable. It still gets podman, the mesh proxy (for the UI names), and the
# apps aggregator. The server definition enables and configures the monitoring
# app and its proxy routes.
{ ... }:

{
  imports = [
    ../runtime/podman.nix
    ../networking/service-exposure.nix
    ../networking/proxy.nix
    # Declares portablevps.backups.* (which core deployment/secrets modules wire
    # on every host). A monitoring server registers no components, so the
    # scheduled backup timers stay disarmed.
    ../system/backups.nix
    # Only the monitoring app — not the whole apps aggregator — so a mon server
    # doesn't pull in other apps' service wiring (e.g. authentik's backup
    # components, whose declaring module this profile intentionally omits).
    ../../apps/monitoring
  ];
}

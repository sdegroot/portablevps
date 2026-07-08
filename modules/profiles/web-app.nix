# Reusable shape for a stateless containerised web app (e.g. an Astro SSR site).
# Like monitoring-server, it runs no PostgreSQL / container-state / app-backup
# machinery — the app holds no state (content/assets live in object storage / a
# CDN). It gets podman, the mesh proxy (for the app's name), and only the website
# app module. The server definition enables and configures portablevps.apps.website
# and its proxy route.
{ ... }:

{
  imports = [
    ../runtime/podman.nix
    ../networking/service-exposure.nix
    ../networking/proxy.nix
    # Declares portablevps.backups.* (core deployment/secrets modules reference it
    # on every host). A web-app server registers no components, so the scheduled
    # backup timers stay disarmed.
    ../system/backups.nix
    # Only the website app — not the whole apps aggregator — so a web-app server
    # doesn't pull in other apps' service/backup wiring.
    ../../apps/website
  ];
}

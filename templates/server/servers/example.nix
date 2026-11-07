# A logical server. Copy this file per server (the file name is the
# server name) and adjust the placement, identity, and routing.
{ ... }:

let
  name = "example";

  # Which provider this server currently runs on. Provider metadata comes from
  # portablevps' built-in providers/ (or this repo's providers/ if present).
  # The public addresses are informational: `server list` shows them so an IP
  # from an invoice, abuse report or log can be traced to this machine. Take
  # them from `ip -br addr` on the host; nothing connects to them.
  placement = {
    provider = "hetzner";
    # publicIPv4 = "203.0.113.10";
    # publicIPv6 = "2001:db8::10";
  };
in
{
  inherit name;

  # Reusable server shape. "single-instance-app" is a PostgreSQL app server
  # with coordinated backup/restore; or set this to a path for a custom profile.
  profile = "single-instance-app";

  inherit placement;

  # Metadata consumed by both Nix and the portablevps CLI.
  info = {
    inherit name placement;
    provider = "hetzner";
    hostname = name;
    netbirdName = name;
    # Per-server restic repository prefix inside your backup bucket.
    backupRepository = "s3:https://s3.example.com/my-backups/servers/${name}/restic";
  };

  # NixOS configuration for this specific server.
  module = { lib, ... }: {
    networking.hostName = lib.mkDefault name;

    # The admin key from `pvps key generate` and the secrets file from
    # `pvps secret init`.
    portablevps.base.adminAuthorizedKeyFiles = [ ../keys/servers/example-admin.pub ];
    portablevps.secrets.file = ../secrets/example.yaml;

    portablevps.server = {
      inherit name;
      provider = "hetzner";
      backupRepository = "s3:https://s3.example.com/my-backups/servers/${name}/restic";
    };

    # Mesh VPN peer name. Set backend = "tailscale" to use Tailscale instead.
    portablevps.network.name = lib.mkDefault name;

    # Run your own container as an app — no NixOS module needed. Swap the image
    # and port for your app. For a STATEFUL app, add `volumes`: their host paths
    # are created and automatically included in the coordinated restic backup,
    # so a restore brings the data back with the machine.
    portablevps.apps.custom.app = {
      enable = true;
      image = "docker.io/library/nginx:1.30.6"; # use an immutable tag
      port = 80;
      # volumes = [ { hostPath = "/data/app"; containerPath = "/var/lib/app"; } ];
      # uid = 1000;                               # if the image runs as non-root
      # env = { LOG_LEVEL = "info"; };            # plain env vars
      # secretEnv = { API_KEY = "app/api-key"; }; # env var <- sops key
    };

    # Expose the app over the mesh (and optionally the public edge) with TLS.
    portablevps.proxy = {
      enable = true;
      acme = { email = "ops@example.com"; dnsProvider = "desec"; };
      http.services.app = {
        domain = "app.example.com";
        upstream = "http://127.0.0.1:80";
        visibility = "internal"; # or "netbird-edge" for public
      };
    };
  };
}
